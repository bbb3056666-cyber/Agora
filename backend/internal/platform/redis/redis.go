package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type Client struct {
	rdb       *redis.Client //redis连接句柄
	keyPrefix string        //key前缀
}

const defaultKeyPrefix = "v1:"

// ✅
// 创建新Client
func NewClient(rdb *redis.Client, keyPrefix string) *Client {
	return &Client{rdb: rdb, keyPrefix: keyPrefix}
}

// ✅
// 解析配置,返回薄封装对象,包含redis连接句柄和key前缀:v1代表版本1
func NewFromCfg(cfg *config.RedisConfig) (*Client, error) {
	//创建redis连接句柄
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Host + ":" + strconv.Itoa(cfg.Port),
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	return &Client{rdb: rdb, keyPrefix: defaultKeyPrefix}, nil
}

// ✅
// 关闭redis连接
func (c *Client) Close() error {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Close()
}

// ✅
// 测试redis连接
func (c *Client) Ping(ctx context.Context) error {
	if c == nil || c.rdb == nil {
		return errors.New("redis client not initialized")
	}
	return c.rdb.Ping(ctx).Err()
}

// IsMiss 判断是否有这个key
// type A string    // 全新类型，和 string 不同
/*
type NotFoundError string

func (e NotFoundError) Error() string { return string(e) }

const UserNotFound NotFoundError = "user not found"
const OrderNotFound NotFoundError = "order not found"
*/
// type B = string  // 别名，就是 string 本身
/*
匿名字段最常见的用途不是嵌入基础类型，
而是嵌入另一个 struct，实现类似继承的效果
type Animal struct {
    Name string
    Age  int
}

func (a Animal) Eat() { fmt.Println("eating...") }

type Dog struct {
    Animal    // ← 匿名嵌入 Animal，没有字段名
    Breed string
}

d := Dog{Animal: Animal{Name: "旺财", Age: 3}, Breed: "金毛"}

// 直接访问，不用 d.Animal.Name
fmt.Println(d.Name)    // 旺财 （提升了）
// 直接调用方法，不用 d.Animal.Eat()
d.Eat()                // eating...   （方法提升）

// 但底层还是组合，不是继承
fmt.Println(d.Animal.Name) // 也可以这样访问，完整路径

 命名嵌入
type Dog struct {
    A Animal       // 字段名是 A
    Breed string
}
d := Dog{A: Animal{Name: "旺财"}}
fmt.Println(d.A.Name)  // 必须写 d.A.Name，不会提升
d.A.Eat()              // 必须写 d.A.Eat()
*/
func IsMiss(err error) bool {
	return errors.Is(err, redis.Nil)
}

// ✅
// 拼 key 的工具函数，防止手写前缀出问题
func (c *Client) Key(format string, args ...any) string {
	prefix := ""
	if c != nil {
		prefix = c.keyPrefix
	}
	return prefix + fmt.Sprintf(format, args...)
}

// ✅
// 生成2n长度随机字符串
func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil { //填入随机字节
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Lock ✅
// 给hot_key lock:v1:video:detail:id=1 加锁
// 给lock:v1:feed:listByFollowing:limit=%d:accountID=%d:before=%d加锁
// 设置锁过期时间为ttl,防止持锁的人挂掉造成死锁
// 返回32位随机字符串和是否加锁成功
func (c *Client) Lock(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error) {
	if c == nil || c.rdb == nil {
		return "", false, nil
	}
	token, err = randToken(16)
	if err != nil {
		return "", false, err
	}
	//key不存在则写入并设置过期时间,防止进程崩了锁不会被释放,存在则返回false
	//lock:v1:video:detail:id=1 = a1b2c3...
	ok, err = c.rdb.SetNX(ctx, key, token, ttl).Result()
	return token, ok, err
}

// 删除hot_key,只能删除自己的,不能删除别人设置的
var unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
else
  return 0
end
`)

// 解hot_key的锁,用lua脚本
// 防止判断完是我的锁后立马过期,别人拿到锁,我把别人的锁删掉
func (c *Client) Unlock(ctx context.Context, key string, token string) error {
	if c == nil || c.rdb == nil {
		return nil
	}
	_, err := unlockScript.Run(ctx, c.rdb, []string{key}, token).Result()
	return err
}

//Lua 脚本必须返回一个值，Redis 拿到后转给客户端。能返回：
//
//return 0                      -- 数字    → Go: .Int64() 拿到 0
//return "hello"                -- 字符串  → Go: .Text() 拿到 "hello"
//return {1, 2, 3}              -- 数组    → Go: .Int64Slice() 拿到 [1,2,3]
//return {ok = true, val = 10}  -- 表      → Go: 当成 map 解析
//return nil                    -- nil    → Go: redis.Nil

// *Script类型
// Lua脚本,分开执行会出现并发问题:
// 两个请求都检查到key不存在,同时加一,结果为1而不是2
var incrementWithExpireScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1]) --INCR feed_system:ratelimit:account_login:1.2.3.4
if count == 1 then 						  --如果首次创建,则给key设置过期时间
  redis.call("PEXPIRE", KEYS[1], ARGV[1]) --redis内:PEXPIRE feed_system:ratelimit:account_login:1.2.3.4 60000,PEXPIRE单位是ms
end
return count
`)

