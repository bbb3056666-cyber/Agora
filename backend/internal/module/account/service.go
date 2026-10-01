package account

import (
	"context"
	"errors"
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/auth"
	"log"
	"strconv"
	"strings"
	"time"

	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

//业务逻辑

type AccountService struct {
	accountRepository *AccountRepository
	cache             *rediscache.Client
}

var (
	ErrUsernameTaken       = errors.New("username already exists")
	ErrNewUsernameRequired = errors.New("new_username is required")
	ErrAbnormalRefresh     = errors.New("abnormal refresh token usage")
)

// ✅
func NewAccountService(accountRepository *AccountRepository, cache *rediscache.Client) *AccountService {
	return &AccountService{accountRepository: accountRepository, cache: cache}
}

// 读用户当前的token版本号。没有记录则返回0(初始版本)。
// key: token_version:<userID>  value: int64版本号
func (as *AccountService) getTokenVersion(ctx context.Context, accountID uint) int64 {
	if as.cache == nil {
		return 0
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	b, err := as.cache.GetBytes(cacheCtx, as.cache.Key("token_version:%d", accountID))
	if err != nil {
		return 0 // 读不到就当0
	}
	v, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// ✅
func (as *AccountService) CreateAccount(ctx context.Context, account *Account) error {
	//业务层
	//密码加密
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(account.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	account.Password = string(passwordHash)
	if err := as.accountRepository.CreateAccount(ctx, account); err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return ErrUsernameTaken
		}
		return err
	}
	return nil
}

// ✅
// 先判断新用户名是否存在
// 生成新token,修改用户名和token,返回新token
func (as *AccountService) Rename(ctx context.Context, accountID uint, newUsername string) (string, error) {
	//错误:字符串为空,直接返回新用户名不存在错误
	if newUsername == "" {
		return "", ErrNewUsernameRequired
	}

	//生成新token
	token, err := auth.GenerateToken(accountID, newUsername, as.getTokenVersion(ctx, accountID))
	if err != nil {
		return "", err
	}

	if err := as.accountRepository.Rename(ctx, accountID, newUsername); err != nil {
		var mysqlErr *mysql.MySQLError
		//用户名重复
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return "", ErrUsernameTaken
		}
		//未找到账户
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", err
		}
		return "", err
	}
	//redis也要存
	//if as.cache != nil {
	//	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//	defer cancel()
	//
	//	if err := as.cache.SetBytes(cacheCtx, as.cache.Key("account:%d", accountID), []byte(token), 15*time.Minute); err != nil {
	//		log.Printf("failed to set cache: %v", err)
	//		// 写入失败,旧值不能留着,宁可让下次请求走"未命中查DB"这条路径
	//		delCtx, delCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//		defer delCancel()
	//		if delErr := as.cache.Del(delCtx, as.cache.Key("account:%d", accountID)); delErr != nil {
	//			//删除也失败,用户被强制踢下线一段时间
	//			log.Printf("failed to delete stale cache key: %v", delErr)
	//		}
	//		delCancel()
	//	}
	//}
	return token, nil
}

// ✅
func (as *AccountService) ChangePassword(ctx context.Context, username, oldPassword, newPassword string) error {
	account, err := as.FindByUsername(ctx, username)
	if err != nil {
		return err
	}
	//比对旧密码
	if err := bcrypt.CompareHashAndPassword([]byte(account.Password), []byte(oldPassword)); err != nil {
		return err
	}
	//生成新密码哈希
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := as.accountRepository.ChangePassword(ctx, account.ID, string(passwordHash)); err != nil {
		return err
	}
	//修改密码后所有设备退出登录
	if err := as.LogoutAll(ctx, account.ID, RefreshTokenRevokedReasonPasswordChanged); err != nil {
		log.Printf("change password: logout all devices failed, user_id=%d, err=%v", account.ID, err)
	}
	return nil
}

// ✅
// 根据id找账户
func (as *AccountService) FindByID(ctx context.Context, id uint) (*Account, error) {
	if account, err := as.accountRepository.FindByID(ctx, id); err != nil {
		return nil, err
	} else {
		return account, nil
	}
}

