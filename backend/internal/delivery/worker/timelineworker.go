package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/social"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	oredis "github.com/redis/go-redis/v9"
)

const (
	// 粉丝数达到该阈值的作者不再 fan-out 写入每位粉丝的收件箱。
	// 但所有作者都会写自己的发件箱 feed:author:{authorID}，
	// 后续读取关注流时可针对大 V 从该发件箱拉取并合并。
	feedTimelineMaxSize = -1001
	// 普通作者的粉丝时间线按批写入，避免单个 Pipeline 堆积过多 Redis 命令。
	followerFanoutBatchSize = 500
	followerFanoutTimeout   = 5 * time.Second
	timelineRetryTimeout    = 5 * time.Second
)

// StartTimelineConsumer 从timelineMQ取消息
// 把新视频id和时间加入到:
// 全局时间线 zset
// 作者自己发件箱 zset
// 从redis/mysql获取粉丝数,可能更新redis
// 更新redis中作者的推拉状态
// 粉丝收件箱 zset(粉丝数<10_000)
func StartTimelineConsumer(queueName string, redisClient *rediscache.Client, rmq *rabbitmq.RabbitMQ, ss *social.SocialService) {
	if rmq == nil || rmq.Conn == nil {
		log.Printf("Timeline consumer disabled: rabbitmq is not initialized")
		return
	}
	if redisClient == nil {
		log.Printf("Timeline consumer disabled: redis is not initialized")
		return
	}

	go func() {
		//重连循环
		for {
			// 每次重连创建独立的 Channel，不与发布者共用,因为Channel不是线程安全的
			// 一个协程一个channel,一个channel一个用途
			consumerCh, err := rmq.NewChannel()
			if err != nil {
				log.Printf("Timeline consumer: 创建 Channel 失败: %v, 5秒后重试", err)
				time.Sleep(5 * time.Second)
				continue
			}

			//设置消费规则
			if err := consumerCh.Qos(
				//最多给这个消费者预取10条未确认的消息。
				//消费者手里最多积压10条没 Ack 的消息，向服务器发送ack才会再投递新的
				10,
				//预取消息的总大小上限（字节），0 表示不限制大小
				0,
				//是否对整个 Connection 下所有 Channel 生效
				false,
			); err != nil {
				log.Printf("Timeline consumer: QoS 设置失败: %v", err)
			}
			//注册消费者,返回装有包裹的只读管道
			deliveries, err := consumerCh.Consume(
				queueName, //消费的队列名
				"",        //消费者标签（名字），空字符串让 RabbitMQ 自动生成一个唯一名称
				false,     //手动确认模式。false = 消息投递后必须自己调用 Ack() 才算处理完；true = 消息一投递就自动确认，不用管处理结果
				false,     //是否允许多个消费者同时消费这个队列
				false,     //不管
				false,     //是否不等服务器返回注册成功才继续
				nil,       //额外参数
			)
			if err != nil {
				//关闭通道,五秒后重新建立
				log.Printf("Timeline consumer: 注册消费失败: %v, 5秒后重试", err)
				_ = consumerCh.Close()
				time.Sleep(5 * time.Second)
				continue
			}
			//消费者注册成功
			log.Printf("Timeline consumer 已启动, queue=%s", queueName)

			//获取重试消息Ch
			retryPublisherCh, err := rmq.NewChannel()
			if err != nil {
				log.Printf("Timeline consumer: 创建重试发布 Channel 失败: %v, 5秒后重试", err)
				_ = consumerCh.Close()
				time.Sleep(5 * time.Second)
				continue
			}
			//开启publisher confirm
			if err := retryPublisherCh.Confirm(false); err != nil {
				log.Printf("Timeline consumer: 开启重试发布确认失败: %v", err)
				_ = retryPublisherCh.Close()
				_ = consumerCh.Close()
				time.Sleep(5 * time.Second)
				continue
			}
			retryConfirms := retryPublisherCh.NotifyPublish(make(chan amqp.Confirmation, 16))

			//for-range遍历管道,只有管道关闭且消息读完才会退出
			for delivery := range deliveries {
				var event rabbitmq.TimelineEvent
				//获取字节切片
				body := delivery.Body
				if err := json.Unmarshal(body, &event); err != nil {
					log.Printf("Timeline consumer: 反序列化失败: %v", err)
					_ = delivery.Ack(false)
					continue
				}

				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				// 写入 Redis 全局最新时间线，失败则不确认消息，等待重新投递。
				if err := writeContentToGlobalTimeline(ctx, redisClient, event); err != nil {
					log.Printf("Timeline consumer: 写入全局时间线失败: %v", err)
					cancel()
					if !retryTimelineDelivery(retryPublisherCh, retryConfirms, delivery, err) {
						break
					}
					continue
				}
				// 每位作者都维护自己的发件箱；大 V 会由读侧从该 Key 拉取并合并。
				if err := writeContentToAuthorTimeline(ctx, redisClient, event); err != nil {
					log.Printf("Timeline consumer: 写入作者时间线失败: %v", err)
					cancel()
					if !retryTimelineDelivery(retryPublisherCh, retryConfirms, delivery, err) {
						break
					}
					continue
				}
				// 先通过 Redis / MySQL 的冗余 follower_count 判断采用推还是拉，
				// 不能先查完整粉丝列表：大 V 的粉丝列表可能非常大。
				followerCount, err := ss.GetFollowerCount(ctx, event.AuthorID)
				if err != nil {
					log.Printf("Timeline consumer: 查询粉丝数失败: %v", err)
					cancel()
					if !retryTimelineDelivery(retryPublisherCh, retryConfirms, delivery, err) {
						break
					}
					continue
				}
				// 发布侧也按当前计数兜底校正标记，修复此前缓存同步失败造成的陈旧状态。
				if err := ss.SyncPullAuthorMarker(ctx, event.AuthorID, followerCount); err != nil {
					log.Printf("Timeline consumer: 更新推拉模式标记失败: %v", err)
					cancel()
					if !retryTimelineDelivery(retryPublisherCh, retryConfirms, delivery, err) {
						break
					}
					continue
				}
				if followerCount >= social.PullModeFollowerThreshold {
					_ = delivery.Ack(false)
					cancel()
					continue
				}

				// 普通作者才执行分页 fan-out。超时覆盖整个多批次过程，而非单条 Redis 命令。
				fanoutCtx, fanoutCancel := context.WithTimeout(context.Background(), followerFanoutTimeout)
				err = fanoutContentToFollowers(fanoutCtx, redisClient, ss, event)
				fanoutCancel()
				if err != nil {
					log.Printf("Timeline consumer: 写入粉丝时间线失败: %v", err)
					cancel()
					if !retryTimelineDelivery(retryPublisherCh, retryConfirms, delivery, err) {
						break
					}
					continue
				}
				// 所有关键 Redis 写入成功 → Ack，消息从队列删除
				_ = delivery.Ack(false) //是否确认所有 delivery tag ≤ 当前这条的未确认消息（批量确认）
				cancel()
			}

			// 循环退出说明某个 AMQP Channel 断开，关闭并重连
			_ = retryPublisherCh.Close()
			_ = consumerCh.Close()
			log.Printf("Timeline consumer: Channel 断开, 5秒后重连...")
			time.Sleep(5 * time.Second)
		}
	}()
}

