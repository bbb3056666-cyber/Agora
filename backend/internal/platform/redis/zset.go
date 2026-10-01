package redis

import (
	"context"
	"errors"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// 给key对应的zset的member的分数加score
// key不存在则自动创建,key存在但不是zset也报错
func (c *Client) ZincrBy(ctx context.Context, key string, member string, score float64) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.ZIncrBy(ctx, key, score, member).Err()
}

// 向key对应的zset插入多个member和score
func (c *Client) ZAdd(ctx context.Context, key string, members ...redis.Z) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.ZAdd(ctx, key, members...).Err()
}

// ZRem 批量删除 ZSet 中指定的 member；不存在的 member 会被忽略。
func (c *Client) ZRem(ctx context.Context, key string, members ...string) error {
	if c == nil || c.rdb == nil || len(members) == 0 {
		return nil
	}
	args := make([]interface{}, len(members))
	for i, member := range members {
		args[i] = member
	}
	return c.rdb.ZRem(ctx, key, args...).Err()
}

// ZRemRangeByRank 删除zset中下标从start到stop的member
// 下标不存在,什么也不干,不报错
//保留最新的1000条视频ZRemRangeByRank(ctx,key,0,-1001)
/*
	ZSET里有 1234 个视频：
	 rank 0      ← 最老，删掉
	 rank 1      ← 删掉
	 ...
	 rank 232    ← 删掉  (1234-1000-1=233个被删掉)
	 rank 233    ← -1001，删掉
	 ──────────────
	 rank 234    ← -1000，保留
	 rank 235    ← 保留
	 ...
	 rank 1233   ← -1（最新），保留
	               共1000个保留
*/
func (c *Client) ZRemRangeByRank(ctx context.Context, key string, start int64, stop int64) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.ZRemRangeByRank(ctx, key, start, stop).Err()
}

// 按索引升序取member和score
// 索引 0: member1 (score 最低，最老)
// 索引 1: member2
// start,stop为索引范围
func (c *Client) ZRangeWithScores(ctx context.Context, key string, start int64, stop int64) ([]redis.Z, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client not initialized")
	}
	return c.rdb.ZRangeWithScores(ctx, key, start, stop).Result()
}

// 给key设置过期时间
func (c *Client) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// ZUnionStore 将多个 ZSET 合并到 dest。keys 和 weights 按下标一一对应：
// member 在每个桶中的 score 会先乘对应权重，再按 aggregate 合并。
// weights 传空切片时，Redis 默认每个桶权重均为 1。
func (c *Client) ZUnionStore(ctx context.Context, dest string, keys []string, weights []float64, aggregate string) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.ZUnionStore(ctx, dest, &redis.ZStore{
		Keys:      keys,
		Weights:   weights,
		Aggregate: aggregate,
	}).Err()
}

// 检查key存不存在
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, nil
	}
	//n为0或1
	n, err := c.rdb.Exists(ctx, key).Result()
	return n > 0, err
}

// 按索引降序取member
// 索引 0: member1 (score 最高，热度最高)
// 索引 1: member2
func (c *Client) ZRevRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	if c == nil || c.rdb == nil {
		return nil, nil
	}
	return c.rdb.ZRevRange(ctx, key, start, stop).Result()
}

// 降序返回指定分数范围内的记录,分数最高的排最前
func (c *Client) ZRevRangeByScore(ctx context.Context, key string, max, min string, offset, count int64) ([]string, error) {
	if c == nil || c.rdb == nil {
		return nil, nil
	}
	return c.rdb.ZRevRangeByScore(ctx, key, &redis.ZRangeBy{
		Max:    max,
		Min:    min,
		Offset: offset, //从开头起舍去几条
		Count:  count,  //取几条
	}).Result()
}
