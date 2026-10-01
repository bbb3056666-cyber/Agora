package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/social"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	goredis "github.com/redis/go-redis/v9"
)

type SocialWorker struct {
	ch    *amqp.Channel
	repo  *social.SocialRepository
	cache *redis.Client
	queue string
}

func NewSocialWorker(ch *amqp.Channel, repo *social.SocialRepository, cache *redis.Client, queue string) *SocialWorker {
	return &SocialWorker{ch: ch, repo: repo, cache: cache, queue: queue}
}

// 消费关注关系事件，同步维护粉丝收件箱；处理失败由 handleDelivery 在本进程内重试。
func (w *SocialWorker) Run(ctx context.Context) error {
	if w == nil || w.ch == nil || w.repo == nil {
		return errors.New("social worker is not initialized")
	}
	if w.queue == "" {
		return errors.New("queue is required")
	}

	//服务器往deliveries发送包裹
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
			//程序停止,直接返回
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				//管道异常关闭,连接断开,不是程序停止,则返回,5s后重新调用
				return errors.New("deliveries channel closed")
			}
			//处理包裹
			w.handleDelivery(ctx, d)
		}
	}
}

func (w *SocialWorker) handleDelivery(ctx context.Context, d amqp.Delivery) {
	const maxRetries = 3
	for i := 0; i <= maxRetries; i++ {
		select {
		case <-ctx.Done():
			//程序停止,包裹重新入队
			_ = d.Nack(false, true)
			return
		default:
		}

		if err := w.process(ctx, d.Body); err != nil {
			if i >= maxRetries {
				log.Printf("social worker: 重试 %d 次后仍失败, 丢弃: %v", maxRetries, err)
				_ = d.Ack(false)
				return
			}
			//等待几秒开始下一次处理
			wait := time.Duration(1<<uint(i)) * time.Second
			log.Printf("social worker: 处理失败, %v 后重试 (%d/%d): %v", wait, i+1, maxRetries, err)
			time.Sleep(wait)
			continue
		}
		//处理成功,向服务器ack,返回
		_ = d.Ack(false)
		return
	}
}

func (w *SocialWorker) process(ctx context.Context, body []byte) error {
	var evt rabbitmq.SocialEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		// 解析事件失败，直接丢弃
		return nil
	}
	if evt.FollowerID == 0 || evt.VloggerID == 0 {
		return nil
	}
	switch evt.Action {
	case "follow":
		// 将作者发件箱中的近期视频同步到新粉丝的收件箱。
		return w.addAuthorContentsToFollowerInbox(ctx, evt.FollowerID, evt.VloggerID)

	case "unfollow":
		// 从粉丝收件箱移除该作者仍保留在发件箱中的视频。
		return w.removeAuthorContentsFromFollowerInbox(ctx, evt.FollowerID, evt.VloggerID)
	default:
		return nil
	}
}

// authorTimelineContents 读取作者发件箱的全部保留视频（当前最多 1000 条）。
func (w *SocialWorker) authorTimelineContents(ctx context.Context, authorID uint) ([]goredis.Z, error) {
	if w.cache == nil {
		return nil, errors.New("redis cache is not initialized")
	}
	return w.cache.ZRangeWithScores(ctx, w.cache.Key("feed:author:%d", authorID), 0, -1)
}

// addAuthorContentsToFollowerInbox 在关注成功后补入作者近期视频。
func (w *SocialWorker) addAuthorContentsToFollowerInbox(ctx context.Context, followerID, authorID uint) error {
	videos, err := w.authorTimelineContents(ctx, authorID)
	if err != nil {
		return fmt.Errorf("read author timeline: %w", err)
	}
	if len(videos) == 0 {
		return nil
	}

	followerTimelineKey := w.cache.Key("feed:follower:%d", followerID)
	if err := w.cache.ZAdd(ctx, followerTimelineKey, videos...); err != nil {
		return fmt.Errorf("write follower timeline: %w", err)
	}
	if err := w.cache.ZRemRangeByRank(ctx, followerTimelineKey, 0, -1001); err != nil {
		return fmt.Errorf("trim follower timeline: %w", err)
	}
	return nil
}

// removeAuthorContentsFromFollowerInbox 在取关后移除作者近期视频。
func (w *SocialWorker) removeAuthorContentsFromFollowerInbox(ctx context.Context, followerID, authorID uint) error {
	videos, err := w.authorTimelineContents(ctx, authorID)
	if err != nil {
		return fmt.Errorf("read author timeline: %w", err)
	}
	if len(videos) == 0 {
		return nil
	}

	members := make([]string, 0, len(videos))
	for _, video := range videos {
		members = append(members, fmt.Sprint(video.Member))
	}
	if err := w.cache.ZRem(ctx, w.cache.Key("feed:follower:%d", followerID), members...); err != nil {
		return fmt.Errorf("remove author videos from follower timeline: %w", err)
	}
	return nil
}
