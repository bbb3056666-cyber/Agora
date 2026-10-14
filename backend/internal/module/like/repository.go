package like

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"time"

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
func (r *LikeRepository) ApplyLikeIntent(ctx context.Context, intent rabbitmq.LikeIntent) (err error) {
	if r == nil || r.db == nil || intent.UserID == 0 || intent.ContentID == 0 {
		return errors.New("invalid like intent")
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state LikeState
		//给读加行锁，持续到事务结束，不用担心行不存在，因为只有创建了才会投递
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("account_id = ? AND content_id = ?", intent.UserID, intent.ContentID).
			First(&state).Error; err != nil {
			return err
		}
		if state.DesiredLiked == state.IsLiked {
			// 重复通知、乱序通知，或者期望状态已由另一条通知完成。
			return nil
		}

		//检验视频是否存在
		var videoCount int64
		if err := tx.Model(&content.Content{}).Where("id = ?", intent.ContentID).Count(&videoCount).Error; err != nil {
			return err
		}
		if videoCount == 0 {
			return nil
		}

		transition := planLikeTransition(state, time.Now())

		videoUpdates := map[string]any{
			"likes_count": gorm.Expr("GREATEST(likes_count + ?, 0)", transition.change),
		}
		if transition.popularityChange != 0 {
			videoUpdates["popularity"] = gorm.Expr("GREATEST(popularity + ?, 0)", transition.popularityChange)
		}
		//更新视频点赞和热度
		if err := tx.Model(&content.Content{}).
			Where("id = ?", intent.ContentID).
			UpdateColumns(videoUpdates).Error; err != nil {
			return err
		}
		//更新点赞状态
		if err := tx.Model(&state).Updates(map[string]any{
			"is_liked":                   transition.desired,
			"liked_at":                   transition.likedAt,
			"has_contributed_popularity": transition.hasContributedPopularity,
		}).Error; err != nil {
			return err
		}

		//取消点赞后续没有任务
		if !transition.desired {
			return nil
		}

		//为什么点赞成功后要写表:
		//热度 Redis 失败或通知写库失败时，点赞状态已经提交，后续工作会丢；
		//若重试整个 Worker，又可能重复加热度、重复通知；
		//热度和通知会拖慢 LikeIntentWorker，点赞意图积压；
		//以后新增“点赞后发放积分、风控记录”等，也都得硬塞进 LikeIntentWorker。
		appliedID, err := outbox.NewEventID()
		if err != nil {
			return err
		}
		appliedEvent := rabbitmq.LikeApplied{
			EventID:          appliedID, //下游热度、通知各自用它做“已生效事件”的幂等标识
			UserID:           intent.UserID,
			ContentID:        intent.ContentID,
			Action:           "like",
			Change:           transition.change,
			PopularityChange: transition.popularityChange,
			OccurredAt:       time.Now().UTC(),
		}
		payload, err := json.Marshal(appliedEvent)
		if err != nil {
			return err
		}

		return tx.Create(&outbox.Message{
			EventType: "like_applied",
			Payload:   payload,
			Status:    "pending",
		}).Error
	})
	return err
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
