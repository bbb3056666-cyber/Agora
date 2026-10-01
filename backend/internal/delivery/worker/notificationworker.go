package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
)

type Notification struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	RecipientID uint   `gorm:"index;not null" json:"recipient_id"`
	SenderID    uint   `gorm:"not null" json:"sender_id"`
	Type        string `gorm:"type:varchar(50);not null" json:"type"` //like（点赞）/ comment（评论）/ follow（关注）
	TargetID    uint   `json:"target_id"`                             //目标ID：点赞/评论是内容ID，关注是粉丝ID
	Content     string `gorm:"type:varchar(255)" json:"content"`      //通知文字内容，如"点赞了你的内容"、"关注了你"
	IsRead      bool   `gorm:"default:false" json:"is_read"`
	// SourceEventID 来自 RabbitMQ 事件；唯一索引使至少一次投递下的重复消费只落一条通知。
	// 指针对应 MySQL NULL，兼容迁移前的历史通知：MySQL 唯一索引允许多条 NULL。
	SourceEventID *string   `gorm:"type:varchar(64);uniqueIndex:ux_notifications_source_event_id" json:"source_event_id,omitempty"`
	CreatedAt     time.Time `gorm:"autoCreateTime" json:"created_at"`
}

type NotificationWorker struct {
	ch        *amqp.Channel
	db        *gorm.DB
	queue     string
	publisher NotificationPublisher
}

// NotificationPublisher 将已持久化的通知广播给 API 进程。
// Redis Client 满足该接口；接口让 Worker 不直接依赖具体 Redis 实现。
type NotificationPublisher interface {
	Publish(ctx context.Context, channel string, message any) (int64, error)
}

// NotificationRealtimeChannel 是 Worker 广播已写库通知、API 进程订阅并推送 SSE 的共享频道。
const NotificationRealtimeChannel = "notification.realtime"

func NewNotificationWorker(ch *amqp.Channel, db *gorm.DB, queue string, publisher NotificationPublisher) *NotificationWorker {
	return &NotificationWorker{
		ch:        ch,        //管道
		db:        db,        //数据库
		queue:     queue,     //取消息的队列
		publisher: publisher, //写库后用于跨进程广播
	}
}

// StartNotificationConsumers 为四类通知队列分别启动一个消费者。
// 每个消费者独占 RabbitMQ Channel：Channel 不是 goroutine-safe，且消费 QoS、连接错误和重连
// 都应彼此隔离。消费者不管理 SSE 连接，只在写库后发布跨进程通知。
func StartNotificationConsumers(ctx context.Context, rmq *rabbitmq.RabbitMQ, db *gorm.DB, publisher NotificationPublisher) {
	if rmq == nil || db == nil {
		log.Printf("Notification consumers disabled (MQ or DB not available)")
		return
	}

	for _, queueName := range []string{
		rabbitmq.NotificationLikeQueue,
		rabbitmq.NotificationCommentQueue,
		rabbitmq.NotificationSocialQueue,
		rabbitmq.NotificationActivityQueue,
	} {
		//每一类消息单开一个goroutine负责
		go func(queue string) {
			//重连循环
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				//建立连接
				ch, err := rmq.NewChannel()
				if err != nil {
					//建立失败5s后重建
					log.Printf("notification-%s: 创建 Channel 失败: %v, 5秒后重试", queue, err)
					time.Sleep(5 * time.Second)
					continue
				}

				log.Printf("NotificationWorker started, queue=%s", queue)
				//创建worker并开始工作
				if err := NewNotificationWorker(ch, db, queue, publisher).Run(ctx); err != nil && ctx.Err() == nil {
					log.Printf("notification-%s: %v, 5秒后重连...", queue, err)
				}
				_ = ch.Close()

				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}(queueName)
	}
}

