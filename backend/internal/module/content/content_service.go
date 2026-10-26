package content

import (
	"context"
	"encoding/json"
	"errors"
	"log"
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
	detailCache       DetailCache
}

func NewContentService(repo *ContentRepository, cache *rediscache.Client, popularityMQ *rabbitmq.PopularityMQ, detailCache DetailCache) *ContentService {
	return &ContentService{
		contentRepository: repo,
		cache:             cache,
		cacheTTL:          5 * time.Minute,
		popularityMQ:      popularityMQ,
		detailCache:       detailCache,
	}
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
	content.LikesReconcileEnabled = content.LikesCount == 0
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
	// MySQL 已提交后先删除 L2 Redis，再广播删除各 API 节点的 L1，
	// 避免 L1 刚失效就从尚未删除的 Redis 重新装入旧值。
	vs.invalidateDetailCache(id)
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
// 根据 ID 获取内容详情。
// distcache Group.Get 负责 L1 命中、跨 API peer read、singleflight、负缓存和回源并发限制；
// LocalGetter 内部继续使用 Redis 作为 L2、MySQL 作为 L3。
func (vs *ContentService) GetDetail(ctx context.Context, id uint) (*Content, error) {
	if id == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if vs.detailCache == nil {
		return vs.contentRepository.GetByID(ctx, id)
	}

	payload, err := vs.detailCache.Get(ctx, strconv.FormatUint(uint64(id), 10))
	if err != nil {
		return nil, err
	}
	var entity Content
	if err := json.Unmarshal(payload, &entity); err != nil {
		return nil, err
	}
	return &entity, nil
}

func (vs *ContentService) UpdateLikesCount(ctx context.Context, id uint, likesCount int64) error {
	if err := vs.contentRepository.UpdateLikesCount(ctx, id, likesCount); err != nil {
		return err
	}
	vs.invalidateDetailCache(id)
	return nil
}

func (vs *ContentService) UpdatePopularity(ctx context.Context, id uint, change int64) error {
	if err := vs.contentRepository.UpdatePopularity(ctx, id, change); err != nil {
		return err
	}
	vs.invalidateDetailCache(id)

	if vs.popularityMQ != nil {
		if err := vs.popularityMQ.Update(ctx, id, change); err == nil {
			return nil
		}
	}

	if vs.cache != nil {
		// 热榜：写到“时间窗ZSET”，不要用 detail key
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

func (vs *ContentService) invalidateDetailCache(id uint) {
	if id == 0 {
		return
	}
	// 先删 L2，防止下一次 L1 miss 从 Redis 重新读回旧内容。
	if vs.cache != nil {
		if err := vs.cache.Del(context.Background(), vs.cache.Key("content:detail:id=%d", id)); err != nil {
			log.Printf("删除内容详情 Redis 缓存失败: contentID=%d, err=%v", id, err)
		}
	}
	// Remove 会删除当前 API 的 L1，并通过 distcache 向其他 API 广播失效。
	if vs.detailCache != nil {
		if err := vs.detailCache.Remove(strconv.FormatUint(uint64(id), 10)); err != nil {
			log.Printf("广播内容详情 L1 失效失败: contentID=%d, err=%v", id, err)
		}
	}
}
