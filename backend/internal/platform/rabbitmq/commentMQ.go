package rabbitmq

import (
	"context"
	"errors"
	"time"
)

const (
	CommentIntentExchange      = "comment.intent.events"
	CommentIntentQueue         = "comment.intent.events"
	CommentIntentRoutingKey    = "comment.intent"
	CommentIntentRetryModule   = "comment.intent"
	CommentIntentRetryExchange = CommentIntentRetryModule + ".retry.events"

	CommentAppliedExchange   = "comment.applied.events"
	CommentAppliedRoutingKey = "comment.applied"

	NotificationCommentQueue = "notification.comment"
)

// CommentMQ 管理评论意图和评论已生效事实两条可靠投递链路。
// Publisher Confirm 由 Outbox Poller 在后台等待，不阻塞 HTTP 请求。
type CommentMQ struct {
	intentPublisher  *confirmedPublisher
	appliedPublisher *confirmedPublisher
}

func NewCommentMQ(base *RabbitMQ) (*CommentMQ, error) {
	intentPublisher, err := newConfirmedPublisher(base, CommentIntentExchange, CommentIntentQueue, CommentIntentRoutingKey)
	if err != nil {
		return nil, err
	}
	if err := DeclareModuleRetryTopologies(intentPublisher.ch, CommentIntentRetryModule, CommentIntentExchange, CommentIntentRoutingKey); err != nil {
		_ = intentPublisher.ch.Close()
		return nil, err
	}

	appliedPublisher, err := newConfirmedPublisher(base, CommentAppliedExchange, PopularityQueue, CommentAppliedRoutingKey)
	if err != nil {
		_ = intentPublisher.ch.Close()
		return nil, err
	}
	return &CommentMQ{intentPublisher: intentPublisher, appliedPublisher: appliedPublisher}, nil
}

func (m *CommentMQ) PublishIntent(ctx context.Context, intent CommentIntent) error {
	if m == nil {
		return errors.New("comment mq is not initialized")
	}
	return m.intentPublisher.publish(ctx, CommentIntentExchange, CommentIntentRoutingKey, intent)
}

func (m *CommentMQ) PublishApplied(ctx context.Context, applied CommentApplied) error {
	if m == nil {
		return errors.New("comment mq is not initialized")
	}
	return m.appliedPublisher.publish(ctx, CommentAppliedExchange, CommentAppliedRoutingKey, applied)
}

func (m *CommentMQ) PublishIntentPayloads(ctx context.Context, payloads [][]byte) []error {
	if m == nil {
		return batchPublisherNotInitialized(len(payloads), "comment mq")
	}
	return m.intentPublisher.publishRawBatch(ctx, CommentIntentExchange, CommentIntentRoutingKey, payloads)
}

func (m *CommentMQ) PublishAppliedPayloads(ctx context.Context, payloads [][]byte) []error {
	if m == nil {
		return batchPublisherNotInitialized(len(payloads), "comment mq")
	}
	return m.appliedPublisher.publishRawBatch(ctx, CommentAppliedExchange, CommentAppliedRoutingKey, payloads)
}

// CommentIntent 是 API 接收的评论操作；它持久化在 Outbox 后异步投递。
type CommentIntent struct {
	EventID    string    `json:"event_id"`
	Action     string    `json:"action"`
	CommentID  uint      `json:"comment_id,omitempty"`
	Username   string    `json:"username,omitempty"`
	ContentID  uint      `json:"content_id"`
	AuthorID   uint      `json:"author_id"`
	Content    string    `json:"content,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// CommentApplied 只代表已经真实写入或删除的评论，供热度和通知消费。
type CommentApplied struct {
	EventID          string    `json:"event_id"`
	IntentID         string    `json:"intent_id"`
	Action           string    `json:"action"`
	CommentID        uint      `json:"comment_id"`
	Username         string    `json:"username,omitempty"`
	ContentID        uint      `json:"content_id"`
	AuthorID         uint      `json:"author_id"`
	Content          string    `json:"content,omitempty"`
	PopularityChange int64     `json:"popularity_change"`
	OccurredAt       time.Time `json:"occurred_at"`
}
