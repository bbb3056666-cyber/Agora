package rabbitmq

import (
	"log"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	// DLX Dead Letter Exchange
	DLX           = "dlx.events"
	DLQ           = ".dlq"
	MaxRetryCount = 3
	RetryCountKey = "x-retry-count"
)

// DeclareDLX 声明死信交换机和对应的死信队列
func DeclareDLX(ch *amqp.Channel, queueName string) error {
	if ch == nil {
		return nil
	}
	//同名同参数重复声明直接成功，不做任何事
	if err := ch.ExchangeDeclare(
		DLX,      //dlx.events
		"direct", //精确路由
		true,
		false,
		false,
		false,
		nil,
	); err != nil {
		return err
	}
	//死信队列名
	DLQName := queueName + DLQ
	_, err := ch.QueueDeclare(
		DLQName, //like.events.dlq
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return err
	}
	//增加一条绑定规则
	if err := ch.QueueBind(
		DLQName,   // 死信队列: 	    like.events.dlq
		queueName, // routing key:  like.events
		DLX,       // 死信交换机:	dlx.events
		false,
		nil,
	); err != nil {
		return err
	}
	log.Printf("DLX ready: exchange=%s queue=%s", DLX, DLQName)
	return nil
}

func GetRetryCount(d amqp.Delivery) int {
	if d.Headers == nil {
		return 0
	}
	//switch type
	switch count := d.Headers[RetryCountKey].(type) {
	case int32:
		return int(count)
	case any:
		return 0
	default:
		return 0
	}
}

func WithRetryCount(headers amqp.Table, retryCount int) amqp.Table {
	cloned := make(amqp.Table, len(headers)+1)
	for key, value := range headers {
		cloned[key] = value
	}
	cloned[RetryCountKey] = int32(retryCount)
	return cloned
}
