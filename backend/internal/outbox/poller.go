package outbox

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"

	"gorm.io/gorm"
)

const (
	publishTimeout = 5 * time.Second //批量发送最大时间
	pollBatchSize  = 100             //一次轮询取的消息数
	pollInterval   = time.Second     //轮询间隔
)

// StartPoller 事务发件箱模式
// 创建发件箱轮询器
// 每秒扫描通用 Outbox；Publisher Confirm 成功前绝不删除记录。
//
//	每秒扫描通用 Outbox；Publisher Confirm 成功前绝不删除记录。
func StartPoller(db *gorm.DB, timelineMQ *rabbitmq.TimelineMQ, likeMQ *rabbitmq.LikeMQ, commentMQ *rabbitmq.CommentMQ) {
	if db == nil {
		log.Printf("Outbox poller disabled: database is not initialized")
		return
	}

	go func() {
		//循环检查数据库未处理的消息
		for {
			//获取未投递到MQ，或投递失败，现在可以重新投递的消息
			var messages []Message
			err := db.Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", "pending", time.Now()).
				Order("create_time ASC").
				Limit(pollBatchSize).
				Find(&messages).Error

			if err != nil || len(messages) == 0 {
				//没有,等一段时间后再找
				time.Sleep(pollInterval)
				continue
			}
			publishOutboxBatch(db, messages, timelineMQ, likeMQ, commentMQ)
		}
	}()
}

// publishOutboxBatch 按事件类型分组。同一类事件使用同一个可靠 Publisher：
// 先连续发送一批，再按 DeliveryTag 对应 Confirm；确认成功的 Outbox 一次性删除。
func publishOutboxBatch(
	db *gorm.DB,
	messages []Message,
	timelineMQ *rabbitmq.TimelineMQ,
	likeMQ *rabbitmq.LikeMQ,
	commentMQ *rabbitmq.CommentMQ,
) {
	//给消息分类
	groups := make(map[string][]Message)
	for _, message := range messages {
		groups[message.EventType] = append(groups[message.EventType], message)
	}

	// 每类事件对应独立的 Publisher / AMQP Channel，可以在同一轮并发投递。
	// 等所有组结束才查下一批，避免尚未删除的 pending 消息被再次取出。
	var wg sync.WaitGroup
	for eventType, msgs := range groups {
		//一次循环单独处理一个组
		wg.Add(1)
		go func(eventType string, msgs []Message) {
			defer wg.Done()
			publishOutboxGroup(db, eventType, msgs, timelineMQ, likeMQ, commentMQ)
		}(eventType, msgs)
	}
	wg.Wait()
}

func publishOutboxGroup(
	db *gorm.DB,
	eventType string,
	msgs []Message,
	timelineMQ *rabbitmq.TimelineMQ,
	likeMQ *rabbitmq.LikeMQ,
	commentMQ *rabbitmq.CommentMQ,
) {
	payloads := make([][]byte, 0, len(msgs))
	for _, msg := range msgs {
		payloads = append(payloads, msg.Payload)
	}

	publishCtx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	var errs []error
	switch eventType {
	case "content_published":
		errs = publishTimelineBatch(publishCtx, payloads, timelineMQ)
	case "like_intent":
		errs = publishLikeIntentBatch(publishCtx, payloads, likeMQ)
	case "like_applied":
		errs = publishLikeAppliedBatch(publishCtx, payloads, likeMQ)
	case "comment_intent":
		errs = publishCommentIntentBatch(publishCtx, payloads, commentMQ)
	case "comment_applied":
		errs = publishCommentAppliedBatch(publishCtx, payloads, commentMQ)
	default:
		errs = batchPublishError(len(payloads), fmt.Errorf("unsupported outbox event type: %s", eventType))
	}

	successIDs := make([]uint, 0, len(msgs))
	for i, message := range msgs {
		if errs[i] == nil {
			//统计成功投递消息的id
			successIDs = append(successIDs, message.ID)
			continue
		}
		//投递失败的消息
		nextRetry := time.Now().Add(2 * time.Second)
		if err := db.Model(&Message{}).Where("id = ?", message.ID).Updates(map[string]any{
			"retry_count":   gorm.Expr("retry_count + 1"),
			"next_retry_at": nextRetry,
		}).Error; err != nil {
			log.Printf("更新 Outbox 重试状态失败: id=%d err=%v", message.ID, err)
		}
		log.Printf("投递MQ失败: outbox_id=%d type=%s err=%v", message.ID, eventType, errs[i])
	}

	if len(successIDs) == 0 {
		return
	}
	if err := db.Where("id IN ?", successIDs).Delete(&Message{}).Error; err != nil {
		log.Printf("批量删除已确认的 Outbox 消息失败: ids=%v err=%v", successIDs, err)
	}
}

func publishTimelineBatch(ctx context.Context, payloads [][]byte, timelineMQ *rabbitmq.TimelineMQ) []error {
	if timelineMQ == nil {
		return batchPublishError(len(payloads), context.Canceled)
	}
	return timelineMQ.PublishPayloads(ctx, payloads)
}

func publishLikeIntentBatch(ctx context.Context, payloads [][]byte, likeMQ *rabbitmq.LikeMQ) []error {
	if likeMQ == nil {
		return batchPublishError(len(payloads), context.Canceled)
	}
	return likeMQ.PublishIntentPayloads(ctx, payloads)
}

func publishLikeAppliedBatch(ctx context.Context, payloads [][]byte, likeMQ *rabbitmq.LikeMQ) []error {
	if likeMQ == nil {
		return batchPublishError(len(payloads), context.Canceled)
	}
	return likeMQ.PublishAppliedPayloads(ctx, payloads)
}

func publishCommentIntentBatch(ctx context.Context, payloads [][]byte, commentMQ *rabbitmq.CommentMQ) []error {
	if commentMQ == nil {
		return batchPublishError(len(payloads), context.Canceled)
	}
	return commentMQ.PublishIntentPayloads(ctx, payloads)
}

func publishCommentAppliedBatch(ctx context.Context, payloads [][]byte, commentMQ *rabbitmq.CommentMQ) []error {
	if commentMQ == nil {
		return batchPublishError(len(payloads), context.Canceled)
	}
	return commentMQ.PublishAppliedPayloads(ctx, payloads)
}

// 组件未初始化，将所有消息标记错误，重投
func batchPublishError(size int, err error) []error {
	errs := make([]error, 0, size)
	for len(errs) < size {
		errs = append(errs, err)
	}
	return errs
}
