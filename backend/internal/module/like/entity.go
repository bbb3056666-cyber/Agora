package like

import "time"

// LikeState 同时保存已生效状态和 API 最近接受的期望状态。
// Worker 只需比较两者并向期望状态收敛，因此重复或乱序通知不会覆盖较新的用户意图。
type LikeState struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	AccountID    uint       `gorm:"not null;uniqueIndex:ux_like_states_account_video,priority:1;index:idx_like_states_account_liked_at,priority:1" json:"account_id"`
	ContentID    uint       `gorm:"not null;uniqueIndex:ux_like_states_account_video,priority:2;index:idx_like_states_content_liked,priority:1" json:"content_id"`
	IsLiked      bool       `gorm:"not null;default:false;index:idx_like_states_account_liked_at,priority:2;index:idx_like_states_content_liked,priority:2" json:"is_liked"`
	DesiredLiked bool       `gorm:"not null;default:false" json:"desired_liked"`
	LikedAt      *time.Time `gorm:"index:idx_like_states_account_liked_at,priority:3,sort:desc" json:"liked_at,omitempty"`
	// HasContributedPopularity 表示该用户曾为该视频贡献过一次热度；取消后不重置。
	HasContributedPopularity bool      `gorm:"not null;default:false" json:"has_contributed_popularity"`
	UpdatedAt                time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

type LikeRequest struct {
	ContentID uint `json:"content_id"`
}
