package feed

import (
	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/jwt"
	"time"

	"github.com/gin-gonic/gin"
)

type FeedHandler struct {
	feedService *FeedService
}

func NewFeedHandler(service *FeedService) *FeedHandler {
	return &FeedHandler{feedService: service}
}

// ListLatest ✅
// 根据时间(先从redis中记录的1000条(global_timeline)最新查,1000条查完了再返回db查)
func (f *FeedHandler) ListLatest(c *gin.Context) {
	//绑定参数
	var req ListLatestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	//条数合法性
	if req.Limit <= 0 || req.Limit > 50 {
		req.Limit = 10
	}
	var latestTime time.Time
	if req.LatestTime > 0 {
		//time.UnixMilli(括号里的时毫秒值,转换成时间)
		//time.UnixMilli(1704067000123) ms
		//→ 2024-01-01 00:00:00 的 time.Time 对象
		latestTime = time.UnixMilli(req.LatestTime)
	}
	//获取观看者id
	viewerAccountID, err := jwt.GetAccountID(c)
	if err != nil {
		viewerAccountID = 0
	}

	//调用service层获取ListLatestResponse
	listLatestResponse, err := f.feedService.ListLatest(c.Request.Context(), req.Limit, latestTime, viewerAccountID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	//检查视频列表是否为nil
	listLatestResponse.ContentList = nonNilFeedContentItems(listLatestResponse.ContentList)
	c.JSON(200, listLatestResponse)
}

// ✅
// 按照点赞数查询视频
func (f *FeedHandler) ListLikesCount(c *gin.Context) {
	var req ListLikesCountRequest
	//	Limit            int    `json:"limit"`
	//	LikesCountBefore *int64 `json:"likes_count_before,omitempty"`
	//	IDBefore         *uint  `json:"id_before,omitempty"`
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if req.Limit <= 0 || req.Limit > 50 {
		req.Limit = 10
	}
	//游标包含点赞和id
	var cursor *LikesCountCursor
	if req.LikesCountBefore != nil || req.IDBefore != nil {
		if req.LikesCountBefore == nil || req.IDBefore == nil {
			c.JSON(400, gin.H{"error": "likes_count_before and id_before must be provided together"})
			return
		}

		//前一页最后一条视频点赞数和视频id
		likesCountBefore := *req.LikesCountBefore
		idBefore := *req.IDBefore

		if likesCountBefore < 0 {
			c.JSON(400, gin.H{"error": "invalid cursor: likes_count_before must be >= 0"})
			return
		}
		if idBefore == 0 {
			if likesCountBefore != 0 {
				c.JSON(400, gin.H{"error": "invalid cursor: id_before must be > 0"})
				return
			}
			//都为0说明是第一页
			cursor = &LikesCountCursor{
				LikesCount: likesCountBefore,
				ID:         idBefore,
			}
		}
	}
	viewerAccountID, err := jwt.GetAccountID(c)
	if err != nil {
		//0表示没登录的游客
		viewerAccountID = 0
	}
	listLikesCountResponse, err := f.feedService.ListLikesCount(c.Request.Context(), req.Limit, cursor, viewerAccountID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	//确保切片不为nil
	listLikesCountResponse.ContentList = nonNilFeedContentItems(listLikesCountResponse.ContentList)
	c.JSON(200, listLikesCountResponse)
}

// ListByFollowing follower自己的zset:
// member：videoID
// score：发布时间
// 特殊版listLatest
func (f *FeedHandler) ListByFollowing(c *gin.Context) {
	var req ListByFollowingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if req.Limit <= 0 || req.Limit > 50 {
		req.Limit = 10
	}
	//获取登录并发来请求的人的id
	viewerAccountID, err := jwt.GetAccountID(c)

	var latestTime time.Time
	if req.LatestTime > 0 {
		latestTime = time.Unix(req.LatestTime, 0)
	}
	//调用service层,有redis和数据库两种方案
	listByFollowingResponse, err := f.feedService.ListByFollowing(c.Request.Context(), req.Limit, latestTime, viewerAccountID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	listByFollowingResponse.ContentList = nonNilFeedContentItems(listByFollowingResponse.ContentList)
	//ContentList []FeedContentItem `json:"content_list"`
	//NextTime  int64           `json:"next_time"`
	//HasMore   bool            `json:"has_more"`
	c.JSON(200, listByFollowingResponse)
}

// ✅
// 以当前分钟合并六十个分钟桶为一个窗口,一次浏览一直从这个窗口里取视频
// redis不存在时从数据库根据热度时间id查找
func (f *FeedHandler) ListByPopularity(c *gin.Context) {
	var req ListByPopularityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if req.Limit <= 0 || req.Limit > 50 {
		req.Limit = 10
	}
	viewerAccountID, err := jwt.GetAccountID(c)
	if err != nil {
		viewerAccountID = 0
	}

	var latestPopularity int64
	var latestBefore time.Time
	var latestIDBefore uint

	if req.LatestPopularity != nil && *req.LatestPopularity < 0 {
		c.JSON(400, gin.H{"error": "latest_popularity must be >= 0"})
		return
	}

	anyCursor := !req.LatestBefore.IsZero() || req.LatestIDBefore != nil || req.LatestPopularity != nil
	if anyCursor {
		if req.LatestBefore.IsZero() || req.LatestIDBefore == nil || req.LatestPopularity == nil {
			c.JSON(400, gin.H{"error": "latest_before and latest_id_before must be provided together"})
			return
		}
		latestPopularity = *req.LatestPopularity
		latestBefore = req.LatestBefore
		latestIDBefore = *req.LatestIDBefore
	}
	resp, err := f.feedService.ListByPopularity(
		c.Request.Context(),
		req.Limit,
		req.AsOf,
		req.Offset,
		viewerAccountID,
		latestPopularity,
		latestBefore,
		latestIDBefore,
	)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	//确保切片不为nil
	resp.ContentList = nonNilFeedContentItems(resp.ContentList)
	c.JSON(200, resp)
}

// ✅
// tag
func (h *FeedHandler) ListByTag(c *gin.Context) {
	var req struct {
		TagName string `json:"tag_name"`
		Limit   int    `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if req.TagName == "" {
		c.JSON(400, gin.H{"error": "tag_name is required"})
		return
	}
	if req.Limit <= 0 || req.Limit > 50 {
		req.Limit = 10
	}
	viewerAccountID, _ := jwt.GetAccountID(c)
	items, err := h.feedService.ListByTag(c.Request.Context(), req.TagName, req.Limit, viewerAccountID)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	items = nonNilFeedContentItems(items)
	c.JSON(200, gin.H{"content_list": items})
}

// ✅
// 确保切片不为nil
func nonNilFeedContentItems(items []FeedContentItem) []FeedContentItem {
	if items == nil {
		return []FeedContentItem{}
	}
	return items
}
