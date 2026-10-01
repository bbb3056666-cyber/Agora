package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/comment"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"

	amqp "github.com/rabbitmq/amqp091-go"
)

// CommentWorker 只消费评论意图；真正完成后由 CommentApplied 事件驱动热度与通知。
type CommentWorker struct {
	ch       *amqp.Channel
	rmq      *rabbitmq.RabbitMQ
	comments *comment.CommentRepository
	queue    string
}

func NewCommentWorker(ch *amqp.Channel, rmq *rabbitmq.RabbitMQ, comments *comment.CommentRepository, queue string) *CommentWorker {
	return &CommentWorker{ch: ch, rmq: rmq, comments: comments, queue: queue}
}

func (w *CommentWorker) Run(ctx context.Context) error {
	if w == nil || w.ch == nil || w.rmq == nil || w.comments == nil {
		return errors.New("comment worker is not initialized")
	}
	deliveries, err := w.ch.Consume(w.queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	retryPublisherCh, err := w.rmq.NewChannel()
	if err != nil {
		return err
	}
	if err := retryPublisherCh.Confirm(false); err != nil {
		_ = retryPublisherCh.Close()
		return err
	}
	defer retryPublisherCh.Close()
	retryConfirms := retryPublisherCh.NotifyPublish(make(chan amqp.Confirmation, 16))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("deliveries channel closed")
			}
			if !w.handleDelivery(ctx, retryPublisherCh, retryConfirms, delivery) {
				return errors.New("comment retry publisher is unavailable")
			}
		}
	}
}

func (w *CommentWorker) handleDelivery(ctx context.Context, retryPublisherCh *amqp.Channel, retryConfirms <-chan amqp.Confirmation, delivery amqp.Delivery) bool {
	select {
	case <-ctx.Done():
		_ = delivery.Nack(false, true)
		return true
	default:
	}
	if err := w.process(ctx, delivery.Body); err != nil {
		return retryCommentDelivery(retryPublisherCh, retryConfirms, delivery, err)
	}
	_ = delivery.Ack(false)
	return true
}

func (w *CommentWorker) process(ctx context.Context, body []byte) error {
	var intent rabbitmq.CommentIntent
	if err := json.Unmarshal(body, &intent); err != nil {
		// 损坏消息无法修复，确认丢弃即可。
		return nil
	}
	if intent.EventID == "" || intent.ContentID == 0 || intent.AuthorID == 0 {
		return nil
	}
	return w.comments.ApplyIntent(ctx, intent)
}

// retryCommentDelivery 将失败意图可靠投递到 1 秒、10 秒、60 秒延迟队列；耗尽后进入 DLQ。
func retryCommentDelivery(ch *amqp.Channel, confirms <-chan amqp.Confirmation, delivery amqp.Delivery, cause error) bool {
	retryCount := rabbitmq.GetRetryCount(delivery)
	if retryCount >= rabbitmq.MaxRetryCount {
		log.Printf("comment worker: 重试耗尽，转入死信队列: retry_count=%d, err=%v", retryCount, cause)
		_ = delivery.Nack(false, false)
		return true
	}
	routingKey := rabbitmq.CommentIntentRetryModule
	switch retryCount {
	case 0:
		routingKey += ".retry.1s"
	case 1:
		routingKey += ".retry.10s"
	case 2:
		routingKey += ".retry.60s"
	}
	sequence := ch.GetNextPublishSeqNo()
	publishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := ch.PublishWithContext(publishCtx, rabbitmq.CommentIntentRetryExchange, routingKey, false, false, amqp.Publishing{
		ContentType:  delivery.ContentType,
		DeliveryMode: amqp.Persistent,
		Headers:      rabbitmq.WithRetryCount(delivery.Headers, retryCount+1),
		Timestamp:    time.Now(),
		Body:         delivery.Body,
	})
	if err == nil {
		err = waitCommentRetryConfirm(publishCtx, confirms, sequence)
	}
	cancel()
	if err != nil {
		log.Printf("comment worker: 投递延迟重试失败，原消息回队: retry_count=%d, err=%v", retryCount, err)
		_ = delivery.Nack(false, true)
		return false
	}
	_ = delivery.Ack(false)
	return true
}

func waitCommentRetryConfirm(ctx context.Context, confirms <-chan amqp.Confirmation, sequence uint64) error {
	for {
		select {
		case confirmation, ok := <-confirms:
			if !ok {
				return errors.New("comment retry publisher confirm channel closed")
			}
			if confirmation.DeliveryTag != sequence {
				continue
			}
			if !confirmation.Ack {
				return errors.New("comment retry publish negatively acknowledged")
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
