package like

import "time"

// LikeState 保存一对“用户 - 视频”关系的最新业务状态和版本。
// 取消点赞后不删除该行，否则旧 unlike / like 延迟到达时会丢失版本依据。
// 后续 LikeIntent Worker 只接受 version 更大的意图，从而避免异步重试造成旧操作覆盖新意图。
type LikeState struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	AccountID uint       `gorm:"not null;uniqueIndex:ux_like_states_account_video,priority:1;index:idx_like_states_account_liked_at,priority:1" json:"account_id"`
	ContentID uint       `gorm:"not null;uniqueIndex:ux_like_states_account_video,priority:2" json:"content_id"`
	IsLiked   bool       `gorm:"not null;default:false;index:idx_like_states_account_liked_at,priority:2" json:"is_liked"`
	LikedAt   *time.Time `gorm:"index:idx_like_states_account_liked_at,priority:3,sort:desc" json:"liked_at,omitempty"`
	// HasContributedPopularity 表示该用户曾为该视频贡献过一次热度；取消后不重置。
	HasContributedPopularity bool      `gorm:"not null;default:false" json:"has_contributed_popularity"`
	Version                  uint64    `gorm:"not null;default:0" json:"version"`
	AppliedVersion           uint64    `gorm:"not null;default:0" json:"applied_version"`
	UpdatedAt                time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}

type LikeRequest struct {
	ContentID uint `json:"content_id"`
}