// Run 从指定通知队列消费事件，持久化通知后发布 Redis 实时广播。
func (w *NotificationWorker) Run(ctx context.Context) error {
	if w == nil || w.ch == nil || w.db == nil {
		return errors.New("notification worker is not initialized")
	}
	if w.queue == "" {
		return errors.New("queue is required")
	}
	//设置
	_ = w.ch.Qos(100, 0, false)
	//得到包裹管道
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
			return ctx.Err() // 不会触发，Background永不取消
		case delivery, ok := <-deliveries:
			if !ok {
				//管道关闭,说明与mq断开,直接返回错误,5s后新建一个worker重新运行Run()
				return errors.New("deliveries channel closed")
			}
			//正常处理消息
			w.handleDelivery(ctx, delivery)
		}
	}
}

func (w *NotificationWorker) handleDelivery(ctx context.Context, d amqp.Delivery) {
	const maxRetries = 3
	for i := 0; i <= maxRetries; i++ {
		select {
		case <-ctx.Done():
			//特殊情况,上下文取消了,未处理好的包裹重新入队
			_ = d.Nack(false, true)
			return
		default:
		}
		if err := w.process(ctx, d); err != nil {
			//可能是数据库连接出了问题,可以重试
			if i >= maxRetries {
				log.Printf("notification worker: 重试 %d 次后仍失败, 丢弃: %v", maxRetries, err)
				//通知丢弃也没事
				_ = d.Ack(false)
				return
			}
			//i=0：1<<0 = 1 → 等1秒
			//i=1：1<<1 = 2 → 等2秒
			wait := time.Duration(1<<uint(i)) * time.Second
			log.Printf("notification worker: 处理失败, %v 后重试 (%d/%d): %v", wait, i+1, maxRetries, err)
			time.Sleep(wait)
			continue
		}
		//err=nil,消息发送成功/没必要发送,向服务器发送本条消息处理完毕
		_ = d.Ack(false)
		return
	}
}

func (w *NotificationWorker) process(ctx context.Context, d amqp.Delivery) error {
	body := d.Body
	if len(body) == 0 {
		return nil
	}
	routingKey := d.RoutingKey

	var (
		notif          *Notification
		sourceEventID  string
		commentApplied *rabbitmq.CommentApplied
	)

	switch {
	case routingKey == rabbitmq.LikeAppliedRoutingKey:
		var evt rabbitmq.LikeApplied
		//反序列化到evt中
		if err := json.Unmarshal(body, &evt); err != nil {
			//消息体本身就是坏的/格式不对,没必要重试,直接算处理完毕
			return nil
		}
		if evt.EventID == "" || evt.Action != "like" || evt.Change != 1 || evt.UserID == 0 || evt.ContentID == 0 {
			//消息无效
			return nil
		}
		// 根据统一内容 ID 获取作者。
		var authorID uint
		if err := w.db.WithContext(ctx).
			Table("contents").
			Where("id = ?", evt.ContentID).
			Select("author_id"). // 只SELECT author_id这一列（ID不用查）
			Scan(&authorID).     // 结果直接扫到uint变量authorID里
			Error; err != nil {
			//数据库临时连接不上,可以重试
			return err
		}
		if authorID == 0 || authorID == evt.UserID {
			// 内容不存在、已删除或作者自己互动时不生成通知。
			return nil
		}
		notif = &Notification{
			RecipientID: authorID,
			SenderID:    evt.UserID,
			Type:        "like",
			TargetID:    evt.ContentID,
			Content:     "点赞了你的内容",
			IsRead:      false,
		}
		sourceEventID = evt.EventID

	case routingKey == rabbitmq.CommentAppliedRoutingKey:
		var evt rabbitmq.CommentApplied
		if err := json.Unmarshal(body, &evt); err != nil {
			return nil
		}
		if evt.EventID == "" || evt.Action != "publish" || evt.AuthorID == 0 || evt.ContentID == 0 {
			return nil
		}
		commentApplied = &evt
		var authorID uint
		if err := w.db.WithContext(ctx).Table("contents").
			Where("id = ?", evt.ContentID).Select("author_id").
			Scan(&authorID).Error; err != nil {
			return err
		}
		if authorID == 0 || authorID == evt.AuthorID {
			return nil
		}
		notif = &Notification{
			RecipientID: authorID,
			SenderID:    evt.AuthorID,
			Type:        "comment",
			TargetID:    evt.ContentID,
			Content:     "评论了你的内容",
			IsRead:      false,
		}
		sourceEventID = evt.EventID

	case routingKey == "social.follow":
		var evt rabbitmq.SocialEvent
		if err := json.Unmarshal(body, &evt); err != nil {
			return nil
		}
		if evt.EventID == "" || evt.FollowerID == 0 || evt.VloggerID == 0 {
			return nil
		}
		notif = &Notification{
			RecipientID: evt.VloggerID,
			SenderID:    evt.FollowerID,
			Type:        "follow",
			TargetID:    evt.FollowerID,
			Content:     "关注了你",
			IsRead:      false,
		}
		sourceEventID = evt.EventID

	case routingKey == "activity.success":
		var evt rabbitmq.ActivityRegistrationEvent
		if err := json.Unmarshal(body, &evt); err != nil {
			return nil
		}
		if evt.EventID == "" || evt.ActivityID == 0 || evt.AccountID == 0 {
			return nil
		}
		notif = &Notification{
			RecipientID: evt.AccountID, // 报名成功的人
			SenderID:    0,             // 系统通知
			Type:        "activity_registration",
			TargetID:    evt.ActivityID,
			Content:     "你已成功报名该内容活动",
			IsRead:      false,
		}
		sourceEventID = evt.EventID
	}

	if notif != nil {
		notif.SourceEventID = &sourceEventID
		//向数据库插入一条记录,方便查询历史消息列表
		if err := w.db.WithContext(ctx).Create(notif).Error; err != nil {
			if !isDuplicateNotificationEvent(err) {
				return err
			}
		} else if w.publisher != nil {
			payload, err := json.Marshal(notif)
			if err != nil {
				return err
			}
			// MySQL 已是可靠事实来源；Pub/Sub 只负责在线实时提醒。
			if _, err := w.publisher.Publish(ctx, NotificationRealtimeChannel, payload); err != nil {
				log.Printf("notification worker: Redis 实时广播失败，通知已写入 MySQL: %v", err)
			}
		}
	}
	if commentApplied != nil {
		return w.createCommentMentionNotifications(ctx, *commentApplied)
	}
	return nil
}