// ✅
// 根据用户名找账户
func (as *AccountService) FindByUsername(ctx context.Context, username string) (*Account, error) {
	if account, err := as.accountRepository.FindByUsername(ctx, username); err != nil {
		return nil, err
	} else {
		return account, nil
	}
}

// ✅
// 登录,核对密码,将jwt和随机字符串存入数据库
func (as *AccountService) Login(ctx context.Context, account *Account, password string) (string, string, error) {
	//比较密码哈希值,不能直接手动比较,因为每次生成的哈希都不同
	//盐 + 密码 ──→ 哈希值 单向
	if err := bcrypt.CompareHashAndPassword([]byte(account.Password), []byte(password)); err != nil {
		return "", "", err
	}

	//登陆成功

	//JWT
	accessToken, err := auth.GenerateToken(account.ID, account.Username, as.getTokenVersion(ctx, account.ID))
	if err != nil {
		return "", "", err
	}
	//64位随机字符串
	refreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		return "", "", err
	}

	//登录就是往RefreshToken表插入一条数据
	//将refreshToken存入数据库
	refresh := RefreshToken{
		TokenHash:  hashRefreshToken(refreshToken),
		UserID:     account.ID,
		Status:     RefreshTokenStatusActive,
		ExpireAt:   time.Now().Add(7 * 24 * time.Hour),
		LastUsedAt: nil,
	}
	if err := as.accountRepository.CreateRefresh(ctx, &refresh); err != nil {
		return "", "", err
	}
	//if err := as.accountRepository.Login(ctx, account.ID, refreshToken); err != nil {
	//	return "", "", err
	//}
	//存入redis
	//if as.cache != nil {
	//	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//	defer cancel()
	//	//存入access token
	//	if err := as.cache.SetBytes(cacheCtx, as.cache.Key("account:%d", account.ID), []byte(accessToken), 15*time.Minute); err != nil {
	//		log.Printf("failed to set cache: %v", err)
	//	}
	//	//存入refresh token
	//	if err := as.cache.SetBytes(cacheCtx, as.cache.Key("account:%d:refresh", account.ID), []byte(refreshToken), 7*24*time.Hour); err != nil {
	//		log.Printf("failed to set refresh cache: %v", err)
	//	}
	//	//通过refresh token反向查找用户id
	//	if err := as.cache.SetBytes(cacheCtx, as.cache.Key("refresh:%s", refreshToken), []byte(strconv.FormatUint(uint64(account.ID), 10)), 7*24*time.Hour); err != nil {
	//		log.Printf("failed to set refresh lookup: %v", err)
	//	}
	//}
	return accessToken, refreshToken, nil
}

// ✅
func (as *AccountService) Logout(ctx context.Context, refreshToken, jti string, ttl time.Duration) error {
	//1. 失效数据库中该refresh token
	if err := as.accountRepository.RevokeSingleToken(ctx, refreshToken); err != nil {
		return err
	}
	//2. 把access token的jti加入黑名单,TTL=token剩余有效期,过期自动消失
	if as.cache != nil && jti != "" {
		cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		key := as.cache.Key("blacklist:%s", jti)
		//ttl<=0时给个兜底值,防止SetBytes存永久key
		if ttl <= 0 {
			ttl = 15 * time.Minute
		}
		if err := as.cache.SetBytes(cacheCtx, key, []byte("1"), ttl); err != nil {
			log.Printf("failed to set blacklist: %v", err)
		}
	}
	return nil

	//已经登出
	//if account.Token == "" {
	//	return nil
	//}
	////删除redis缓存
	//if as.cache != nil {
	//	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//	defer cancel()
	//	keys := []string{
	//		//删除 account:1 = access_token
	//		as.cache.Key("account:%d", account.ID),
	//		//删除 account:1:refresh = refresh_token
	//		as.cache.Key("account:%d:refresh", account.ID),
	//	}
	//	//删除 refresh:refresh_token = 1
	//	if account.RefreshToken != nil {
	//		keys = append(keys, as.cache.Key("refresh:%s", account.RefreshToken))
	//	}
	//	if err := as.cache.DelBatch(cacheCtx, keys...); err != nil { // 变长参数一次删多个
	//		log.Printf("failed to del cache keys: %v", err)
	//	}
	//	cancel()
	//}
	//把数据库中账户的两个token字段设为空
	//return as.accountRepository.Logout(ctx, account.ID)
}

