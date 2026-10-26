package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/like"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// LikeIntentWorker 只消费点赞意图并裁决最终点赞状态。
// 热度与通知由 like_applied 的下游消费者处理。
type LikeIntentWorker struct {
	ch    *amqp.Channel
	rmq   *rabbitmq.RabbitMQ
	likes *like.LikeRepository
	queue string
}

func NewLikeIntentWorker(ch *amqp.Channel, rmq *rabbitmq.RabbitMQ, likes *like.LikeRepository, queue string) *LikeIntentWorker {
	return &LikeIntentWorker{ch: ch, rmq: rmq, likes: likes, queue: queue}
}

func (w *LikeIntentWorker) Run(ctx context.Context) error {
	if w == nil || w.ch == nil || w.rmq == nil || w.likes == nil {
		return errors.New("like intent worker is not initialized")
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
	//新创建channel用来投递消息到延迟队列，不能复用原channel来retry，一个channel一个职责
	retryPublisherCh, err := w.rmq.NewChannel()
	if err != nil {
		return err
	}
	defer retryPublisherCh.Close()
	if err := retryPublisherCh.Confirm(false); err != nil {
		return err
	}
	retryConfirms := retryPublisherCh.NotifyPublish(make(chan amqp.Confirmation, 16))

	// 有界短窗口聚合，按视频分组后每组只更新一次计数行。
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("deliveries channel closed")
			}
			batch := []amqp.Delivery{d}
			timer := time.NewTimer(likeBatchWait)
		collect:
			for len(batch) < like.MaxLikeIntentBatchSize {
				select {
				case next, open := <-deliveries:
					if !open {
						break collect
					}
					batch = append(batch, next)
				case <-timer.C:
					break collect
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				}
			}
			timer.Stop()
			if !w.handleBatch(ctx, retryPublisherCh, retryConfirms, batch) {
				return errors.New("like retry publisher is unavailable")
			}
		}
	}
}

const likeRetryTimeout = 5 * time.Second
const likeBatchWait = 10 * time.Millisecond

// handleBatch 先按视频聚合；事务提交后才逐条 Ack，失败则保留原有可靠重试语义。
func (w *LikeIntentWorker) handleBatch(
	ctx context.Context,
	retryPublisherCh *amqp.Channel,
	retryConfirms <-chan amqp.Confirmation,
	batch []amqp.Delivery,
) bool {
	type item struct {
		delivery amqp.Delivery
		intent   rabbitmq.LikeIntent
	}
	groups := make(map[uint][]item)
	for _, d := range batch {
		var intent rabbitmq.LikeIntent
		if err := json.Unmarshal(d.Body, &intent); err != nil || intent.UserID == 0 || intent.ContentID == 0 {
			_ = d.Ack(false)
			continue
		}
		groups[intent.ContentID] = append(groups[intent.ContentID], item{delivery: d, intent: intent})
	}
	for _, group := range groups {
		if ctx.Err() != nil {
			return true
		}
		intents := make([]rabbitmq.LikeIntent, len(group))
		for i := range group {
			intents[i] = group[i].intent
		}
		err := w.likes.ApplyLikeIntents(ctx, intents)
		for _, entry := range group {
			if err != nil {
				if !retryLikeDelivery(retryPublisherCh, retryConfirms, entry.delivery, rabbitmq.LikeIntentRetryModuleForQueue(w.queue), err) {
					return false
				}
			} else {
				_ = entry.delivery.Ack(false)
			}
		}
	}
	return true
}

// retryLikeDelivery 按 1 秒、10 秒、60 秒转入延迟队列；超过次数则进入 DLQ。
// 只有延迟消息获得 Publisher Confirm 后，才确认原消息，避免重试过程中丢命令。
func retryLikeDelivery(
	ch *amqp.Channel,
	confirms <-chan amqp.Confirmation,
	delivery amqp.Delivery,
	module string,
	cause error,
) bool {
	retryCount := rabbitmq.GetRetryCount(delivery)
	if retryCount >= rabbitmq.MaxRetryCount {
		log.Printf("like intent worker: 重试耗尽，转入死信队列: retry_count=%d, err=%v", retryCount, cause)
		_ = delivery.Nack(false, false)
		return true
	}

	routingKey := module
	switch retryCount {
	case 0:
		routingKey += ".retry.1s"
	case 1:
		routingKey += ".retry.10s"
	case 2:
		routingKey += ".retry.60s"
	}

	sequence := ch.GetNextPublishSeqNo()
	publishCtx, cancel := context.WithTimeout(context.Background(), likeRetryTimeout)
	err := ch.PublishWithContext(
		publishCtx,
		module+".retry.events",
		routingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  delivery.ContentType,
			DeliveryMode: amqp.Persistent,
			Headers:      rabbitmq.WithRetryCount(delivery.Headers, retryCount+1),
			Timestamp:    time.Now(),
			Body:         delivery.Body,
		},
	)
	if err == nil {
		//阻塞等待publisher confirm
		err = waitLikeRetryConfirm(publishCtx, confirms, sequence)
	}
	cancel()
	if err != nil {
		log.Printf("like intent worker: 投递延迟重试失败，原消息回队: retry_count=%d, err=%v", retryCount, err)
		_ = delivery.Nack(false, true)
		return false
	}

	//投递成功，旧消息直接ack
	_ = delivery.Ack(false)
	return true
}

// waitLikeRetryConfirm 等待publisher confirm
func waitLikeRetryConfirm(ctx context.Context, confirms <-chan amqp.Confirmation, sequence uint64) error {
	for {
		select {
		case confirmation, ok := <-confirms:
			if !ok {
				return errors.New("like retry publisher confirm channel closed")
			}
			if confirmation.DeliveryTag != sequence {
				continue
			}
			if !confirmation.Ack {
				return errors.New("like retry publish negatively acknowledged")
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
