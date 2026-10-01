package redis

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// SetBytes ✅
// 插入键值对,redis中值为string类型
// 直接传结构体给Set
// go-redis内部,fmt.Sprint(&Content{ID:15, Title:"hello"}) 输出的是 Go 的内存表示
// "&{15 hello}"存进redis中没有意义——没有字段名、没有 JSON 结构、无法反序列化回来
// 所以结构体需要自己先Marshal,先转成json字符串后再转成byte[],再传给redis
// go-redis 内部逻辑：
// switch v := value.(type) {
// case string:
//
//	// 直接当字符串交,反正还是当字节存进redis
//
// case []byte:
//
//	// 直接当字节交 ← SetBytes 传的 []byte 走这
//
// case int, int64, float64:
//
//	// fmt.Sprint(v) → "15"
//
// default:
//
//	   // fmt.Sprint(v) → 对结构体就是垃圾
//	}
func (c *Client) SetBytes(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client not initialized")
	}
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

// GetBytes ✅
// go-redis拿到redis给的字节先解释成字符串,再将字符串转换为字节切片
// 没有数据返回(nil,redis.Nil)
func (c *Client) GetBytes(ctx context.Context, key string) ([]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client not initialized")
	}
	return c.rdb.Get(ctx, key).Bytes()
}

// Del 删除一对key-val
func (c *Client) Del(ctx context.Context, key string) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client not initialized")
	}
	return c.rdb.Del(ctx, key).Err()
}

// 批量删除
func (c *Client) DelBatch(ctx context.Context, keys ...string) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client not initialized")
	}
	return c.rdb.Del(ctx, keys...).Err()
}

// 按模式删除所有匹配的key
func (c *Client) DelByPattern(ctx context.Context, pattern string) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	iter := c.rdb.Scan(ctx, 0, pattern, 0).Iterator()
	for iter.Next(ctx) {
		_ = c.rdb.Del(ctx, iter.Val())
	}
	return iter.Err()
}

// MGet 根据keys批量获取value,没有就是nil: ["value1", nil, "value3"]
// string是值类型,var a string = nil会报错,string零值是""
// nil 只能用于指针、切片、map、channel、函数、接口,是引用类型的零值,所以返回[]any
func (c *Client) MGet(cacheCtx context.Context, cacheKeys ...string) ([]interface{}, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client not initialized")
	}
	return c.rdb.MGet(cacheCtx, cacheKeys...).Result()
}

// HSet 写入 Hash 的多个 field/value。活动元数据这类同生命周期字段适合放在一个 Hash 中。
func (c *Client) HSet(ctx context.Context, key string, values ...any) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client not initialized")
	}
	return c.rdb.HSet(ctx, key, values...).Err()
}

// Publish 向 Redis Pub/Sub 频道广播瞬时消息，返回实际收到该消息的订阅者数量。
// Pub/Sub 不保存离线消息；需要可靠查询的业务数据仍应先写入 MySQL。
func (c *Client) Publish(ctx context.Context, channel string, message any) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client not initialized")
	}
	return c.rdb.Publish(ctx, channel, message).Result()
}

// Subscribe 建立 Redis Pub/Sub 订阅，并等待 Redis 返回订阅确认。
// Pub/Sub 使用独立连接，不会占用普通的缓存读写连接；调用方负责在不再使用时 Close。
func (c *Client) Subscribe(ctx context.Context, channels ...string) (*goredis.PubSub, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client not initialized")
	}
	if len(channels) == 0 {
		return nil, errors.New("at least one channel is required")
	}

	pubsub := c.rdb.Subscribe(ctx, channels...)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}
	return pubsub, nil
}

// 从set里查有没有这个人
func (c *Client) SIsMember(ctx context.Context, key string, member any) (bool, error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis client not initialized")
	}
	return c.rdb.SIsMember(ctx, key, member).Result()
}

// SAdd 将成员加入集合；重复加入同一成员不会产生重复数据。
func (c *Client) SAdd(ctx context.Context, key string, members ...any) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client not initialized")
	}
	return c.rdb.SAdd(ctx, key, members...).Err()
}

// SRem 从集合移除成员；成员不存在时 Redis 会忽略该操作。
func (c *Client) SRem(ctx context.Context, key string, members ...any) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client not initialized")
	}
	return c.rdb.SRem(ctx, key, members...).Err()
}

func (c *Client) SMIsMember(ctx context.Context, key string, members ...any) ([]bool, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client not initialized")
	}
	return c.rdb.SMIsMember(ctx, key, members...).Result()
}
