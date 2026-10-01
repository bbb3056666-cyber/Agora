package popularity

import (
	"context"
	"errors"
	"strconv"
	"time"

	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"

	goredis "github.com/redis/go-redis/v9"
)

const (
	popularityWindowTTL      = 2 * time.Hour
	popularityEventDedupeTTL = 2 * time.Hour
)

// UpdateCache 给同步降级路径使用：当前请求已经确认完成业务变更，无 MQ 重投语义，因此不需要 EventID。
func UpdateCache(ctx context.Context, cache *rediscache.Client, videoID uint, change int64) {
	if cache == nil || videoID == 0 || change == 0 {
		return
	}
	invalidateVideoCaches(cache, videoID)

	//将时间截断到分钟
	now := time.Now().UTC().Truncate(time.Minute)

	//v1:hot:video:1m:200601021504
	windowKey := cache.Key("hot:content:1m:%s", now.Format("200601021504"))

	//视频id转字符串,后面表示结果的进制strconv.FormatUint(15, 16) → "f"
	member := strconv.FormatUint(uint64(videoID), 10)

	opCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	//给windowKey对应的zset的member的分数加change
	_ = cache.ZincrBy(opCtx, windowKey, member, float64(change))
	_ = cache.Expire(opCtx, windowKey, popularityWindowTTL)
}

// 同一个 Lua 脚本内先 SET NX 事件去重 Key，再 ZINCRBY 热度。
// Redis 执行脚本时不会穿插其他命令，所以不会出现“已标记但未加分”或“先加分后崩溃重投又加一次”的中间状态。
var applyPopularityEventScript = goredis.NewScript(`
local inserted = redis.call("SET", KEYS[1], "1", "NX", "EX", ARGV[3])
if not inserted then return 0 end
redis.call("ZINCRBY", KEYS[2], ARGV[2], ARGV[1])
redis.call("EXPIRE", KEYS[2], ARGV[4])
return 1
`)

// ApplyEvent 给 MQ 消费路径使用，返回 true 表示本次事件首次生效。
// 去重 Key 保留两小时，和热度桶生命周期一致；重复消息在 TTL 内到达只会得到 false。
func ApplyEvent(ctx context.Context, cache *rediscache.Client, eventID string, videoID uint, change int64) (bool, error) {
	if cache == nil || videoID == 0 || change == 0 {
		return false, errors.New("invalid popularity event")
	}
	if eventID == "" {
		return false, errors.New("popularity event ID is required")
	}

	now := time.Now().UTC().Truncate(time.Minute)
	windowKey := cache.Key("hot:content:1m:%s", now.Format("200601021504"))
	dedupeKey := cache.Key("idem:popularity:%s", eventID)
	member := strconv.FormatUint(uint64(videoID), 10)

	opCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	applied, err := cache.RunScriptInt(
		opCtx,
		applyPopularityEventScript,
		[]string{dedupeKey, windowKey},
		member,
		change,
		int64(popularityEventDedupeTTL/time.Second),
		int64(popularityWindowTTL/time.Second),
	)
	if err != nil {
		return false, err
	}
	if applied == 1 {
		invalidateVideoCaches(cache, videoID)
		return true, nil
	}
	return false, nil
}

func invalidateVideoCaches(cache *rediscache.Client, videoID uint) {
	//删除原来存在redis中视频的detail(Content)
	//用于获取视频详细信息
	_ = cache.Del(context.Background(), cache.Key("content:detail:id=%d", videoID))
	//用于feed流批量获取视频详细信息
	_ = cache.Del(context.Background(), cache.Key("content:entity:%d", videoID))
}