var mentionPattern = regexp.MustCompile(`@(\w+)`)

// @ 通知也改由 CommentApplied 驱动；来源 ID 带接收者维度，重投不会重复插入。
func (w *NotificationWorker) createCommentMentionNotifications(ctx context.Context, evt rabbitmq.CommentApplied) error {
	seen := make(map[string]struct{})
	for _, match := range mentionPattern.FindAllStringSubmatch(evt.Content, -1) {
		username := strings.TrimSpace(match[1])
		if username == "" || username == evt.Username {
			continue
		}
		if _, ok := seen[username]; ok {
			continue
		}
		seen[username] = struct{}{}
		var recipientID uint
		if err := w.db.WithContext(ctx).Table("accounts").Where("username = ?", username).Select("id").Scan(&recipientID).Error; err != nil {
			return err
		}
		if recipientID == 0 || recipientID == evt.AuthorID {
			continue
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:mention:%d", evt.EventID, recipientID)))
		sourceID := fmt.Sprintf("%x", sum[:])
		notif := &Notification{
			RecipientID: recipientID, SenderID: evt.AuthorID, Type: "mention", TargetID: evt.ContentID,
			Content: evt.Username + " 在评论中提到了你", SourceEventID: &sourceID,
		}
		if err := w.db.WithContext(ctx).Create(notif).Error; err != nil {
			if isDuplicateNotificationEvent(err) {
				continue
			}
			return err
		}
		if w.publisher != nil {
			payload, err := json.Marshal(notif)
			if err != nil {
				return err
			}
			if _, err := w.publisher.Publish(ctx, NotificationRealtimeChannel, payload); err != nil {
				log.Printf("notification worker: Redis 实时广播失败，通知已写入 MySQL: %v", err)
			}
		}
	}
	return nil
}

func isDuplicateNotificationEvent(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
