package rabbitmq

import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type SocialMQ struct {
	ch *amqp.Channel
	mu sync.Mutex
}

const (
	SocialExchange   = "social.events"
	SocialQueue      = "social.events"
	SocialRoutingKey = "social.*"

	SocialFollowRoutingKey   = "social.follow"
	SocialUnfollowRoutingKey = "social.unfollow"

	NotificationSocialQueue = "notification.social"
)

type SocialEvent struct {
	EventID    string    `json:"event_id"`
	Action     string    `json:"action"`
	FollowerID uint      `json:"follower_id"`
	VloggerID  uint      `json:"vlogger_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

func NewSocialMQ(base *RabbitMQ) (*SocialMQ, error) {
	if base == nil {
		return nil, errors.New("rabbitmq base is nil")
	}
	ch, err := base.NewChannel()
	if err != nil {
		return nil, err
	}
	if err := DeclareTopic(ch, SocialExchange, SocialQueue, SocialRoutingKey); err != nil {
		ch.Close()
		return nil, err
	}
	return &SocialMQ{ch: ch}, nil
}

func (s *SocialMQ) Follow(ctx context.Context, followerID, vloggerID uint) error {
	return s.publish(ctx, "follow", SocialFollowRoutingKey, followerID, vloggerID)
}

func (s *SocialMQ) UnFollow(ctx context.Context, followerID, vloggerID uint) error {
	return s.publish(ctx, "unfollow", SocialUnfollowRoutingKey, followerID, vloggerID)
}

func (s *SocialMQ) publish(ctx context.Context, action, routingKey string, followerID, vloggerID uint) error {
	if s == nil || s.ch == nil {
		return errors.New("social mq is not initialized")
	}
	if followerID == 0 || vloggerID == 0 {
		return errors.New("followerID and vloggerID are required")
	}
	id, err := newEventID(16)
	if err != nil {
		return err
	}
	//消息
	evt := SocialEvent{
		EventID:    id,
		Action:     action,
		FollowerID: followerID,
		VloggerID:  vloggerID,
		OccurredAt: time.Now().UTC(),
	}
	s.mu.Lock()         // 加锁
	defer s.mu.Unlock() // 解锁
	return PublishJSON(
		ctx,
		s.ch,
		SocialExchange,
		routingKey,
		evt)
}
