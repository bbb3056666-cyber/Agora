package activity

import "time"

// ContentActivity 是挂在某条内容下的限量活动。RemainingQuota 是 MySQL 的最终名额账本。
type ContentActivity struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ContentID      uint      `gorm:"not null;index" json:"content_id"`
	CreatorID      uint      `gorm:"not null;index" json:"creator_id"`
	Title          string    `gorm:"size:100;not null" json:"title"`
	Description    string    `gorm:"size:500" json:"description"`
	Quota          int64     `gorm:"not null" json:"quota"`           //总名额
	RemainingQuota int64     `gorm:"not null" json:"remaining_quota"` //剩余名额
	StartAt        time.Time `gorm:"not null;index" json:"start_at"`
	EndAt          time.Time `gorm:"not null;index" json:"end_at"`
	CreatedAt      time.Time `json:"created_at"`
}

func (ContentActivity) TableName() string { return "content_activities" }

// ActivityRegistration
// 联合唯一索引保证 MQ 重复投递、Redis 故障等情况下仍然一人只能报名一次。
type ActivityRegistration struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	ActivityID uint      `gorm:"not null;uniqueIndex:uk_content_activity_account,priority:1" json:"activity_id"`
	AccountID  uint      `gorm:"not null;uniqueIndex:uk_content_activity_account,priority:2" json:"account_id"`
	EventID    string    `gorm:"size:64;not null;uniqueIndex" json:"event_id"`
	CreatedAt  time.Time `json:"created_at"`
}

func (ActivityRegistration) TableName() string { return "activity_registrations" }

type CreateActivityRequest struct {
	ContentID   uint   `json:"content_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Quota       int64  `json:"quota"`    //总名额
	StartAt     int64  `json:"start_at"` // Unix seconds
	EndAt       int64  `json:"end_at"`
}

type ActivityIDRequest struct {
	ActivityID uint `json:"activity_id"`
}