// IncrementWithExpire ✅
// 用lua脚本确保+1和设置过期是原子的
// 返回count+1后的结果
func (c *Client) IncrementWithExpire(ctx context.Context, key string, expire time.Duration) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, nil
	}
	return incrementWithExpireScript.Run(
		ctx,
		c.rdb,
		[]string{key},         //key的参数:KEYS[1]
		expire.Milliseconds(), //其他参数:ARGV[1],毫秒值
	).Int64()
}

func (c *Client) Pipeline() redis.Pipeliner {
	if c == nil || c.rdb == nil {
		return nil
	}
	return c.rdb.Pipeline()
}

func (c *Client) Incr(ctx context.Context, key string) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client not initialized")
	}
	return c.rdb.Incr(ctx, key).Result()
}

// incrIfExistsScript 只在 Key 已由读侧回填时递增缓存值。
// 缓存缺失不能直接 INCR：例如真实粉丝数是 500，Redis 重启后直接 INCR 会错误得到 1。
var incrIfExistsScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  return 0
end
redis.call("INCR", KEYS[1])
return 1
`)

// IncrIfExists 原子地递增一个已存在的整数缓存。
// updated=false 表示缓存未命中，调用方应保持它缺失，等待读侧按 MySQL 数据回填。
func (c *Client) IncrIfExists(ctx context.Context, key string) (updated bool, err error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis client not initialized")
	}
	result, err := incrIfExistsScript.Run(ctx, c.rdb, []string{key}).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// decrIfExistsScript 与 IncrIfExists 对应：仅对已存在的缓存递减，且不允许结果小于 0。
var decrIfExistsScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 0 then
  return 0
end
local count = redis.call("DECR", KEYS[1])
if count < 0 then
  redis.call("SET", KEYS[1], 0)
end
return 1
`)

// DecrIfExists 原子地递减一个已存在的整数缓存，结果最低保持为 0。
// updated=false 表示缓存未命中，调用方无需创建该缓存。
func (c *Client) DecrIfExists(ctx context.Context, key string) (updated bool, err error) {
	if c == nil || c.rdb == nil {
		return false, errors.New("redis client not initialized")
	}
	result, err := decrIfExistsScript.Run(ctx, c.rdb, []string{key}).Int64()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// EvalInt executes a Lua script atomically and returns its integer result.
// 业务模块只传入已经由 Key 生成的键，避免直接暴露底层 Redis client。
func (c *Client) EvalInt(ctx context.Context, script string, keys []string, args ...any) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client not initialized")
	}
	return c.rdb.Eval(ctx, script, keys, args...).Int64()
}

// RunScriptInt 执行固定 Lua 脚本。go-redis 会优先使用 Redis 已缓存的脚本 SHA1，
// 遇到 NOSCRIPT 时自动回退到 EVAL 重新加载脚本。
func (c *Client) RunScriptInt(ctx context.Context, script *redis.Script, keys []string, args ...any) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, errors.New("redis client not initialized")
	}
	if script == nil {
		return 0, errors.New("redis script is nil")
	}
	return script.Run(ctx, c.rdb, keys, args...).Int64()
}

func (c *Client) SMembers(ctx context.Context, key string) ([]string, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client not initialized")
	}
	return c.rdb.SMembers(ctx, key).Result()
}
