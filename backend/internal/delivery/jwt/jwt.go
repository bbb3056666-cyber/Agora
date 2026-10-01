package jwt

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/auth"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// JWTAuth ✅
// 没有token,签名不对,token过期,密钥不对,token失效,返回401未授权
// 核心,每次请求的JWT令牌校验
func JWTAuth(cache *rediscache.Client) func(*gin.Context) {
	return func(c *gin.Context) {
		var tokenString string
		var err error
		//获取access token
		if tokenString, err = extractToken(c); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
			return
		}

		//验证签名,时间,获取claims
		claims, err := auth.ParseToken(tokenString)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		//签名本身正确,但还得验证有没有登出(黑名单)
		check(c, claims, cache)
	}
}

func extractToken(c *gin.Context) (string, error) {
	//Authorization: Bearer <token>
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		//调用abort,假如下面有next,忽略,一直走到return
		//返回401,代表未授权
		//c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
		return "", errors.New("missing authorization header")
	}
	//切成两份
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		//c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header"})
		return "", errors.New("invalid authorization header")
	}
	//获取到access token
	tokenString := parts[1]
	return tokenString, nil
}

// ✅
// 软验证
func SoftJWTAuth(cache *rediscache.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			//没有token也给通过,游客
			c.Next()
			return
		}

		//有就验证
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization header"})
			return
		}

		tokenString := parts[1]

		claims, err := auth.ParseToken(tokenString)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		check(c, claims, cache)
	}
}

// ✅
// JWT 验签通过后检查：1)token版本号(全部踢下线用) 2)jti黑名单(单设备登出用)
func check(c *gin.Context, claims *auth.Claims, cache *rediscache.Client) {
	//有redis才校验
	if cache != nil {
		//每次 Redis 操作都包个 50ms 超时-Redis 正常响应 <1ms,设 50ms 是防网络抖动
		cacheCtx, cancel := context.WithTimeout(c.Request.Context(), 50*time.Millisecond)
		defer cancel()

		//1. 版本号校验:token里的ver必须等于当前token_version:<uid>
		//   踢下线时INCR版本号,旧token的ver不匹配→拒绝。读不到当0,和getTokenVersion对称
		curV, err := cache.GetBytes(cacheCtx, cache.Key("token_version:%d", claims.AccountID))
		cancel()
		var curVer int64
		if err == nil || errors.Is(err, redis.Nil) {
			if err == nil {
				//字节 -> 字符串 -> int64
				curVer, _ = strconv.ParseInt(string(curV), 10, 64)
			} else {
				//redis中不存在版本号,说明没改过密码
				curVer = 0
			}
			if curVer != claims.Ver {
				//版本不匹配:该用户的token已全部被踢下线
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token has been revoked"})
				return
			}
		} else {
			//Redis故障:降级放行,依赖JWT签名本身
			log.Printf("token version check failed, allow: %v", err)
		}

		//2. jti黑名单校验:单设备登出的token会加入黑名单
		if claims.ID != "" {
			blacklisted, err := cache.Exists(cacheCtx, cache.Key("blacklist:%s", claims.ID))
			cancel()
			//任务执行完,手动释放计时器,防止一直占用资源
			if err == nil {
				if blacklisted {
					//access token已失效,拒绝请求
					c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token has been revoked"})
					return
				}
			} else {
				//Redis故障:降级放行,依赖JWT签名本身,不因Redis挂而拒绝所有请求
				log.Printf("blacklist check failed, allow: %v", err)
			}
		}
	}

	//向请求上下文写入用户id,jti,过期时间
	c.Set("accountID", claims.AccountID)
	c.Set("jti", claims.ID)
	if claims.ExpiresAt != nil {
		c.Set("exp", claims.ExpiresAt.Time)
	}
	//调用下一个中间件
	c.Next()
}

// ✅
// 取出中间件解析JWT令牌时存入请求上下文的用户id
func GetAccountID(c *gin.Context) (uint, error) {
	uidValue, exists := c.Get("accountID")
	if !exists {
		//不存在
		return 0, errors.New("accountID not found")
	}

	accountID, ok := uidValue.(uint)
	if !ok { //accountID格式不对
		return 0, errors.New("accountID has invalid type")
	}

	return accountID, nil
}
