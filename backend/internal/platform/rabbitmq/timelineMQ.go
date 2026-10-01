package rabbitmq

import (
	"context"
	"errors"
	"time"
)

const (
	TimelineExchange          = "timeline.events"
	TimelineQueue             = "timeline.queue"
	TimelineRoutingKey        = "timeline.*"
	TimelinePublishRoutingKey = "timeline.publish"
	TimelineRetryModule       = "timeline"
	TimelineRetryExchange     = TimelineRetryModule + ".retry.events"
)

type TimelineEvent struct {
	EventID    string    `json:"event_id"`
	AuthorID   uint      `json:"author_id"`
	ContentID  uint      `json:"content_id"`
	CreateTime int64     `json:"create_time"`
	OccurredAt time.Time `json:"occurred_at"`
}

type TimelineMQ struct {
	publisher *confirmedPublisher
}

// NewTimelineMQ 创建时间线消息队列
func NewTimelineMQ(base *RabbitMQ) (*TimelineMQ, error) {
	if base == nil {
		return nil, errors.New("rabbitmq base is nil")
	}
	publisher, err := newConfirmedPublisher(base, TimelineExchange, TimelineQueue, TimelineRoutingKey)
	if err != nil {
		return nil, err
	}
	//声明重试交换机和延迟队列
	if err := DeclareModuleRetryTopologies(
		publisher.ch,
		TimelineRetryModule,
		TimelineExchange,
		TimelinePublishRoutingKey,
	); err != nil {
		_ = publisher.ch.Close()
		return nil, err
	}
	return &TimelineMQ{publisher: publisher}, nil
}

// Publish 将已持久化的时间线事件发布到消息队列。
// EventID 由创建 Outbox 的事务生成，重试时保持不变，方便消费者识别同一业务事件。
func (t *TimelineMQ) Publish(ctx context.Context, event TimelineEvent) error {
	if t == nil || t.publisher == nil {
		return errors.New("timeline mq is not initialized")
	}
	if event.EventID == "" || event.ContentID == 0 || event.AuthorID == 0 {
		return errors.New("timeline event is invalid")
	}
	return t.publisher.publish(ctx, TimelineExchange, TimelinePublishRoutingKey, event)
}

func (t *TimelineMQ) PublishPayloads(ctx context.Context, payloads [][]byte) []error {
	if t == nil || t.publisher == nil {
		return batchPublisherNotInitialized(len(payloads), "timeline mq")
	}
	return t.publisher.publishRawBatch(ctx, TimelineExchange, TimelinePublishRoutingKey, payloads)
}
