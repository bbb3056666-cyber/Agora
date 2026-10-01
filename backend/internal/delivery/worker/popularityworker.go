package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/popularity"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type PopularityWorker struct {
	ch    *amqp.Channel
	cache *rediscache.Client
	queue string
}

func NewPopularityWorker(ch *amqp.Channel, cache *rediscache.Client, queue string) *PopularityWorker {
	return &PopularityWorker{ch: ch, cache: cache, queue: queue}
}

func (w *PopularityWorker) Run(ctx context.Context) error {
	if w == nil || w.ch == nil || w.cache == nil {
		return errors.New("popularity worker is not initialized")
	}
	if w.queue == "" {
		return errors.New("queue is required")
	}

	deliveries, err := w.ch.Consume(
		w.queue,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("deliveries channel closed")
			}
			w.handleDelivery(ctx, d)
		}
	}
}

func (w *PopularityWorker) handleDelivery(ctx context.Context, d amqp.Delivery) {
	const maxRetries = 3
	for i := 0; i <= maxRetries; i++ {
		select {
		case <-ctx.Done():
			_ = d.Nack(false, true)
			return
		default:
		}
		if err := w.process(ctx, d); err != nil {
			if i >= maxRetries {
				log.Printf("popularity worker: 重试 %d 次后仍失败, 丢弃: %v", maxRetries, err)
				_ = d.Ack(false)
				return
			}
			wait := time.Duration(1<<uint(i)) * time.Second
			log.Printf("popularity worker: 处理失败, %v 后重试 (%d/%d): %v", wait, i+1, maxRetries, err)
			time.Sleep(wait)
			continue
		}
		_ = d.Ack(false)
		return
	}
}

func (w *PopularityWorker) process(ctx context.Context, delivery amqp.Delivery) error {
	var eventID string
	var videoID uint
	var change int64
	switch delivery.RoutingKey {
	case rabbitmq.LikeAppliedRoutingKey:
		var evt rabbitmq.LikeApplied
		if err := json.Unmarshal(delivery.Body, &evt); err != nil {
			return nil
		}
		eventID, videoID, change = evt.EventID, evt.ContentID, evt.PopularityChange
	case rabbitmq.CommentAppliedRoutingKey:
		var evt rabbitmq.CommentApplied
		if err := json.Unmarshal(delivery.Body, &evt); err != nil {
			return nil
		}
		eventID, videoID, change = evt.EventID, evt.ContentID, evt.PopularityChange
	default:
		return nil
	}
	if eventID == "" || videoID == 0 || change == 0 {
		return nil
	}
	// RabbitMQ 采用至少一次投递；同一条消息可能因 Ack 丢失被再次投递。
	// EventID 会在 Redis Lua 脚本中与热度增加一起原子去重。
	_, err := popularity.ApplyEvent(ctx, w.cache, eventID, videoID, change)
	return err
}
