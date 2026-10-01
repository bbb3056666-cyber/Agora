package message

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/jwt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Repository struct{ db *gorm.DB }
type Service struct{ repo *Repository }
type Handler struct{ service *Service }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }
func NewService(repo *Repository) *Service  { return &Service{repo: repo} }
func NewHandler(service *Service) *Handler  { return &Handler{service: service} }

func (r *Repository) AutoMigrate(ctx context.Context) error {
	return r.db.WithContext(ctx).AutoMigrate(&Message{})
}

// ✅
// 发私信
func (h *Handler) Send(c *gin.Context) {
	fromID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	//绑定数据
	var req SendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	//校验对象和信息合法
	if req.ToID == 0 || strings.TrimSpace(req.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "to_id and content are required"})
		return
	}
	//构造私信记录
	m := &Message{FromID: fromID, ToID: req.ToID, Content: req.Content}
	//调用service向数据库插入一条记录
	if err := h.service.Send(c.Request.Context(), m); err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m)
}

// ✅
func (h *Handler) List(c *gin.Context) {
	var req ListRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.PeerID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "peer_id is required"})
		return
	}
	userID, err := jwt.GetAccountID(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	msgs, err := h.service.List(c.Request.Context(), userID, req.PeerID, 50)
	if err != nil {
		c.JSON(apierror.ClassifyHTTPStatus(err), gin.H{"error": err.Error()})
		return
	}
	if msgs == nil {
		msgs = []Message{}
	}
	c.JSON(http.StatusOK, ListResponse{Messages: msgs})
}

// ✅
func (s *Service) Send(ctx context.Context, m *Message) error {
	return s.repo.Send(ctx, m)
}

// ✅
func (s *Service) List(ctx context.Context, userID, peerID uint, limit int) ([]Message, error) {
	return s.repo.List(ctx, userID, peerID, limit)
}

// ✅
// 插入记录
func (r *Repository) Send(ctx context.Context, m *Message) error {
	//去除首尾空格
	m.Content = strings.TrimSpace(m.Content)
	//信息为空
	if m.Content == "" {
		return errors.New("content is required")
	}
	m.CreatedAt = time.Now()
	//向数据库插入一条私信记录
	return r.db.WithContext(ctx).Create(m).Error
}

// ✅
// 获取两个账户间的私信
func (r *Repository) List(ctx context.Context, userID, peerID uint, limit int) ([]Message, error) {
	var msgs []Message
	err := r.db.WithContext(ctx).
		Where("(from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?)", userID, peerID, peerID, userID).
		Order("created_at desc").
		Limit(limit).
		Find(&msgs).Error
	return msgs, err
}
