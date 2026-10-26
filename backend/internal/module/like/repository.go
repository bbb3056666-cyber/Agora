package like

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const likePopularityChange int64 = 1

type LikeRepository struct {
	db *gorm.DB
}

func NewLikeRepository(db *gorm.DB) *LikeRepository {
	return &LikeRepository{db: db}
}

// SubmitLikeIntent 在一个 MySQL 事务内记录最新期望状态并写入 Outbox 通知。
// API 提交事务后即可返回 202；Worker 收到通知后读取数据库中的最新状态进行对账。
func (r *LikeRepository) SubmitLikeIntent(ctx context.Context, userID, videoID uint, desired bool) error {
	if r == nil || r.db == nil || userID == 0 || videoID == 0 {
		return errors.New("invalid like intent")
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		state := LikeState{
			AccountID:    userID,
			ContentID:    videoID,
			IsLiked:      false,
			DesiredLiked: desired,
			UpdatedAt:    now,
		}
		// 同一用户和内容的并发请求由唯一键对应的行锁串行化，最后提交的意图生效。
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "account_id"},
				{Name: "content_id"},
			},
			DoUpdates: clause.Assignments(map[string]any{
				"desired_liked": desired,
				"updated_at":    now,
			}),
		}).Create(&state).Error; err != nil {
			return err
		}

		// 消息只是“需要对账”的通知，不携带可能过期的 like/unlike 状态。
		intent := rabbitmq.LikeIntent{
			UserID:    userID,
			ContentID: videoID,
		}
		payload, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		return tx.Create(&outbox.Message{
			EventType: "like_intent",
			Payload:   payload,
			Status:    "pending",
		}).Error
	})
}

// ApplyLikeIntent 锁定点赞关系并把实际状态向最新期望状态收敛。
// 状态真的变化时，点赞关系、内容计数和 like_applied Outbox 在同一事务提交。
func (r *LikeRepository) ApplyLikeIntent(ctx context.Context, intent rabbitmq.LikeIntent) error {
	return r.ApplyLikeIntents(ctx, []rabbitmq.LikeIntent{intent})
}

type likeTransition struct {
	desired                  bool
	change                   int64
	popularityChange         int64
	likedAt                  *time.Time
	hasContributedPopularity bool
}

func planLikeTransition(state LikeState, now time.Time) likeTransition {
	transition := likeTransition{
		desired:                  state.DesiredLiked,
		change:                   -1,
		hasContributedPopularity: state.HasContributedPopularity,
	}
	if !state.DesiredLiked {
		return transition
	}

	transition.change = 1
	transition.likedAt = &now
	transition.hasContributedPopularity = true
	if !state.HasContributedPopularity {
		transition.popularityChange = likePopularityChange
	}
	return transition
}

// IsLiked 返回 API 最近接受的点赞意图，让前端无需等待异步 Worker 即可看到操作结果。
func (r *LikeRepository) IsLiked(ctx context.Context, videoID, accountID uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&LikeState{}).
		Where("content_id = ? AND account_id = ? AND desired_liked = ?", videoID, accountID, true).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ✅
// 返回map[视频id]是否点赞
func (r *LikeRepository) BatchGetLiked(ctx context.Context, videoIDs []uint, accountID uint) (map[uint]bool, error) {
	likeMap := make(map[uint]bool)
	if len(videoIDs) == 0 {
		return likeMap, nil
	}
	if accountID == 0 {
		//说明为游客,不可能点赞,直接返回
		return likeMap, nil
	}
	var states []LikeState
	err := r.db.WithContext(ctx).Model(&LikeState{}).
		Where("content_id IN ? AND account_id = ? AND desired_liked = ?", videoIDs, accountID, true).
		Find(&states).Error
	if err != nil {
		return nil, err
	}
	for _, state := range states {
		likeMap[state.ContentID] = true
	}
	return likeMap, nil
}

// ✅
// 获取点赞列表,一次最多两百条
func (r *LikeRepository) ListLikedContents(ctx context.Context, accountID uint) ([]content.Content, error) {
	var videos []content.Content
	if accountID == 0 {
		return videos, nil
	}
	err := r.db.WithContext(ctx).
		Model(&content.Content{}).
		Joins("JOIN like_states ON like_states.content_id = contents.id").
		Where("like_states.account_id = ? AND like_states.desired_liked = ?", accountID, true).
		Order("like_states.liked_at desc").
		Limit(200).
		Find(&videos).Error
	if err != nil {
		return nil, err
	}
	return videos, nil
}
