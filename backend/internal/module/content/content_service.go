package content

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"

	"gorm.io/gorm"
)

type ContentService struct {
	contentRepository *ContentRepository
	cache             *rediscache.Client
	cacheTTL          time.Duration
	popularityMQ      *rabbitmq.PopularityMQ
}

func NewContentService(repo *ContentRepository, cache *rediscache.Client, popularityMQ *rabbitmq.PopularityMQ) *ContentService {
	return &ContentService{contentRepository: repo, cache: cache, cacheTTL: 5 * time.Minute, popularityMQ: popularityMQ}
}

// ✅
func (vs *ContentService) Publish(ctx context.Context, content *Content) error {
	if content == nil {
		return errors.New("content is nil")
	}
	content.ContentType = strings.ToLower(strings.TrimSpace(content.ContentType))
	content.Title = strings.TrimSpace(content.Title)
	content.Description = strings.TrimSpace(content.Description)
	content.Body = strings.TrimSpace(content.Body)
	content.PlayURL = strings.TrimSpace(content.PlayURL)
	content.CoverURL = strings.TrimSpace(content.CoverURL)

	if content.Title == "" {
		return errors.New("title is required")
	}
	switch content.ContentType {
	case ContentTypeVideo:
		if content.PlayURL == "" || content.CoverURL == "" {
			return errors.New("video requires play_url and cover_url")
		}
	case ContentTypeImage:
		if len(content.Assets) == 0 || len(content.Assets) > 9 {
			return errors.New("image content requires 1 to 9 images")
		}
		for index := range content.Assets {
			content.Assets[index].AssetType = ContentTypeImage
			content.Assets[index].URL = strings.TrimSpace(content.Assets[index].URL)
			content.Assets[index].SortOrder = index
			if content.Assets[index].URL == "" {
				return errors.New("image url is required")
			}
		}
		if content.CoverURL == "" {
			content.CoverURL = content.Assets[0].URL
		}
	case ContentTypeArticle:
		if content.Body == "" {
			return errors.New("article body is required")
		}
	default:
		return errors.New("unsupported content type")
	}

	// 内容主体、图片资源、标签关系和 Outbox 在同一个事务提交。
	err := vs.contentRepository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("Assets").Create(content).Error; err != nil {
			return err
		}
		if len(content.Assets) > 0 {
			for index := range content.Assets {
				content.Assets[index].ContentID = content.ID
			}
			if err := tx.Create(&content.Assets).Error; err != nil {
				return err
			}
		}

		//outbox_msgs
		//写入 videos → 发 RabbitMQ 消息
		//就会有风险：视频已经写入数据库，但 RabbitMQ 恰好不可用，消息没发出去；这样视频不会进入 Redis Feed 时间线
		//插入outbox_msgs表,只要视频发布成功，对应的待发送事件就一定在数据库里
		//后台 Outbox 轮询器持续扫描 outbox_msgs 中的待处理事件，成功发送 RabbitMQ 的视频发布时间线消息后，再删除该记录
		eventID, err := outbox.NewEventID()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(rabbitmq.TimelineEvent{
			EventID:    eventID,
			ContentID:  content.ID,
			AuthorID:   content.AuthorID,
			CreateTime: content.CreateTime.UnixMilli(),
			OccurredAt: time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		msg := outbox.Message{
			EventType: "content_published",
			Payload:   payload,
			Status:    "pending",
		}
		//在同一个事务中,发MQ和视频入库同时成功或失败
		//INSERT INTO outbox_msgs (event_type, payload, ...)   → 一行
		if err := tx.Create(&msg).Error; err != nil {
			return err
		}

		//提取标签
		tags := ExtractTags(content.Title + " " + content.Description + " " + content.Body)
		for _, tagName := range tags {
			var tag Tag
			//tags
			//查得到就用，查不到就创建
			//INSERT INTO tags (name)
			if err := tx.Where("name = ?", tagName).FirstOrCreate(&tag, Tag{Name: tagName}).Error; err != nil {
				return err
			}

			//content_tags
			//插入视频与标签的关联
			//INSERT INTO content_tags (content_id, tag_id)
			if err := tx.Create(&ContentTag{ContentID: content.ID, TagID: tag.ID}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return err

}

// 删除视频
func (vs *ContentService) Delete(ctx context.Context, id uint, authorID uint) error {
	//获取视频id
	video, err := vs.contentRepository.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if video == nil {
		return errors.New("video not found")
	}
	if video.AuthorID != authorID {
		return apierror.ErrUnauthorized
	}
	if err := vs.contentRepository.DeleteContent(ctx, id); err != nil {
		return err
	}
	if vs.cache != nil {
		cacheKey := vs.cache.Key("content:detail:id=%d", id)
		_ = vs.cache.Del(context.Background(), cacheKey)
	}
	return nil
}

// ✅
func (vs *ContentService) ListByAuthorID(ctx context.Context, authorID uint) ([]Content, error) {
	videos, err := vs.contentRepository.ListByAuthorID(ctx, int64(authorID))
	if err != nil {
		return nil, err
	}
	return videos, nil
}

// ✅
// 根据id获取视频详细信息
// 缓存击穿:大批的请求redis缓存同时miss,之后同时去查数据库
func (vs *ContentService) GetDetail(ctx context.Context, id uint) (*Content, error) {
	//v1:video:detail:id=1
	cacheKey := vs.cache.Key("content:detail:id=%d", id)

	//函数功能:从redis提取视频详细信息,返回信息和是否提取到
	getCached := func() (*Content, error) {
		opCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()

		//从redis中获取视频详细信息字节
		b, err := vs.cache.GetBytes(opCtx, cacheKey)
		if err != nil {
			return nil, err
		}
		var cached Content
		//反序列化到cached变量中
		if err := json.Unmarshal(b, &cached); err != nil {
			return nil, err
		}
		return &cached, nil
	}

	//函数功能:将视频详细信息插入redis
	setCached := func(video *Content) {
		b, err := json.Marshal(video)
		if err != nil {
			return
		}
		opCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		_ = vs.cache.SetBytes(opCtx, cacheKey, b, vs.cacheTTL)
	}

	//缓存击穿:大批的请求redis缓存同时miss,之后同时去查数据库
	if vs.cache != nil {
		if v, err := getCached(); err == nil {
			return v, nil
		} else if rediscache.IsMiss(err) {
			//lock:v1:video:detail:id=1
			lockKey := "lock:" + cacheKey

			lockCtx, lockCancel := context.WithTimeout(ctx, 50*time.Millisecond)
			//给key加锁
			token, locked, lockErr := vs.cache.Lock(lockCtx, lockKey, 2*time.Second)
			lockCancel()

			//加锁成功
			if lockErr == nil && locked {
				defer func() { _ = vs.cache.Unlock(context.Background(), lockKey, token) }()
				//双重检验,其他请求可能早已拿到锁并重建完毕并释放锁
				//后拿到锁的(这些请求在IsMiss和加锁这两个时间点中间时缓存重建完毕)
				//只需提前查一下redis(微秒级)即可返回,无需查库(毫秒级)
				if v, err := getCached(); err == nil {
					return v, nil
				}

				//假如某个请求特别慢,锁过期,另一个协程拿到锁,两个请求同时到达这里
				//最坏情况：多一次 DB 查询 + 多一次缓存写

				//自己真的是第一个拿到锁的,去查数据库
				video, err := vs.contentRepository.GetByID(ctx, id)
				if err != nil {
					return nil, err
				}
				//写入redis缓存
				setCached(video)
				return video, nil
			}

			// 没拿到锁：等待别人回填缓存
			for i := 0; i < 5; i++ {
				select {
				case <-ctx.Done(): // 请求取消了（用户关页面/超时）
					return nil, ctx.Err() //→ 不等了，立即退出
				case <-time.After(20 * time.Millisecond): // ② 等 20ms
				}
				if v, err := getCached(); err == nil {
					return v, nil
				}
			}
		}
	}

	//没有redis/redis失效/循环5次都没得到结果,兜底
	video, err := vs.contentRepository.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	//redis存在但是五次都没拿到
	if vs.cache != nil {
		setCached(video)
	}
	return video, nil
}

func (vs *ContentService) UpdateLikesCount(ctx context.Context, id uint, likesCount int64) error {
	if err := vs.contentRepository.UpdateLikesCount(ctx, id, likesCount); err != nil {
		return err
	}
	return nil
}

func (vs *ContentService) UpdatePopularity(ctx context.Context, id uint, change int64) error {
	if err := vs.contentRepository.UpdatePopularity(ctx, id, change); err != nil {
		return err
	}

	if vs.popularityMQ != nil {
		if err := vs.popularityMQ.Update(ctx, id, change); err == nil {
			return nil
		}
	}

	if vs.cache != nil {
		// 1) 详情缓存：直接失效（最简单靠谱）
		_ = vs.cache.Del(context.Background(), vs.cache.Key("content:detail:id=%d", id))

		// 2) 热榜：写到“时间窗ZSET”，不要用 detail key
		now := time.Now().UTC().Truncate(time.Minute)
		windowKey := vs.cache.Key("hot:content:1m:%s", now.Format("200601021504"))
		member := strconv.FormatUint(uint64(id), 10)

		opCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()

		_ = vs.cache.ZincrBy(opCtx, windowKey, member, float64(change))
		_ = vs.cache.Expire(opCtx, windowKey, 2*time.Hour)
	}
	return nil
}
