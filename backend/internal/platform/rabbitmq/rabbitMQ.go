package rabbitmq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitMQ 只管理 Connection，Channel 由各组件按需创建
type RabbitMQ struct {
	Conn *amqp.Connection
	mu   sync.Mutex
}

// ✅
func NewRabbitMQ(cfg *config.RabbitMQConfig) (*RabbitMQ, error) {
	if cfg == nil {
		return nil, errors.New("rabbitmq config is nil")
	}
	//amqp://admin:password123@localhost:5672/   <- / 表示默认 vhost
	//        ↑          ↑           ↑    ↑
	//      用户名       密码        主机  端口
	url := "amqp://" + cfg.Username + ":" + cfg.Password + "@" + cfg.Host + ":" + strconv.Itoa(cfg.Port) + "/"
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	return &RabbitMQ{Conn: conn}, nil
}

// ✅
// 关闭mq连接
func (r *RabbitMQ) Close() error {
	if r == nil {
		return nil
	}
	if r.Conn != nil {
		return r.Conn.Close()
	}
	return nil
}

// NewChannel ✅
// 一个 Channel 一次只能被一个 goroutine 用,一个功能
func (r *RabbitMQ) NewChannel() (*amqp.Channel, error) {
	if r == nil || r.Conn == nil {
		return nil, errors.New("rabbitmq connection is not initialized")
	}
	return r.Conn.Channel()
}

// DeclareTopic 声明交换器,队列,绑定关系
func DeclareTopic(ch *amqp.Channel, exchange string, queue string, bindingKey string) error {
	if ch == nil {
		return errors.New("channel is not initialized")
	}
	if exchange == "" || queue == "" || bindingKey == "" {
		return errors.New("exchange/queue/bindingKey is required")
	}

	//声明交换器
	if err := ch.ExchangeDeclare(
		exchange, // 交换器名称
		"topic",  // 交换机类型：topic:按routing key模式匹配路由;Direct:直连 / 精准路由方式;Fanout:扇出 / 广播方式;Topic:主题 / 模糊匹配方式
		true,     // durable: 持久化，RabbitMQ重启后交换器还在
		false,    // autoDelete: 自动删除（有绑定的队列就不删）
		false,    // internal: 是否内部使用（客户端可用）
		false,    // noWait: 不等待服务器确认交换器声明成功与否
		nil,      // arguments: 额外参数
	); err != nil {
		return err
	}

	//声明队列
	q, err := ch.QueueDeclare(
		queue, // 队列名称
		true,  // durable: 持久化，RabbitMQ重启后队列还在
		false, // autoDelete: 自动删除
		false, // exclusive: 独占（其他消费者不可共享）,独属于某个connection
		false, // noWait: 不等待服务器确认队列声明成功与否
		amqp.Table{
			"x-dead-letter-exchange":    DLX,   //队列中消息成为死信后去往的死信交换机
			"x-dead-letter-routing-key": queue, //带着的routing key
		},
	)
	if err != nil {
		return err
	}

	//声明一条绑定关系
	if err := ch.QueueBind(
		q.Name,     // 队列名
		bindingKey, // routing key 匹配规则（如 "like.*" 匹配 "like.like"/"like.unlike"）
		exchange,   // 绑定到哪个交换器
		false,      // noWait
		nil,        // 额外参数
	); err != nil {
		return err
	}
	//声明死信队列和死信交换机
	if err := DeclareDLX(ch, queue); err != nil {
		return err
	}
	return nil
}

// DeclareModuleRetryTopologies
// 对一个模块声明一个重试交换机和三个延迟队列
// 后端发布
// --消息,routing_key-->重试交换机
// --路由-->延迟队列
// --过期,成为死信,带着targetRoutingKey --> targetExchange
// --路由--> 重新进入队列
func DeclareModuleRetryTopologies(
	ch *amqp.Channel,
	module string,
	targetExchange string,
	targetRoutingKey string,
) error {
	if module == "" || targetExchange == "" || targetRoutingKey == "" {
		return errors.New("module and target topology fields are required")
	}

	retryExchange := module + ".retry.events"
	topologies := []struct {
		queue      string
		routingKey string
		delay      time.Duration
	}{
		{module + ".retry.1s.queue", module + ".retry.1s", time.Second},
		{module + ".retry.10s.queue", module + ".retry.10s", 10 * time.Second},
		{module + ".retry.60s.queue", module + ".retry.60s", time.Minute},
	}

	for _, topology := range topologies {
		if err := DeclareRetryTopology(
			ch,
			retryExchange,       //video.timeline.retry.events
			topology.queue,      //video.timeline.retry.1s/2s/3s.queue
			topology.routingKey, //video.timeline.retry.1s/2s/3s
			topology.delay,      //time.Second/10s/60s
			targetExchange,      //video.timeline.events
			targetRoutingKey,    //video.timeline.publish
		); err != nil {
			return err
		}
	}
	return nil
}

