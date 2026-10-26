package content

import "time"

const (
	ContentTypeVideo   = "video"
	ContentTypeImage   = "image"
	ContentTypeArticle = "article"
)

// Content
// 索引一览（★ Feed 分页核心）：
//
//	idx_videos_author_id           (author_id)                     → 查某个作者的作品
//	idx_videos_create_time         (create_time DESC)              → 最新流
//	idx_videos_likes_count_id      (likes_count DESC, id DESC)     → 点赞榜（复合游标分页）
//	idx_videos_popularity_time_id  (popularity DESC, create_time DESC, id DESC) → 热榜（复合游标分页）
//
// GORM 索引 tag 规则：index → 单列 | index:名称 → 指定名 | priority:N → 复合索引第N列 | sort:desc → 倒序
type Content struct {
	ID                    uint           `gorm:"primaryKey;index:idx_contents_likes_count_id,priority:2,sort:desc;index:idx_contents_popularity_time_id,priority:3,sort:desc" json:"id"`
	AuthorID              uint           `gorm:"index;not null" json:"author_id"`
	Username              string         `gorm:"type:varchar(255);not null" json:"username"`
	ContentType           string         `gorm:"type:varchar(16);not null;default:video;index" json:"content_type"`
	Title                 string         `gorm:"type:varchar(255);not null" json:"title"`
	Description           string         `gorm:"type:varchar(500)" json:"description,omitempty"`
	Body                  string         `gorm:"type:longtext" json:"body,omitempty"`
	PlayURL               string         `gorm:"type:varchar(500)" json:"play_url,omitempty"`
	CoverURL              string         `gorm:"type:varchar(500)" json:"cover_url,omitempty"`
	CreateTime            time.Time      `gorm:"autoCreateTime;index:idx_contents_create_time,sort:desc;index:idx_contents_popularity_time_id,priority:2,sort:desc" json:"create_time"`
	LikesCount            int64          `gorm:"not null;default:0;index:idx_contents_likes_count_id,priority:1,sort:desc" json:"likes_count"`
	LikesCountVersion     uint64         `gorm:"not null;default:0" json:"-"`
	LikesReconcileEnabled bool           `gorm:"not null;default:false" json:"-"`
	Popularity            int64          `gorm:"not null;default:0;index:idx_contents_popularity_time_id,priority:1,sort:desc" json:"popularity"`
	Assets                []ContentAsset `gorm:"foreignKey:ContentID" json:"assets,omitempty"`
}

// ContentAsset 保存内容的有序媒体资源。图片内容可拥有多条 image 资源；
// 视频和文章封面也可作为资源扩展，而时间线只需要保存统一的 ContentID。
type ContentAsset struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ContentID uint      `gorm:"not null;uniqueIndex:ux_content_asset_order,priority:1;index" json:"content_id"`
	AssetType string    `gorm:"type:varchar(16);not null;uniqueIndex:ux_content_asset_order,priority:2" json:"asset_type"`
	URL       string    `gorm:"type:varchar(500);not null" json:"url"`
	SortOrder int       `gorm:"not null;uniqueIndex:ux_content_asset_order,priority:3" json:"sort_order"`
	CreatedAt time.Time `gorm:"autoCreateTime" json:"created_at"`
}

// ✅
// PublishContentRequest 统一承载视频、图片和文章发布请求。
type PublishContentRequest struct {
	ContentType string   `json:"content_type" binding:"required,oneof=video image article"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	PlayURL     string   `json:"play_url"`
	CoverURL    string   `json:"cover_url"`
	ImageURLs   []string `json:"image_urls"`
}

// ✅
// 删除视频
type DeleteContentRequest struct {
	ID uint `json:"id"`
}

// ✅
// 用户id
type ListByAuthorIDRequest struct {
	AuthorID uint `json:"author_id"`
}

// ✅
// 视频id
type GetDetailRequest struct {
	ID uint `json:"id"`
}

type UpdateLikesCountRequest struct {
	ID         uint  `json:"id"`
	LikesCount int64 `json:"likes_count"`
}