// ✅
// 踢下线该用户所有设备:1)撤销全部refresh token 2)INCR版本号,所有已签发的access token立即失效
// 用于修改密码/账号异常等需要全设备登出的场景
func (as *AccountService) LogoutAll(ctx context.Context, accountID uint, reason string) error {
	//1.  撤销当前仍 active 的 Token，历史 Token 的 rotated/logout 原因不被覆盖。
	if err := as.accountRepository.RevokeAllActiveTokens(ctx, accountID, reason); err != nil {
		return err
	}
	//2. INCR版本号,懒创建:不存在则从0变1。所有ver!=1的旧token失效
	if as.cache != nil {
		cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		//版本号key永久不过期:它是单调递增的持久状态,过期会导致版本号归零,和所有已签发token的ver失配,用户永远登不进去
		if _, err := as.cache.Incr(cacheCtx, as.cache.Key("token_version:%d", accountID)); err != nil {
			log.Printf("failed to increment token version, user_id=%d, err=%v", accountID, err)
		}
	}
	return nil
}

// ✅
// 上传头像URL
func (as *AccountService) UpdateAvatar(ctx context.Context, accountID uint, avatarURL string) error {
	return as.accountRepository.UpdateAvatar(ctx, accountID, avatarURL)
}

func (as *AccountService) FindAll(ctx context.Context) ([]*Account, error) {
	return as.accountRepository.FindAll(ctx)
}

// ✅
func (as *AccountService) UpdateProfile(ctx context.Context, accountID uint, req *UpdateProfileRequest) error {
	updates := map[string]interface{}{}
	if req.Bio != "" {
		//去掉字符串首尾的空白字符（空格、制表符、换行等）
		updates["bio"] = strings.TrimSpace(req.Bio)
	}
	if req.AvatarURL != "" {
		updates["avatar_url"] = strings.TrimSpace(req.AvatarURL)
	}
	if len(updates) == 0 {
		return errors.New("nothing to update")
	}
	return as.accountRepository.UpdateFields(ctx, accountID, updates)
}