// DeclareRetryTopology
// 声明重试交换机和延迟队列
// 重试交换机用来路由消息到重试队列
// 重试队列消息过期后会带上targetRoutingKey被targetExchange路由
func DeclareRetryTopology(
	ch *amqp.Channel,
	retryExchange string,
	retryQueue string,
	retryRoutingKey string,
	delay time.Duration,
	targetExchange string,
	targetRoutingKey string,
) error {
	if ch == nil {
		return errors.New("channel is not initialized")
	}
	if retryExchange == "" || retryQueue == "" || retryRoutingKey == "" || targetExchange == "" || targetRoutingKey == "" {
		return errors.New("retry and target topology fields are required")
	}
	if delay < time.Millisecond {
		return errors.New("retry delay must be at least one millisecond")
	}

	if err := ch.ExchangeDeclare(
		retryExchange,
		"direct",
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return err
	}

	q, err := ch.QueueDeclare(
		retryQueue,
		true,
		false,
		false,
		false,
		amqp.Table{
			"x-message-ttl":             delay.Milliseconds(),
			"x-dead-letter-exchange":    targetExchange,
			"x-dead-letter-routing-key": targetRoutingKey,
		},
	)
	if err != nil {
		return err
	}

	return ch.QueueBind(
		q.Name,
		retryRoutingKey,
		retryExchange,
		false,
		nil,
	)
}

// PublishJSON 配置条件,向RabbitMQ服务器发送消息,发给交换机分拣
func PublishJSON(ctx context.Context, ch *amqp.Channel, exchange string, routingKey string, payload any) error {
	if ch == nil {
		return errors.New("channel is not initialized")
	}
	if exchange == "" || routingKey == "" {
		return errors.New("exchange and routingKey are required")
	}
	//event := LikeEvent{
	//    EventID:    "evt_8f3a2c",
	//    Action:     "like",
	//    UserID:     10086,
	//    ContentID:    20260722,
	//    OccurredAt: time.Now(),
	//}
	//变成json
	//{"event_id":"evt_8f3a2c","action":"like","user_id":10086,"content_id":20260722,"occurred_at":"2026-07-22T18:30:00+08:00"}
	//b = [123 34 101 118 101 110 116 95 105 100 34 58 34 101 118 116 95 56 102 51 97 50 99 34 44 ...]
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	//用信道来发送
	return ch.PublishWithContext(
		ctx,
		exchange,   //交换机名字
		routingKey, //路由键
		false,      //true：当交换机根据 routingKey 找不到任何匹配的队列时，消息会被"退回"给生产者（通过 NotifyReturn 通道），而不是被静默丢弃
		false,      //设为false不用管
		amqp.Publishing{ //消息真正内容,上面只是用来路由到某个队列
			ContentType:  "application/json", //告诉消费者Body 里的字节应该按 JSON 格式解析
			DeliveryMode: amqp.Persistent,    //消息进入队列后，队列要不要把它写到磁盘上
			//Transient（1，瞬态，默认值）	消息只存内存，速度快；RabbitMQ 重启/崩溃后消息丢失
			//Persistent（2，持久化）	消息除了放内存，还会同步写一份到磁盘文件；RabbitMQ 重启后可以从磁盘把消息恢复回队列
			//*:前提是队列本身必须是持久化队列,否则重启后队列都消失了,更别提消息了
			Timestamp: time.Now(), //记录消息产生的时间，方便追踪/调试/排序
			Body:      b,          //payload转成的字节数组,消息真正内容
		})
}

// 生成2n位随机字符串
func newEventID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
