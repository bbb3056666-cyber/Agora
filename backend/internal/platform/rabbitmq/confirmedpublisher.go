package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// confirmedPublisher 是复用的可靠发布器：投递后等待 publisher Confirm，
// 可批量投递等待
// HTTP 请求无需同步等待 RabbitMQ 回复。
type confirmedPublisher struct {
	ch       *amqp.Channel
	confirms <-chan amqp.Confirmation
	mu       sync.Mutex
}

func newConfirmedPublisher(base *RabbitMQ, exchange, queue, bindingKey string) (*confirmedPublisher, error) {
	if base == nil {
		return nil, errors.New("rabbitmq base is nil")
	}
	ch, err := base.NewChannel()
	if err != nil {
		return nil, err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, err
	}
	// 一个 Outbox 批次会连续发布多条消息；缓冲要能容纳一个批次的 Confirm，
	// 避免 Broker 已确认、发布协程却被无缓冲 channel 卡住。
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 256))
	if err := DeclareTopic(ch, exchange, queue, bindingKey); err != nil {
		_ = ch.Close()
		return nil, err
	}
	return &confirmedPublisher{ch: ch, confirms: confirms}, nil
}

// publish 可靠发布单条
// 单条发布也复用批量实现：批量大小为 1 时，可靠性语义完全一样。
func (p *confirmedPublisher) publish(ctx context.Context, exchange, routingKey string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return p.publishRawBatch(ctx, exchange, routingKey, [][]byte{body})[0]
}

// publishRawBatch 可靠发布多条
// 先连续发送同一类消息，再集中等待 Publisher Confirm。
// 返回值与 payloads 一一对应：确认成功为 nil；Nack、超时或发送失败保留错误，
// 由 Outbox 在下一轮重试。一个 Publisher 独占一个 AMQP Channel，整批加锁即可。
func (p *confirmedPublisher) publishRawBatch(ctx context.Context, exchange, routingKey string, payloads [][]byte) []error {
	routingKeys := make([]string, len(payloads))
	for i := range routingKeys {
		routingKeys[i] = routingKey
	}
	return p.publishRoutedBatch(ctx, exchange, routingKeys, payloads)
}

func (p *confirmedPublisher) publishRoutedBatch(ctx context.Context, exchange string, routingKeys []string, payloads [][]byte) []error {
	//每一条消息的结果
	results := make([]error, len(payloads))
	if p == nil || p.ch == nil {
		for i := range results {
			results[i] = errors.New("confirmed publisher is not initialized")
		}
		return results
	}
	if len(payloads) == 0 {
		return results
	}
	if exchange == "" || len(routingKeys) != len(payloads) {
		for i := range results {
			results[i] = errors.New("exchange and matching routing keys are required")
		}
		return results
	}
	for _, key := range routingKeys {
		if key == "" {
			for i := range results {
				results[i] = errors.New("routing key is required")
			}
			return results
		}
	}

	//加锁保证同一时刻只有一个 goroutine 使用这个 Channel 发消息
	//避免 AMQP 帧交错、Channel 协议错误或连接异常
	p.mu.Lock()
	defer p.mu.Unlock()

	// pending 记录“RabbitMQ 发布序号 -> 原始 payload 下标”。
	// Confirm 返回的只有 DeliveryTag（也就是发布序号），靠这张表才能知道
	// 当前 ACK / Nack 对应 Outbox 批次中的哪一条记录。
	pending := make(map[uint64]int, len(payloads))
	for index, payload := range payloads {
		// 必须在 Publish 前取序号：本次 Publish 成功后，Broker 就会用它回 Confirm。
		sequence := p.ch.GetNextPublishSeqNo()

		// 这里故意不等待 Confirm。循环会先把整批消息写入 RabbitMQ，
		// 等所有 Publish 调用完成后，下面再统一接收 Confirm。
		err := p.ch.PublishWithContext(
			ctx,
			exchange,
			routingKeys[index],
			false,
			false, amqp.Publishing{
				ContentType:  "application/json",
				DeliveryMode: amqp.Persistent,
				Timestamp:    time.Now(),
				Body:         payload,
			})
		if err != nil {
			// 当前这条没有写进 Channel，后续消息也没有尝试发送；二者都交给
			// Outbox 重试。此前已经成功写入 Channel 的消息仍要继续等 Confirm。
			results[index] = err
			for remaining := index + 1; remaining < len(results); remaining++ {
				results[remaining] = fmt.Errorf("publish skipped after previous failure: %w", err)
			}
			break
		}
		//记录序列号对应的消息下标
		pending[sequence] = index
	}

	// 不按发送顺序假设 Confirm，而是按 DeliveryTag 精确匹配。
	// 这样即使 Confirm 到达顺序出现变化，也不会误删别的 Outbox 记录。
	for len(pending) > 0 {
		select {
		case confirmation, ok := <-p.confirms:
			if !ok {
				//1.通道关闭，剩余未确认消息不可能再得到确认
				for sequence, index := range pending {
					results[index] = fmt.Errorf("publisher confirm channel closed: delivery_tag=%d", sequence)
				}
				return results
			}
			index, found := pending[confirmation.DeliveryTag]
			if !found {
				//2.上一批超时后迟到的 Confirm，不属于当前批次。
				continue
			}

			//本条消息已获得确认，删除这条消息的映射
			delete(pending, confirmation.DeliveryTag)
			//3.broker nack，记录下错误，重发
			if !confirmation.Ack {
				results[index] = fmt.Errorf("publisher negatively acknowledged: delivery_tag=%d", confirmation.DeliveryTag)
			}
		case <-ctx.Done(): //到时间了
			// 已经发出但还没确认的消息不能删除：它们可能已经到达 Broker，
			// 也可能还没到。保留 Outbox 并重试，消费者靠业务幂等处理重复投递。
			for sequence, index := range pending {
				results[index] = fmt.Errorf("wait publisher confirm: delivery_tag=%d: %w", sequence, ctx.Err())
			}
			return results
		}
	}
	return results
}

func batchPublisherNotInitialized(size int, name string) []error {
	results := make([]error, size)
	for i := range results {
		results[i] = fmt.Errorf("%s is not initialized", name)
	}
	return results
}