// ✅
// 更新access token和refresh token
func (as *AccountService) RefreshAccessToken(ctx context.Context, refreshToken string) (string, string, uint, string, error) {
	if refreshToken == "" {
		return "", "", 0, "", errors.New("refresh token is empty")
	}
	//redis
	//if as.cache != nil {
	//	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//	defer cancel()
	//	//refresh:j4nedjo49fh4939hfmep...
	//	key := as.cache.Key("refresh:%s", refreshToken)
	//	//通过refresh token获取用户id[]byte
	//	b, err := as.cache.GetBytes(cacheCtx, key)
	//	if err == nil {
	//		//转成字符串
	//		idStr := string(b)
	//		//转成uint64
	//		id, parseErr := strconv.ParseUint(idStr, 10, 64)
	//		if parseErr == nil {
	//			account, err := as.FindByID(ctx, uint(id))
	//			if err == nil && account != nil && *account.RefreshToken == refreshToken {
	//				accountKey := as.cache.Key("account:%d", account.ID)
	//				newToken, err := auth.GenerateToken(account.ID, account.Username)
	//				if err != nil {
	//					return "", 0, "", err
	//				}
	//				if err := as.accountRepository.UpdateToken(ctx, account.ID, newToken); err != nil {
	//					return "", 0, "", err
	//				}
	//				//存在旧的则替换,确保下一次对比不会拿旧的token对比,无法通行
	//				if err := as.cache.SetBytes(cacheCtx, accountKey, []byte(newToken), 15*time.Minute); err != nil {
	//					log.Printf("failed to set cache: %v", err)
	//					//替换失败就删除键,让第一次请求在中间件走数据库,并写入redis
	//					delCtx, delCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	//					defer delCancel()
	//					if err := as.cache.Del(delCtx, accountKey); err != nil {
	//						//极端情况,删除也失败,用户15min无法请求
	//						log.Printf("CRITICAL: cannot clean stale token cache, user may be rejected for 15m: %v", err)
	//					}
	//					delCancel()
	//				}
	//				return newToken, account.ID, account.Username, nil
	//			}
	//		}
	//	}
	//}

	//找到refresh token
	refresh, err := as.accountRepository.FindRefresh(ctx, refreshToken)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", 0, "", errors.New("invalid refresh token:not found")
		}
		//数据库连接错误等，带上原始错误
		return "", "", 0, "", fmt.Errorf("find refresh token failed: %w", err)
	}
	// 过期 Token 没有安全分析价值，直接按普通无效 Token 拒绝。
	if refresh.ExpireAt.Before(time.Now()) {
		return "", "", 0, "", errors.New("invalid refresh token")
	}
	// status 已撤销但原因不同，处理方式不同：
	// rotated = 该 Token 已成功换出新 Token；它再次出现意味着同一凭据被第二方复用。
	// logout/password_changed/refresh_reuse = 主动撤销或已完成的安全处置，不能据此再次断言遭到攻击。
	if refresh.Status == RefreshTokenStatusRevoked {
		if refresh.RevokedReason == RefreshTokenRevokedReasonRotated {
			if err := as.LogoutAll(ctx, refresh.UserID, RefreshTokenRevokedReasonRefreshReuse); err != nil {
				log.Printf("CRITICAL: failed to invalidate account after rotated token reuse, user_id=%d, err=%v", refresh.UserID, err)
			}
			return "", "", 0, "", ErrAbnormalRefresh
		}
		return "", "", 0, "", errors.New("invalid refresh token")
	}

	//根据refreshToken找到指定账户
	acc, err := as.accountRepository.FindByID(ctx, refresh.UserID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", 0, "", errors.New("invalid refresh token")
		}
		return "", "", 0, "", err
	}

	//生成新refresh token
	var newRefreshToken, newToken string

	//开启事务-------------
	//return nil才提交
	if err := as.accountRepository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 这里所有操作都用 tx，不是 db
		var err error
		//第一步做废旧refresh token
		result := tx.Model(&RefreshToken{}).
			Where("user_id = ? AND status = ? AND token_hash = ?", acc.ID, RefreshTokenStatusActive, hashRefreshToken(refreshToken)).
			Updates(map[string]interface{}{
				"status":         RefreshTokenStatusRevoked,
				"revoked_reason": RefreshTokenRevokedReasonRotated,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			//发生了并发刷新或者攻击,作废全部refresh token,吊销全部access token
			return ErrAbnormalRefresh
		}
		//生成新refresh token
		newRefreshToken, err = auth.GenerateRefreshToken()
		if err != nil {
			return err
		}
		//插入表中
		newRefresh := &RefreshToken{
			TokenHash:  hashRefreshToken(newRefreshToken),
			UserID:     acc.ID,
			Status:     RefreshTokenStatusActive,
			ExpireAt:   time.Now().Add(7 * 24 * time.Hour),
			LastUsedAt: nil,
		}
		if err := tx.WithContext(ctx).Create(newRefresh).Error; err != nil {
			return err
		}
		newToken, err = auth.GenerateToken(acc.ID, acc.Username, as.getTokenVersion(ctx, acc.ID))
		if err != nil {
			return err
		}
		return nil // 返回 nil → 提交事务
	}); err != nil { // err != nil 说明事务回滚了
		if errors.Is(err, ErrAbnormalRefresh) {
			// 仅撤销 Refresh Token 不够：攻击者已有的 Access Token 仍会在剩余有效期内通过验签。
			// LogoutAll 会同时撤销全部 Refresh Token，并递增 token_version，使旧 Access Token 在下次 JWT 校验时失效。
			if err := as.LogoutAll(ctx, acc.ID, RefreshTokenRevokedReasonRefreshReuse); err != nil {
				log.Printf("CRITICAL: failed to invalidate account after abnormal refresh, user_id=%d, err=%v", acc.ID, err)
			}
			return "", "", 0, "", err
		}
		return "", "", 0, "", err
	}
	return newToken, newRefreshToken, acc.ID, acc.Username, nil
}
