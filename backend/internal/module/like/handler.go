package like

import (
	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/jwt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"

	"github.com/gin-gonic/gin"
)

type LikeHandler struct {
	service *LikeService
}

func NewLikeHandler(service *LikeService) *LikeHandler {
	return &LikeHandler{service: service}
}

func prepareLikeRequest(c *gin.Context) (accountID, videoID uint, ok bool) {
	var req LikeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return 0, 0, false
	}
	if req.ContentID <= 0 {
		c.JSON(400, gin.H{"error": "content_id is required"})
		return 0, 0, false
	}
	//从请求上下文获取点赞用户id
	accountID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return 0, 0, false
	}
	return accountID, req.ContentID, true
}

// ✅
// 插入点赞关系
func (lh *LikeHandler) Like(c *gin.Context) {
	accountID, videoID, ok := prepareLikeRequest(c)
	if !ok {
		return
	}
	if err := lh.service.Like(c.Request.Context(), accountID, videoID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "APPLIED", "message": "like completed"})
}

// ✅
// 取消点赞
func (lh *LikeHandler) Unlike(c *gin.Context) {
	accountID, videoID, ok := prepareLikeRequest(c)
	if !ok {
		return
	}
	if err := lh.service.Unlike(c.Request.Context(), accountID, videoID); err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"status": "APPLIED", "message": "unlike completed"})
}

// ✅
// 查询用户是否点赞过
func (lh *LikeHandler) IsLiked(c *gin.Context) {
	accountID, videoID, ok := prepareLikeRequest(c)
	if !ok {
		return
	}
	isLiked, err := lh.service.IsLiked(c.Request.Context(), videoID, accountID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"is_liked": isLiked})
}

// ✅
// 获取点赞列表
func (lh *LikeHandler) ListMyLikedVideos(c *gin.Context) {
	accountID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}

	videos, err := lh.service.ListLikedContents(c.Request.Context(), accountID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if videos == nil {
		videos = []content.Content{}
	}
	c.JSON(200, videos)
}
