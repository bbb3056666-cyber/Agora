package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	LikeIntentExchange      = "like.intent.events"
	LikeIntentQueuePrefix   = "like.intent.events"
	LikeIntentRoutingPrefix = "like.intent"
	LikeIntentRetryPrefix   = "like.intent"

	LikeAppliedExchange   = "like.applied.events"
	LikeAppliedRoutingKey = "like.applied"

	NotificationLikeQueue = "notification.like"
	LikeIntentPartitions  = 8
)

func LikeIntentPartition(contentID uint) int {
	return int(contentID % LikeIntentPartitions)
}

func LikeIntentPartitionQueue(partition int) string {
	return fmt.Sprintf("%s.p%d", LikeIntentQueuePrefix, partition)
}

func LikeIntentPartitionKey(partition int) string {
	return fmt.Sprintf("%s.p%d", LikeIntentRoutingPrefix, partition)
}

func LikeIntentPartitionRetryModule(partition int) string {
	return fmt.Sprintf("%s.p%d", LikeIntentRetryPrefix, partition)
}

func LikeIntentRetryModuleForQueue(queue string) string {
	for partition := 0; partition < LikeIntentPartitions; partition++ {
		if queue == LikeIntentPartitionQueue(partition) {
			return LikeIntentPartitionRetryModule(partition)
		}
	}
	return ""
}

// 同一视频始终进入同一队列；单活消费者保证多个 Worker 实例也不会并发消费该分区。
func DeclareLikeIntentPartitions(ch *amqp.Channel) error {
	if ch == nil {
		return errors.New("like intent channel is nil")
	}
	if err := ch.ExchangeDeclare(LikeIntentExchange, "topic", true, false, false, false, nil); err != nil {
		return err
	}
	for partition := 0; partition < LikeIntentPartitions; partition++ {
		queue := LikeIntentPartitionQueue(partition)
		key := LikeIntentPartitionKey(partition)
		q, err := ch.QueueDeclare(queue, true, false, false, false, amqp.Table{
			"x-single-active-consumer":  true,
			"x-dead-letter-exchange":    DLX,
			"x-dead-letter-routing-key": queue,
		})
		if err != nil {
			return err
		}
		if err := ch.QueueBind(q.Name, key, LikeIntentExchange, false, nil); err != nil {
			return err
		}
		if err := DeclareDLX(ch, queue); err != nil {
			return err
		}
		if err := DeclareModuleRetryTopologies(ch, LikeIntentPartitionRetryModule(partition), LikeIntentExchange, key); err != nil {
			return err
		}
	}
	return nil
}

// LikeMQ 管理点赞模块的两条事件链路：用户意图和已生效事实。
// 两者各有独立 Channel，避免其中一条 Confirm 等待阻塞另一条。
type LikeMQ struct {
	intentPublisher  *confirmedPublisher
	appliedPublisher *confirmedPublisher
}

func NewLikeMQ(base *RabbitMQ) (*LikeMQ, error) {
	if base == nil {
		return nil, errors.New("rabbitmq base is nil")
	}
	intentCh, err := base.NewChannel()
	if err != nil {
		return nil, err
	}
	if err := intentCh.Confirm(false); err != nil {
		_ = intentCh.Close()
		return nil, err
	}
	if err := DeclareLikeIntentPartitions(intentCh); err != nil {
		_ = intentCh.Close()
		return nil, err
	}
	intentPublisher := &confirmedPublisher{ch: intentCh, confirms: intentCh.NotifyPublish(make(chan amqp.Confirmation, 256))}

	appliedPublisher, err := newConfirmedPublisher(base, LikeAppliedExchange, PopularityQueue, LikeAppliedRoutingKey)
	if err != nil {
		_ = intentPublisher.ch.Close()
		return nil, err
	}
	return &LikeMQ{intentPublisher: intentPublisher, appliedPublisher: appliedPublisher}, nil
}

func (m *LikeMQ) PublishIntent(ctx context.Context, intent LikeIntent) error {
	if m == nil {
		return errors.New("like mq is not initialized")
	}
	if intent.UserID == 0 || intent.ContentID == 0 {
		return errors.New("invalid like intent")
	}
	return m.intentPublisher.publish(ctx, LikeIntentExchange, LikeIntentPartitionKey(LikeIntentPartition(intent.ContentID)), intent)
}

func (m *LikeMQ) PublishApplied(ctx context.Context, applied LikeApplied) error {
	if m == nil {
		return errors.New("like mq is not initialized")
	}
	return m.appliedPublisher.publish(ctx, LikeAppliedExchange, LikeAppliedRoutingKey, applied)
}

// PublishIntentPayloads 是 Outbox 轮询器的批量入口：同一 Publisher 连续发送，
// 再统一收取 Confirm，避免每条消息都执行一次“发送后等待”。
func (m *LikeMQ) PublishIntentPayloads(ctx context.Context, payloads [][]byte) []error {
	if m == nil {
		return batchPublisherNotInitialized(len(payloads), "like mq")
	}
	routingKeys := make([]string, len(payloads))
	for i, payload := range payloads {
		var intent LikeIntent
		if err := json.Unmarshal(payload, &intent); err != nil || intent.ContentID == 0 {
			// 无效消息交给分区消费者按原有规则丢弃，避免阻塞同批其他 Outbox 消息。
			routingKeys[i] = LikeIntentPartitionKey(0)
			continue
		}
		routingKeys[i] = LikeIntentPartitionKey(LikeIntentPartition(intent.ContentID))
	}
	return m.intentPublisher.publishRoutedBatch(ctx, LikeIntentExchange, routingKeys, payloads)
}

func (m *LikeMQ) PublishAppliedPayloads(ctx context.Context, payloads [][]byte) []error {
	if m == nil {
		return batchPublisherNotInitialized(len(payloads), "like mq")
	}
	return m.appliedPublisher.publishRawBatch(ctx, LikeAppliedExchange, LikeAppliedRoutingKey, payloads)
}

// LikeIntent 只负责通知 Worker 某条点赞关系需要重新对账。
// 最新期望状态保存在 MySQL，而不是消息中，因此重复或乱序投递不影响最终结果。
type LikeIntent struct {
	UserID    uint `json:"user_id"`
	ContentID uint `json:"content_id"`
}

// LikeApplied 是主业务事务提交后的事实事件，热度和通知都能消费它。
type LikeApplied struct {
	EventID          string    `json:"event_id"`
	UserID           uint      `json:"user_id"`
	ContentID        uint      `json:"content_id"`
	Action           string    `json:"action"`
	Change           int64     `json:"change"`            // 点赞数变化：like=1，unlike=-1。
	PopularityChange int64     `json:"popularity_change"` //只在用户首次点赞该视频时为 1；取消和重复点赞均为 0。
	OccurredAt       time.Time `json:"occurred_at"`
}
