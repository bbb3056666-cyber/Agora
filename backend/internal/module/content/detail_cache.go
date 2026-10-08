package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/bbb3056666-cyber/distcache"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"gorm.io/gorm"
)

const (
	contentDetailGroupName          = "content-detail"
	contentDetailLocalTTL           = 5 * time.Second
	contentDetailRedisTTL           = 5 * time.Minute
	contentDetailMaxBytes     int64 = 64 << 20
	contentDetailMaxLoads           = 64
)

// DetailCache 只暴露内容详情业务需要的读和失效操作，ContentService 不依赖具体缓存实现。
type DetailCache interface {
	Get(ctx context.Context, contentID string) ([]byte, error)
	Remove(contentID string) error
}

type distributedDetailCache struct {
	group *distcache.Group
}

// NewDistributedDetailCache 创建内容详情缓存组：
// L1 是 API 进程内的 distcache，L2 是 Redis，L3 是最终事实来源 MySQL。
// 本轮只迁移单 Key 的 GetDetail；Feed 的批量 MGET 三级缓存保持原样。
// TODO: Worker 直接更新 likes_count/popularity 时不会经过 ContentService。
// 正式多节点部署前，需要由 Worker 发布可靠失效事件，或使用只负责发送失效的轻量客户端，
// 让所有 API 删除 Redis 与 distcache 中的详情副本；Worker 本身不应加入缓存哈希环。
func NewDistributedDetailCache(node *distcache.Cache, repo *ContentRepository, redisCache *rediscache.Client) DetailCache {
	if node == nil || repo == nil {
		return nil
	}

	getter := distcache.GetterFunc(func(ctx context.Context, key string) ([]byte, error) {
		contentID, err := strconv.ParseUint(key, 10, 64)
		if err != nil || contentID == 0 {
			return nil, fmt.Errorf("invalid content detail cache key %q", key)
		}

		redisKey := ""
		if redisCache != nil {
			redisKey = redisCache.Key("content:detail:id=%d", contentID)
			redisCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			cached, cacheErr := redisCache.GetBytes(redisCtx, redisKey)
			cancel()

			if cacheErr == nil {
				// Redis 中只有能够反序列化且 ID 匹配的 JSON 才能进入 L1。
				var entity Content
				if json.Unmarshal(cached, &entity) == nil && uint64(entity.ID) == contentID {
					return cached, nil
				}
				// 缓存坏数据不阻断请求，删除后继续回源 MySQL。
				_ = redisCache.Del(context.Background(), redisKey)
			}
		}

		entity, err := repo.GetByID(ctx, uint(contentID))
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, distcache.ErrNotFound
			}
			return nil, err
		}

		payload, err := json.Marshal(entity)
		if err != nil {
			return nil, err
		}
		if redisCache != nil {
			setCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			_ = redisCache.SetBytes(setCtx, redisKey, payload, contentDetailRedisTTL)
			cancel()
		}
		return payload, nil
	})

	group := node.NewGroup(
		contentDetailGroupName,
		getter,
		distcache.WithTTL(contentDetailLocalTTL),
		distcache.WithTTLJitterRatio(0.2),
		distcache.WithMaxBytes(contentDetailMaxBytes),
		distcache.WithMaxConcurrentLoads(contentDetailMaxLoads),
	)
	return &distributedDetailCache{group: group}
}

func (c *distributedDetailCache) Get(ctx context.Context, contentID string) ([]byte, error) {
	if c == nil || c.group == nil {
		return nil, errors.New("content detail cache is not initialized")
	}
	view, err := c.group.Get(ctx, contentID)
	if err != nil {
		if errors.Is(err, distcache.ErrNotFound) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}
	return view.ByteSlice(), nil
}

func (c *distributedDetailCache) Remove(contentID string) error {
	if c == nil || c.group == nil {
		return nil
	}
	return c.group.Remove(contentID)
}