func retryTimelineDelivery(ch *amqp.Channel, confirms <-chan amqp.Confirmation, delivery amqp.Delivery, cause error) bool {
	retryCount := rabbitmq.GetRetryCount(delivery)
	if retryCount >= rabbitmq.MaxRetryCount {
		log.Printf("Timeline consumer: 重试次数耗尽，转入死信队列: retry_count=%d, err=%v", retryCount, cause)
		_ = delivery.Nack(false, false)
		return true
	}

	routingKey := rabbitmq.TimelineRetryModule
	switch retryCount {
	case 0:
		routingKey += ".retry.1s"
	case 1:
		routingKey += ".retry.10s"
	case 2:
		routingKey += ".retry.60s"
	}
	sequence := ch.GetNextPublishSeqNo()
	publishCtx, cancel := context.WithTimeout(context.Background(), timelineRetryTimeout)
	//发送至重试交换机,路由至延迟队列,时间耗尽重新带着routing key被事件交换机转发进入事件队列
	err := ch.PublishWithContext(
		publishCtx,
		rabbitmq.TimelineRetryExchange,
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
		err = waitTimelineRetryConfirm(publishCtx, confirms, sequence)
	}
	cancel()
	if err != nil {
		log.Printf("Timeline consumer: 投递延迟重试失败，原消息回队: retry_count=%d, err=%v", retryCount, err)
		_ = delivery.Nack(false, true)
		return false
	}

	//确保消息成功投递到延迟队列才ack原消息
	_ = delivery.Ack(false)
	return true
}

