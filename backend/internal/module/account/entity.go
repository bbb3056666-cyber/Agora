package account

import "time"

// 数据结构(实体)定义
type Account struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Username string `gorm:"type:varchar(191);unique" json:"username"` //username加上唯一约束(索引)
	Password string `gorm:"type:varchar(255)" json:"-"`               //密码,bcrypt加密
	//Token        string  `gorm:"type:varchar(1024)" json:"-"`高频.无状态,不应该存数据库
	//RefreshToken string `gorm:"type:varchar(512);uniqueIndex:idx_unique_refresh_token" json:"-"`
	AvatarURL string `gorm:"type:varchar(512)" json:"avatar_url,omitempty"` //头像url
	Bio       string `gorm:"type:varchar(255)" json:"bio,omitempty"`        //个人简介
	// 冗余保存粉丝总数：发布视频时只需读取这一列判断是否需要 fan-out，
	// 无须先查询全部粉丝关系。关注/取关时会与 socials 表在同一事务内维护它。
	FollowerCount int64 `gorm:"not null;default:0" json:"follower_count"`
	//gorm: "uniqueIndex: idx_unique_refresh_token"
	//gorm: "index: idx_refresh_token unique"同一个东西
	//CREATE UNIQUE INDEX idx_refresh_token ON accounts (refresh_token)
	//UNIQUE约束 = 普通索引结构 + 唯一性检查 = UNIQUE INDEX (为null时不判断)
	//PrimaryKey = 唯一索引 + not null
}

// 状态常量
const (
	RefreshTokenStatusActive  int8 = 1 // 有效
	RefreshTokenStatusRevoked int8 = 0 // 已撤销
)

const (
	// rotated 表示旧 Token 已被一次正常刷新替换；它再次出现通常意味着 Token 被复制后复用。
	RefreshTokenRevokedReasonRotated = "rotated"
	RefreshTokenRevokedReasonLogout  = "logout"
	// 改密码和检测到旧 Token 复用都会撤销当前仍有效的全部会话，但保留不同原因便于审计。
	RefreshTokenRevokedReasonPasswordChanged = "password_changed"
	RefreshTokenRevokedReasonRefreshReuse    = "refresh_reuse"
)

// RefreshToken 刷新令牌记录，支持多设备管理
type RefreshToken struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	TokenHash     string     `gorm:"type:varchar(64);uniqueIndex:uk_token_hash" json:"-"`
	UserID        uint       `gorm:"index:idx_user_id_status" json:"user_id"` //复合索引,加速userID和status复合查询
	DeviceID      *string    `gorm:"type:varchar(64)" json:"device_id,omitempty"`
	DeviceName    *string    `gorm:"type:varchar(100)" json:"device_name,omitempty"`
	IPAddress     *string    `gorm:"type:varchar(45)" json:"ip_address,omitempty"`
	Status        int8       `gorm:"type:tinyint;default:1;index:idx_user_id_status" json:"status"` //复合索引,加速userID和status复合查询
	RevokedReason string     `gorm:"type:varchar(32)" json:"revoked_reason,omitempty"`              // 仅 status=revoked 时有意义，用于区分轮换与主动撤销
	ExpireAt      time.Time  `gorm:"not null" json:"expire_at"`
	CreatedAt     time.Time  `gorm:"autoCreateTime" json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
}

// ✅
// 创建账户用
type CreateAccountRequest struct {
	Username string `json:"username" binding:"required,min=3,max=32,alphanum"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

type RenameRequest struct {
	NewUsername string `json:"new_username"`
}

// ✅
type FindByIDRequest struct {
	ID uint `json:"id"`
}

type FindByIDResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

type FindByUsernameRequest struct {
	Username string `json:"username"`
}

type FindByUsernameResponse struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

// ✅
// 修改密码用
type ChangePasswordRequest struct {
	Username    string `json:"username"`
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// ✅
// 登陆用
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ✅
// 登录/刷新access token返回数据用
type LoginResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    uint   `json:"account_id"`
	Username     string `json:"username"`
}

// ✅
// 更新资料用
type UpdateProfileRequest struct {
	AvatarURL string `json:"avatar_url"`
	Bio       string `json:"bio"`
}

// ✅
// 用refresh token更新access token
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type GetProfileRequest struct {
	AccountID uint `json:"account_id"`
}

type GetProfileResponse struct {
	Account       FindByIDResponse `json:"account"`
	ContentCount  int64            `json:"content_count"`
	TotalLikes    int64            `json:"total_likes"`
	FollowerCount int64            `json:"follower_count"`
	VloggerCount  int64            `json:"vlogger_count"`
}
