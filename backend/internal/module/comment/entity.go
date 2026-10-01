package comment

import "time"

type Comment struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// 每条评论绑定来源 Intent ID，唯一索引防止 MQ 重投重复插入。
	SourceEventID string    `gorm:"type:varchar(64);not null;uniqueIndex" json:"-"`
	AuthorID      uint      `gorm:"index" json:"author_id"`
	Username      string    `gorm:"index" json:"username"`
	ContentID     uint      `gorm:"index" json:"content_id"`
	Content       string    `gorm:"type:text" json:"content"`
	CreatedAt     time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// CommentPopularityState 记录用户是否已为某个视频贡献过评论热度。
// 评论删除后保留这条状态，因此再次评论也不会重复加分。
type CommentPopularityState struct {
	ID        uint `gorm:"primaryKey"`
	AuthorID  uint `gorm:"not null;uniqueIndex:ux_comment_popularity_author_video"`
	ContentID uint `gorm:"not null;uniqueIndex:ux_comment_popularity_author_video"`
}

type PublishCommentRequest struct {
	ContentID uint   `json:"content_id"`
	Content   string `json:"content"`
}

type DeleteCommentRequest struct {
	CommentID uint `json:"comment_id"`
}

type GetAllCommentsRequest struct {
	ContentID uint `json:"content_id"`
}
