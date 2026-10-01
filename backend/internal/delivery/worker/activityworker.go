package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/activity"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ActivityWorker 将 Redis 预报名异步落到 MySQL，避免高并发请求竞争数据库库存行。
type ActivityWorker struct {
	ch      *amqp.Channel
	service *activity.ActivityService
	queue   string
}

func NewActivityWorker(ch *amqp.Channel, service *activity.ActivityService, queue string) *ActivityWorker {
	return &ActivityWorker{ch: ch, service: service, queue: queue}
}

func (w *ActivityWorker) Run(ctx context.Context) error {
	if w == nil || w.ch == nil || w.service == nil || w.queue == "" {
		return errors.New("activity worker is not initialized")
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
				return errors.New("activity deliveries channel closed")
			}
			w.handleDelivery(ctx, d)
		}
	}
}

func (w *ActivityWorker) handleDelivery(ctx context.Context, d amqp.Delivery) {
	const maxRetries = 3
	//从Body取出字节,反序列化,得到消息
	var event rabbitmq.ActivityRegistrationEvent
	if err := json.Unmarshal(d.Body, &event); err != nil || event.EventID == "" || event.ActivityID == 0 || event.AccountID == 0 {
		log.Printf("activity worker: discard invalid message: %v", err)
		_ = d.Ack(false)
		return
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		//尝试插入数据库
		if err := w.service.PersistRegistration(ctx, event.EventID, event.ActivityID, event.AccountID); err == nil {
			_ = d.Ack(false)
			return
		} else if attempt < maxRetries {
			wait := time.Duration(1<<uint(attempt)) * time.Second
			log.Printf("activity worker: retry in %v (%d/%d): %v", wait, attempt+1, maxRetries, err)
			//等待一段时间再发
			select {
			case <-ctx.Done():
				_ = d.Nack(false, true)
				return
			case <-time.After(wait):
			}
		} else {
			//到达最大尝试次数
			log.Printf("activity worker: failed after %d retries: %v", maxRetries, err)
			if releaseErr := w.service.FailRegistration(ctx, event.ActivityID, event.AccountID); releaseErr != nil {
				log.Printf("activity worker: release reservation failed: %v", releaseErr)
			}
			_ = d.Ack(false)
			return
		}
	}
}
