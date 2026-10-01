package rabbitmq

/*
import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// 只有一个与mq服务器连接的信道,只供一个goroutine使用
type LikeMQ struct {
	ch *amqp.Channel
	mu sync.Mutex //零值可用,无需初始化
}

const (
	likeExchange   = "like.events" //交换机名
	likeQueue      = "like.events" //消息队列名
	likeBindingKey = "like.*"      //绑定关系,交换机拿到这个key要交给指定队列

	//事件,like(分类).unlike(具体事件)
	likeLikeRoutingKey   = "like.like"
	likeUnlikeRoutingKey = "like.unlike"

	//通知队列
	NotificationLikeQueue = "notification.like"

												  ----- like.events(queue)----LikeWorker
										 (like.*)/
		LikeEvent   ->  like.events(exchange)----
									   (like.like)\
												   ---- notification.like(queue)----like的notificationWorker

)

// LikeEvent 对象序列化后被投入消息队列里
type LikeEvent struct {
	EventID    string    `json:"event_id"`
	Action     string    `json:"action"`
	UserID     uint      `json:"user_id"`
	ContentID    uint      `json:"content_id"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ✅
// 声明交换器,队列,绑定,返回包含信道的LikeMQ
func NewLikeMQ(base *RabbitMQ) (*LikeMQ, error) {
	if base == nil {
		return nil, errors.New("rabbitmq base is nil")
	}
	ch, err := base.NewChannel()
	if err != nil {
		return nil, err
	}
	if err := DeclareTopic(ch, likeExchange, likeQueue, likeBindingKey); err != nil {
		_ = ch.Close()
		return nil, err
	}
	return &LikeMQ{ch: ch}, nil
}

// 点赞
func (l *LikeMQ) Like(ctx context.Context, userID, videoID uint) error {
	return l.publish(ctx, "like", likeLikeRoutingKey, userID, videoID)
}

// 取消点赞
func (l *LikeMQ) Unlike(ctx context.Context, userID, videoID uint) error {
	return l.publish(ctx, "unlike", likeUnlikeRoutingKey, userID, videoID)
}

// 校验,生成事件,发送消息
func (l *LikeMQ) publish(ctx context.Context, action, routingKey string, userID, videoID uint) error {
	if l == nil || l.ch == nil {
		return errors.New("like mq is not initialized")
	}
	if userID == 0 || videoID == 0 {
		return errors.New("userID and videoID are required")
	}
	//32位随机字符串作为事件id
	id, err := newEventID(16)
	if err != nil {
		return err
	}
	//生成点赞事件
	event := LikeEvent{
		EventID:    id,
		Action:     action,
		UserID:     userID,
		ContentID:    videoID,
		OccurredAt: time.Now(),
	}
	//保证同一时刻只有一个 goroutine 使用这个 Channel 发消息
	//避免 AMQP 帧交错、Channel 协议错误或连接异常
	l.mu.Lock()         // 加锁
	defer l.mu.Unlock() // 解锁
	//发送消息
	return PublishJSON(
		ctx,
		l.ch,
		likeExchange,
		routingKey,
		event,
	)
}
*/
