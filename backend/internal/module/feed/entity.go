package feed

import "time"

type FeedAuthor struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

type FeedAsset struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	SortOrder int    `json:"sort_order"`
}

type FeedContentItem struct {
	ID          uint        `json:"id"`
	Author      FeedAuthor  `json:"author"`
	ContentType string      `json:"content_type"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Body        string      `json:"body,omitempty"`
	PlayURL     string      `json:"play_url,omitempty"`
	CoverURL    string      `json:"cover_url,omitempty"`
	Assets      []FeedAsset `json:"assets,omitempty"`
	CreateTime  int64       `json:"create_time"`
	LikesCount  int64       `json:"likes_count"`
	IsLiked     bool        `json:"is_liked"`
}

type ListLatestRequest struct {
	Limit      int   `json:"limit"`       //每页多少条视频
	LatestTime int64 `json:"latest_time"` //上页最后一条视频时间戳,毫秒
}

type ListLatestResponse struct {
	ContentList []FeedContentItem `json:"content_list"`
	NextTime    int64             `json:"next_time"`
	HasMore     bool              `json:"has_more"`
}

type ListLikesCountRequest struct {
	Limit            int    `json:"limit"`
	LikesCountBefore *int64 `json:"likes_count_before,omitempty"` //为了区分点赞为0和第一页的情况
	IDBefore         *uint  `json:"id_before,omitempty"`
}

type LikesCountCursor struct {
	LikesCount int64
	ID         uint
}

type ListLikesCountResponse struct {
	ContentList []FeedContentItem `json:"content_list"`
	//序列化时 json.Marshal 自动用 *ptr 取值，前端拿到的是数字
	NextLikesCountBefore *int64 `json:"next_likes_count_before,omitempty"`
	NextIDBefore         *uint  `json:"next_id_before,omitempty"`
	HasMore              bool   `json:"has_more"`
}

type ListByFollowingRequest struct {
	Limit      int   `json:"limit"`
	LatestTime int64 `json:"latest_time"`
}

type ListByFollowingResponse struct {
	ContentList []FeedContentItem `json:"content_list"`
	NextTime    int64             `json:"next_time"`
	HasMore     bool              `json:"has_more"`
}

type ListByPopularityRequest struct {
	Limit  int   `json:"limit"`
	AsOf   int64 `json:"as_of"`  // 服务器返回的分钟时间戳；在一次浏览内完全不变
	Offset int   `json:"offset"` // 下一页从这里开始；第一页传0,在一次浏览内严格递增,下拉刷新,退出后变为0
	// DB fallback 用
	LatestIDBefore   *uint     `json:"latest_id_before,omitempty"`
	LatestPopularity *int64    `json:"latest_popularity"`
	LatestBefore     time.Time `json:"latest_before"`
}

type ListByPopularityResponse struct {
	ContentList []FeedContentItem `json:"content_list"`
	AsOf        int64             `json:"as_of"`       //在一次浏览内完全不变
	NextOffset  int               `json:"next_offset"` //在一次浏览内严格递增,下拉刷新,退出后变为0
	HasMore     bool              `json:"has_more"`

	NextLatestPopularity *int64     `json:"next_latest_popularity,omitempty"`
	NextLatestBefore     *time.Time `json:"next_latest_before,omitempty"`
	NextLatestIDBefore   *uint      `json:"next_latest_id_before,omitempty"`
}
