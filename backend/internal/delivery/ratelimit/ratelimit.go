package ratelimit

import (
	"context"
	"fmt"
	jwt "github.com/bbb3056666-cyber/Agora/backend/internal/delivery/jwt"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ✅
// 返回限流中间件
func GetLimiter(
	cache *rediscache.Client,
	keyPrefix string,
	maxRequests int64,
	window time.Duration,
	getKey func(*gin.Context) (string, bool),
) gin.HandlerFunc {
	return func(c *gin.Context) {
		//没有redis或参数不合法,直接进入下一个中间件
		if cache == nil || getKey == nil || maxRequests <= 0 || window <= 0 {
			c.Next()
			return
		}
		//获取限流对象
		subject, ok := getKey(c)
		if !ok {
			c.Next()
			return
		}
		//key = rateLimit:account_login:1.2.3.4
		key := buildKey(keyPrefix, subject)
		//① 首次请求: key 不存在 → INCR 自动创建 → count=1 → 触发 PEXPIRE 设 60 秒过期
		//② 后续请求: key 已存在 → INCR 直接 +1 → count=2/3/4... → 不做 PEXPIRE
		//③ 60 秒后:   key 自动消失 → 下一次就是全新的①周期
		ctx, cancel := context.WithTimeout(c.Request.Context(), 50*time.Millisecond)
		defer cancel()
		//将key的值+1并拿到结果
		count, err := cache.IncrementWithExpire(ctx, key, window)
		if err != nil {
			c.Next()
			return
		}
		//同一ip/用户一段时间请求超过限制次数,直接abort禁止访问,返回,不进入下一个中间件
		if count > maxRequests {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "too many requests",
			})
			return
		}
		//没有超过限制,进入下一个中间件
		c.Next()
	}
}

// ✅
// 构造存入redis的键
// account_login 1.2.3.4
func buildKey(keyPrefix, subject string) string {
	keyPrefix = strings.TrimSpace(keyPrefix)
	subject = strings.TrimSpace(subject)
	if keyPrefix == "" {
		keyPrefix = "default"
	}
	//rateLimit:account_login:1.2.3.4
	return fmt.Sprintf("rateLimit:%s:%s", keyPrefix, subject)
}

// GetIP ✅
// 从上下文获取客户端 IP
func GetIP(c *gin.Context) (string, bool) {
	//不信任代理就直接返回 TCP 连接方 IP，信任代理就返回 X-Forwarded-For 头里的(发送请求的ip)
	//攻击者会换不同ip
	ip := c.ClientIP()
	//去除首尾空格
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return "", false
	}
	return ip, true
}

// GetAccount ✅
// 从上下文获取用户id
func GetAccount(c *gin.Context) (string, bool) {
	accountID, err := jwt.GetAccountID(c)
	if err != nil || accountID == 0 {
		return "", false
	}
	return strconv.FormatUint(uint64(accountID), 10), true
}
