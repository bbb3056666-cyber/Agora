package rabbitmq

import (
	"context"
	"errors"
	"time"
)

const (
	LikeIntentExchange      = "like.intent.events"
	LikeIntentQueue         = "like.intent.events"
	LikeIntentRoutingKey    = "like.intent"
	LikeIntentRetryModule   = "like.intent"
	LikeIntentRetryExchange = LikeIntentRetryModule + ".retry.events"

	LikeAppliedExchange   = "like.applied.events"
	LikeAppliedRoutingKey = "like.applied"

	NotificationLikeQueue = "notification.like"
)

// LikeMQ 管理点赞模块的两条事件链路：用户意图和已生效事实。
// 两者各有独立 Channel，避免其中一条 Confirm 等待阻塞另一条。
type LikeMQ struct {
	intentPublisher  *confirmedPublisher
	appliedPublisher *confirmedPublisher
}

func NewLikeMQ(base *RabbitMQ) (*LikeMQ, error) {
	intentPublisher, err := newConfirmedPublisher(base, LikeIntentExchange, LikeIntentQueue, LikeIntentRoutingKey)
	if err != nil {
		return nil, err
	}
	//LikeIntentWorker 消费意图失败后的重试
	if err := DeclareModuleRetryTopologies(intentPublisher.ch, LikeIntentRetryModule, LikeIntentExchange, LikeIntentRoutingKey); err != nil {
		_ = intentPublisher.ch.Close()
		return nil, err
	}

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
	return m.intentPublisher.publish(ctx, LikeIntentExchange, LikeIntentRoutingKey, intent)
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
	return m.intentPublisher.publishRawBatch(ctx, LikeIntentExchange, LikeIntentRoutingKey, payloads)
}

func (m *LikeMQ) PublishAppliedPayloads(ctx context.Context, payloads [][]byte) []error {
	if m == nil {
		return batchPublisherNotInitialized(len(payloads), "like mq")
	}
	return m.appliedPublisher.publishRawBatch(ctx, LikeAppliedExchange, LikeAppliedRoutingKey, payloads)
}

// LikeIntent 是用户的点赞意图；Version 按同一用户和视频单调递增。
type LikeIntent struct {
	EventID    string    `json:"event_id"`
	UserID     uint      `json:"user_id"`
	ContentID  uint      `json:"content_id"`
	Action     string    `json:"action"`
	Version    uint64    `json:"version"`
	OccurredAt time.Time `json:"occurred_at"`
}

// LikeApplied 是主业务事务提交后的事实事件，热度和通知都能消费它。
type LikeApplied struct {
	EventID          string    `json:"event_id"`
	IntentID         string    `json:"intent_id"`
	UserID           uint      `json:"user_id"`
	ContentID        uint      `json:"content_id"`
	Action           string    `json:"action"`
	Change           int64     `json:"change"`            // 点赞数变化：like=1，unlike=-1。
	PopularityChange int64     `json:"popularity_change"` //只在用户首次点赞该视频时为 1；取消和重复点赞均为 0。
	Version          uint64    `json:"version"`
	OccurredAt       time.Time `json:"occurred_at"`
}
