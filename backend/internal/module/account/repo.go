package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

//数据库操作（纯 SQL/GORM）

type AccountRepository struct {
	db *gorm.DB
}

func NewAccountRepository(db *gorm.DB) *AccountRepository {
	return &AccountRepository{db: db}
}

// ✅
func (ar *AccountRepository) CreateAccount(ctx context.Context, account *Account) error {
	// WithContext(ctx) 让 GORM 感知请求生命周期：ctx 取消则中断 SQL，带超时则不会无限卡住
	return ar.db.WithContext(ctx).Create(account).Error
}

// ✅
// 同时更新用户名和token
//func (ar *AccountRepository) RenameWithToken(ctx context.Context, id uint, newUsername string, token string) error {
//	return ar.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
//		result := tx.Model(&Account{}).Where("id = ?", id).Update("username", newUsername)
//		if result.Error != nil {
//			return result.Error
//		}
//		if result.RowsAffected == 0 {
//			return gorm.ErrRecordNotFound
//		}
//		if err := tx.Model(&Account{}).Where("id = ?", id).Update("token", token).Error; err != nil {
//			return err
//		}
//		return nil
//	})
//}

// ✅
// 更新用户名
func (ar *AccountRepository) Rename(ctx context.Context, id uint, newUsername string) error {
	return ar.db.WithContext(ctx).Model(&Account{}).
		Where("id = ?", id).
		Update("username", newUsername).Error
}

// ✅
// 修改密码
func (ar *AccountRepository) ChangePassword(ctx context.Context, id uint, newPassword string) error {
	return ar.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Update("password", newPassword).Error
}

// ✅
// 根据id查找账户
func (ar *AccountRepository) FindByID(ctx context.Context, id uint) (*Account, error) {
	var account Account
	if err := ar.db.WithContext(ctx).First(&account, id).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

// GetFollowerCount 只读取粉丝计数列，避免高频读取时加载完整账户信息。
func (ar *AccountRepository) GetFollowerCount(ctx context.Context, id uint) (int64, error) {
	var account Account
	if err := ar.db.WithContext(ctx).
		Select("follower_count").
		First(&account, id).Error; err != nil {
		return 0, err
	}
	return account.FollowerCount, nil
}

// ✅
// 根据用户名找账户
func (ar *AccountRepository) FindByUsername(ctx context.Context, username string) (*Account, error) {
	var account Account
	//前面的方法（Where/Order/Limit/Joins/Select）：只加查询条件，不执行SQL，顺序随便排
	//最后的方法（First/Find/Update/Delete/Scan）：触发SQL执行，必须放在最后。
	if err := ar.db.WithContext(ctx).Where("username = ?", username).First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

// ✅
// 存入两个token
func (ar *AccountRepository) Login(ctx context.Context, id uint, refreshToken string) error {
	if err := ar.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(map[string]interface{}{"refresh_token": refreshToken}).Error; err != nil {
		return err
	}
	return nil
}

// 插入refresh token
func (ar *AccountRepository) CreateRefresh(ctx context.Context, refresh *RefreshToken) error {
	return ar.db.WithContext(ctx).Create(refresh).Error
}

// 计算refresh token的SHA-256哈希,数据库中只存哈希不存明文,防数据库泄露后token被盗用
// SHA-256: 纯哈希函数,单向,生成256位(32字节)哈希值
func hashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// FindRefresh 找到refresh token
func (ar *AccountRepository) FindRefresh(ctx context.Context, refresh string) (*RefreshToken, error) {
	var refreshToken RefreshToken
	if err := ar.db.WithContext(ctx).First(&refreshToken, "token_hash = ?", hashRefreshToken(refresh)).Error; err != nil {
		return nil, err
	}
	return &refreshToken, nil
}

func (ar *AccountRepository) RevokeSingleToken(ctx context.Context, refresh string) error {
	return ar.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("token_hash = ?", hashRefreshToken(refresh)).
		Updates(map[string]interface{}{
			"status":         RefreshTokenStatusRevoked,
			"revoked_reason": RefreshTokenRevokedReasonLogout,
		}).Error
}

// RevokeAllActiveTokens 只改仍 active 且未过期的 Token；已撤销或过期的历史记录保留原状态和原因。
func (ar *AccountRepository) RevokeAllActiveTokens(ctx context.Context, id uint, reason string) error {
	return ar.db.WithContext(ctx).Model(&RefreshToken{}).
		Where("user_id = ? AND status = ? AND expire_at > ?", id, RefreshTokenStatusActive, time.Now()).
		Updates(map[string]interface{}{
			"status":         RefreshTokenStatusRevoked,
			"revoked_reason": reason,
		}).Error
}

// ✅
// 清空两个token
func (ar *AccountRepository) Logout(ctx context.Context, id uint) error {
	return ar.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(map[string]interface{}{"token": "", "refresh_token": nil}).Error
}

// ✅
// 上传头像URL
func (ar *AccountRepository) UpdateAvatar(ctx context.Context, accountID uint, avatarURL string) error {
	return ar.db.WithContext(ctx).Model(&Account{}).Where("id = ?", accountID).Update("avatar_url", avatarURL).Error
}

// ✅
// 更新账户access token
func (ar *AccountRepository) UpdateToken(ctx context.Context, id uint, token string) error {
	return ar.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Update("token", token).Error
}

// ✅
// 更新用户名或个人简介
func (ar *AccountRepository) UpdateFields(ctx context.Context, id uint, updates map[string]interface{}) error {
	return ar.db.WithContext(ctx).Model(&Account{}).Where("id = ?", id).Updates(updates).Error
}

// ✅
// 获取全部用户
func (ar *AccountRepository) FindAll(ctx context.Context) ([]*Account, error) {
	var accounts []*Account
	if err := ar.db.WithContext(ctx).Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

// ✅
//func (ar *AccountRepository) FindByIDAndRefresh(ctx context.Context, refresh string) (*Account, error) {
//	var account Account
//	if err := ar.db.WithContext(ctx).Model(&Account{}).
//		Where("refresh_token = ?", refresh).First(account).Error; err != nil {
//		return nil, err
//	}
//	return &account, nil
//}
