package rabbitmq

import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type PopularityMQ struct {
	ch *amqp.Channel
	mu sync.Mutex
}

const (
	PopularityExchange   = "popularity.events"
	PopularityQueue      = "popularity.events"
	PopularityRoutingKey = "popularity.*"

	PopularityUpdateRoutingKey = "popularity.update"
)

type PopularityEvent struct {
	EventID    string    `json:"event_id"`
	ContentID  uint      `json:"content_id"`
	Change     int64     `json:"change"`
	OccurredAt time.Time `json:"occurred_at"`
}

// ✅
// 用一个mq连接句柄创建一个信道
// 声明交换机,队列,绑定关系
// 返回一个包装信道的对象PopularityMQ
func NewPopularityMQ(base *RabbitMQ) (*PopularityMQ, error) {
	if base == nil {
		return nil, errors.New("rabbitmq base is nil")
	}
	//获取一个Channel
	ch, err := base.NewChannel()
	if err != nil {
		return nil, err
	}
	//video.popularity.events  video.popularity.events  video.popularity.*
	if err := DeclareTopic(ch, PopularityExchange, PopularityQueue, PopularityRoutingKey); err != nil {
		_ = ch.Close()
		return nil, err
	}
	return &PopularityMQ{ch: ch}, nil
}

// 向消息队列发送更新事件热度的消息
func (p *PopularityMQ) Update(ctx context.Context, videoID uint, change int64) error {
	if p == nil || p.ch == nil {
		return errors.New("popularity mq is not initialized")
	}
	if videoID == 0 || change == 0 {
		return errors.New("videoID and change are required")
	}
	//32位事件随机字符串
	id, err := newEventID(16)
	if err != nil {
		return err
	}
	event := PopularityEvent{
		EventID:    id,
		ContentID:  videoID,
		Change:     change,
		OccurredAt: time.Now().UTC(),
	}
	p.mu.Lock()         // 加锁
	defer p.mu.Unlock() // 解锁
	//发消息,交换机,路由键,内容
	return PublishJSON(
		ctx,
		p.ch,
		PopularityExchange,
		PopularityUpdateRoutingKey,
		event,
	)
}
