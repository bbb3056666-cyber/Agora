// internal/auth/jwt.go
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

//纯签名包,和gin无关,换别的框架也能用

// 防止环境变量中没有密钥时不断换新随机密钥
var (
	secretOnce sync.Once
	signingKey []byte
)

// ✅
// 获得JWT密钥字节数组,环境变量没有就随机生成
func jwtSecret() []byte {
	secretOnce.Do(func() {
		//先从环境变量获取
		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				log.Printf("FATAL: cannot generate JWT secret: %v", err)
				panic("cannot generate JWT secret")
			}
			//生成64字符十六进制字符串
			secret = hex.EncodeToString(b)
			log.Printf("WARNING: JWT_SECRET not set, generated random key. All tokens invalid on restart.")
		}
		signingKey = []byte(secret)
	})
	return signingKey
}

type Claims struct {
	//自定义字段
	AccountID uint   `json:"account_id"`
	Username  string `json:"username"`
	//Ver:用户token版本号,用于"全部设备踢下线"机制。改密码/重置时版本号+1,旧token的ver不匹配→失效
	Ver int64 `json:"ver,omitempty"`
	jwt.RegisteredClaims
	//嵌入（无字段名）→ JSON 平铺,继承方法,直接调用嵌入结构体的方法,无需通过结构体内变量名调用
	//因为jwt.RegisteredClaims实现了type Claims interface {}所以我的Claims也实现了这个接口
	//带变量名 → JSON带变量名,不继承方法,必须通过变量名调用嵌入结构体的方法,无法实现type Claims interface {}
}

// GenerateToken ✅
// Base64:每六个bit表示一个符号,所以需要2^6=64个符号
// 用id和用户名生成新token,token里包含id和username信息
// 第一段 (Header): eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9
// Base64 解码 → {"alg":"HS256","typ":"JWT"}   ← 签名计算算法和令牌类型(JWT)
//
// 第二段 (Payload): eyJhY2NvdW50X2lkIjoxLCJ1c2VybmFtZSI6InRvbSIsImV4cCI6MTc0MjIwMDAwMH0
// Base64 解码 → {"account_id":1,"username":"tom","exp":1742200000}   ← 公开可读！
//
// 第三段 (Signature): x9k3f2aBcD1eF4gH5iJ6kL7mN8oP0qR1sT2uV3wX4yZ
// HMAC-SHA256(第一段.第二段, JWT_SECRET)   ← 防篡改
// JWT Access Token：把用户信息编入，15分钟有效，用 HMAC-SHA256 签名，防篡改
// ver:用户token版本号,来自Redis的token_version:<userID>,用于全部设备踢下线机制
func GenerateToken(accountID uint, username string, ver int64) (string, error) {
	return GenerateTokenWithTTL(accountID, username, ver, 15*time.Minute)
}

func GenerateTokenWithTTL(accountID uint, username string, ver int64, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", errors.New("access token ttl must be positive")
	}

	now := time.Now()

	claims := Claims{
		AccountID: accountID,
		Username:  username,
		Ver:       ver,
		//没有字段名的字段初始化时，Go 自动用 RegisteredClaims 这个类型名当字段名
		RegisteredClaims: jwt.RegisteredClaims{
			//jti:JWT唯一ID,用于登出黑名单机制
			ID:        newTokenID(),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	//生成前两段
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	//用密钥生成最终token字符串
	return token.SignedString(jwtSecret())
}

// 生成JWT唯一ID(jti),用于登出黑名单机制
func newTokenID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// GenerateRefreshToken ✅
// 随机字符串，不含任何用户信息，存数据库，7天有效，用于换取新的 Access Token
func GenerateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ParseToken ✅
// 验证签名,查过期,得到id和用户名
func ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(
		tokenString,
		//传入模板,识别类型,创建对象,填数据,断言使用
		&Claims{},
		//检查算法,返回密钥的函数
		func(token *jwt.Token) (interface{}, error) {
			//检查算法,防止篡改为none,库跳过签名验证
			if token.Method == nil || token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
				return nil, errors.New("unexpected signing method")
			}
			//返回密钥字节数组
			return jwtSecret(), nil
		},
	)
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, jwt.ErrTokenInvalidClaims
	}

	return claims, nil
}