func waitTimelineRetryConfirm(ctx context.Context, confirms <-chan amqp.Confirmation, sequence uint64) error {
	for {
		select {
		case confirmation, ok := <-confirms:
			if !ok {
				return fmt.Errorf("timeline retry publisher confirm channel closed")
			}
			if confirmation.DeliveryTag != sequence {
				continue
			}
			if !confirmation.Ack {
				return fmt.Errorf("timeline retry publish negatively acknowledged: delivery_tag=%d", sequence)
			}
			return nil
		case <-ctx.Done():
			return fmt.Errorf("wait timeline retry publisher confirm: %w", ctx.Err())
		}
	}
}

// fanoutContentToFollowers 使用关系表主键游标分页：一批粉丝对应一次 Redis Pipeline。
// 若中途失败，消费者会 Nack 整条消息；已完成批次重复执行 ZADD 是幂等的。
func fanoutContentToFollowers(ctx context.Context, redisClient *rediscache.Client, ss *social.SocialService, event rabbitmq.TimelineEvent) error {
	var relationCursor uint
	for {
		followerIDs, nextCursor, err := ss.ListFollowerIDsAfterID(ctx, event.AuthorID, relationCursor, followerFanoutBatchSize)
		if err != nil {
			return err
		}
		if len(followerIDs) == 0 {
			return nil
		}

		pipe := redisClient.Pipeline()
		if pipe == nil {
			return fmt.Errorf("redis pipeline is not initialized")
		}
		for _, followerID := range followerIDs {
			key := redisClient.Key("feed:follower:%d", followerID)
			_ = pipe.ZAdd(ctx, key, oredis.Z{
				Member: fmt.Sprintf("%d", event.ContentID),
				Score:  float64(event.CreateTime),
			})
			_ = pipe.ZRemRangeByRank(ctx, key, 0, feedTimelineMaxSize)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}

		// 本批不足 limit 说明已到末尾；否则以最后一条关系 ID 请求下一页。
		if len(followerIDs) < followerFanoutBatchSize {
			return nil
		}
		relationCursor = nextCursor
	}
}

// writeContentToAuthorTimeline 写入作者发件箱，供大 V 的读侧拉模式合并。
func writeContentToAuthorTimeline(ctx context.Context, redisClient *rediscache.Client, event rabbitmq.TimelineEvent) error {
	authorTimelineKey := redisClient.Key("feed:author:%d", event.AuthorID)
	if err := redisClient.ZAdd(ctx, authorTimelineKey, oredis.Z{
		Member: fmt.Sprintf("%d", event.ContentID),
		Score:  float64(event.CreateTime),
	}); err != nil {
		return err
	}
	if err := redisClient.ZRemRangeByRank(ctx, authorTimelineKey, 0, feedTimelineMaxSize); err != nil {
		// 同样只影响容量控制，不影响已写入的新视频。
		log.Printf("Timeline consumer: 清理作者时间线失败: %v", err)
	}
	return nil
}

// writeContentToGlobalTimeline 写入全局最新视频时间线，供 ListLatest 读取。
func writeContentToGlobalTimeline(ctx context.Context, redisClient *rediscache.Client, event rabbitmq.TimelineEvent) error {
	timelineKey := redisClient.Key("feed:global_timeline")
	if err := redisClient.ZAdd(ctx, timelineKey, oredis.Z{
		Score:  float64(event.CreateTime),
		Member: fmt.Sprintf("%d", event.ContentID),
	}); err != nil {
		return err
	}
	if err := redisClient.ZRemRangeByRank(ctx, timelineKey, 0, feedTimelineMaxSize); err != nil {
		// 清理失败只会暂时多保留旧数据，不影响新视频可见性，因此记录日志后继续消费。
		log.Printf("Timeline consumer: 清理全局时间线失败: %v", err)
	}
	return nil
}
