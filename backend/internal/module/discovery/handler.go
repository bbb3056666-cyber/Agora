package discovery

import (
	"net/http"
	"strings"

	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	db *gorm.DB
}

func NewHandler(db *gorm.DB) *Handler {
	return &Handler{db: db}
}

func (h *Handler) Search(c *gin.Context) {
	var req SearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}

	query := strings.TrimSpace(req.Query)
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required"})
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 30 {
		limit = 12
	}
	pattern := "%" + query + "%"

	var accounts []account.Account
	if err := h.db.WithContext(c.Request.Context()).
		Select("id", "username", "avatar_url", "bio").
		Where("username LIKE ? OR bio LIKE ?", pattern, pattern).
		Order("follower_count DESC, id DESC").
		Limit(limit).
		Find(&accounts).Error; err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}

	publicAccounts := make([]account.FindByIDResponse, 0, len(accounts))
	for _, item := range accounts {
		publicAccounts = append(publicAccounts, account.FindByIDResponse{
			ID: item.ID, Username: item.Username, AvatarURL: item.AvatarURL, Bio: item.Bio,
		})
	}

	var contents []content.Content
	if err := h.db.WithContext(c.Request.Context()).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Where("title LIKE ? OR description LIKE ? OR body LIKE ?", pattern, pattern, pattern).
		Order("create_time DESC").
		Limit(limit).
		Find(&contents).Error; err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, SearchResponse{Accounts: publicAccounts, Contents: contents})
}
