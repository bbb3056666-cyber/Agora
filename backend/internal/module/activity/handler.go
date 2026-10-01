package activity

import (
	"errors"

	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/jwt"

	"github.com/gin-gonic/gin"
)

type ActivityHandler struct {
	service *ActivityService
}

func NewActivityHandler(service *ActivityService) *ActivityHandler {
	return &ActivityHandler{service: service}
}

// 在一个视频里创建一个活动
func (h *ActivityHandler) CreateActivity(c *gin.Context) {
	var req CreateActivityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	creatorID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(401, gin.H{"error": err.Error()})
		return
	}
	activity, err := h.service.CreateActivity(c.Request.Context(), creatorID, req)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidActivity):
			c.JSON(400, gin.H{"error": err.Error()})
		case errors.Is(err, ErrContentNotFound):
			c.JSON(404, gin.H{"error": err.Error()})
		case errors.Is(err, ErrNotContentOwner):
			c.JSON(403, gin.H{"error": err.Error()})
		default:
			c.JSON(500, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(201, activity)
}

// 根据视频id找所有活动
func (h *ActivityHandler) ListByContentID(c *gin.Context) {
	var req struct {
		ContentID uint `json:"content_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ContentID == 0 {
		c.JSON(400, gin.H{"error": "content_id is required"})
		return
	}
	activities, err := h.service.ListByContentID(c.Request.Context(), req.ContentID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if activities == nil {
		activities = []ContentActivity{}
	}
	c.JSON(200, activities)
}

// 浏览者报名活动
func (h *ActivityHandler) Register(c *gin.Context) {
	var req ActivityIDRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.ActivityID == 0 {
		c.JSON(400, gin.H{"error": "activity_id is required"})
		return
	}
	accountID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(401, gin.H{"error": err.Error()})
		return
	}
	if err := h.service.Register(c.Request.Context(), req.ActivityID, accountID); err != nil {
		switch {
		case errors.Is(err, ErrQuotaExhausted), errors.Is(err, ErrAlreadyRegistered):
			c.JSON(409, gin.H{"error": err.Error()})
		case errors.Is(err, ErrActivityNotStarted), errors.Is(err, ErrActivityEnded), errors.Is(err, ErrActivityCacheMiss):
			c.JSON(400, gin.H{"error": err.Error()})
		case errors.Is(err, ErrActivityMQUnavailable):
			c.JSON(503, gin.H{"error": err.Error()})
		default:
			c.JSON(500, gin.H{"error": err.Error()})
		}
		return
	}
	c.JSON(202, gin.H{"status": "PENDING", "message": "registration is being processed"})
}

// RegistrationResult 查询当前登录用户对某个活动的报名结果。
func (h *ActivityHandler) RegistrationResult(c *gin.Context) {
	var req ActivityIDRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.ActivityID == 0 {
		c.JSON(400, gin.H{"error": "activity_id is required"})
		return
	}
	accountID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(401, gin.H{"error": err.Error()})
		return
	}
	result, err := h.service.RegistrationResult(c.Request.Context(), req.ActivityID, accountID)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	//NOT_REGISTERED：从未报名。
	//PENDING：Redis 预报名已成功，等待 MQ Worker 落 MySQL。
	//SUCCESS：MySQL 报名记录已创建成功。
	//FAILED：Worker 多次失败后已释放名额
	c.JSON(200, gin.H{"status": result})
}
