package rabbitmq

import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	ActivityExchange           = "activity.events"
	ActivityQueue              = "activity.registration.queue"
	ActivityRegisterRoutingKey = "activity.register"
	ActivitySuccessRoutingKey  = "activity.success"
	NotificationActivityQueue  = "notification.activity"
)

type ActivityMQ struct {
	ch *amqp.Channel
	mu sync.Mutex
}

func NewActivityMQ(base *RabbitMQ) (*ActivityMQ, error) {
	if base == nil {
		return nil, errors.New("rabbitmq base is nil")
	}
	ch, err := base.NewChannel()
	if err != nil {
		return nil, err
	}
	if err := DeclareTopic(ch, ActivityExchange, ActivityQueue, ActivityRegisterRoutingKey); err != nil {
		_ = ch.Close()
		return nil, err
	}
	return &ActivityMQ{ch: ch}, nil
}

// ActivityRegistrationEvent 是 Redis 预报名成功后投递的异步落库任务。
// EventID 用于追踪；MySQL 的活动+用户唯一索引负责消费幂等。
type ActivityRegistrationEvent struct {
	EventID    string    `json:"event_id"`
	ActivityID uint      `json:"activity_id"`
	AccountID  uint      `json:"account_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

// PublishRegistration 将报名消息发布到队列
func (m *ActivityMQ) PublishRegistration(ctx context.Context, activityID, accountID uint) error {
	if m == nil || m.ch == nil {
		return errors.New("activity mq is not initialized")
	}
	if activityID == 0 || accountID == 0 {
		return errors.New("activity_id and account_id are required")
	}
	id, err := newEventID(16)
	if err != nil {
		return err
	}
	event := ActivityRegistrationEvent{
		EventID:    id,
		ActivityID: activityID,
		AccountID:  accountID,
		OccurredAt: time.Now(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return PublishJSON(ctx, m.ch, ActivityExchange, ActivityRegisterRoutingKey, event)
}

func (m *ActivityMQ) PublishRegistrationSuccess(ctx context.Context, eventID string, activityID, accountID uint) error {
	if m == nil || m.ch == nil {
		return errors.New("activity mq is not initialized")
	}
	event := ActivityRegistrationEvent{
		EventID:    eventID,
		ActivityID: activityID,
		AccountID:  accountID,
		OccurredAt: time.Now(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return PublishJSON(ctx, m.ch, ActivityExchange, ActivitySuccessRoutingKey, event)
}
