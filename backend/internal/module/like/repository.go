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

// SubmitLikeIntent 原子分配同一点赞关系的版本，并写入待投递意图 Outbox。
// 每一个点赞请求都会生成一个 intent。
// API 在事务提交后即可返回 202；真正的点赞状态由 Worker 后续裁决。
func (r *LikeRepository) SubmitLikeIntent(ctx context.Context, userID, videoID uint, action string) error {
	if r == nil || r.db == nil || userID == 0 || videoID == 0 || (action != "like" && action != "unlike") {
		return errors.New("invalid like intent")
	}
	//32位随机字符串
	eventID, err := outbox.NewEventID()
	if err != nil {
		return err
	}

	//插入/更新like_state，插入outbox_msgs
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 新关系从版本 1 开始；唯一索引冲突时由数据库在同一条 Upsert（冲突则更新，不冲突则插入） 中原子递增，
		// 因此并发请求不会拿到相同版本。
		now := time.Now()
		state := LikeState{
			AccountID:      userID,
			ContentID:      videoID,
			IsLiked:        false,
			Version:        1,
			AppliedVersion: 0,
			UpdatedAt:      now,
		}
		//Upsert（不冲突则插入，冲突则更新），并且拿到行锁
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "account_id"},
				{Name: "content_id"},
			},
			DoUpdates: clause.Assignments(map[string]any{
				"version":    gorm.Expr("version + ?", 1),
				"updated_at": now,
			}),
		}).Create(&state).Error; err != nil {
			return err
		}

		// 此时行锁没释放，其他事务无法修改这一行，可以防止同一用户对同一视频的并发操作
		// Upsert 后只读取本次命令需要的真实版本号。
		var version uint64
		if err := tx.Model(&LikeState{}).
			Select("version").
			Where("account_id = ? AND content_id = ?", userID, videoID).
			Row().Scan(&version); err != nil {
			return err
		}

		//点赞意图
		intent := rabbitmq.LikeIntent{
			EventID:    eventID, //为本次请求专门生成的id
			UserID:     userID,
			ContentID:  videoID,
			Action:     action,
			Version:    version,
			OccurredAt: time.Now().UTC(),
		}
		//序列化成json字符串字节
		payload, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		//插入到发件箱里
		return tx.Create(&outbox.Message{
			EventType: "like_intent",
			Payload:   payload,
			Status:    "pending",
		}).Error
	})
	return err
}

// ApplyLikeIntent 只接受比 AppliedVersion 更新的意图。状态真的变化时，
// 点赞关系、视频计数和 like_applied Outbox 在同一事务提交。
func (r *LikeRepository) ApplyLikeIntent(ctx context.Context, intent rabbitmq.LikeIntent) (err error) {
	if r == nil || r.db == nil || intent.EventID == "" || intent.UserID == 0 || intent.ContentID == 0 || intent.Version == 0 {
		return errors.New("invalid like intent")
	}
	desired := intent.Action == "like"
	if !desired && intent.Action != "unlike" {
		return errors.New("invalid like action")
	}

	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state LikeState
		//给读加行锁，持续到事务结束，不用担心行不存在，因为只有创建了才会投递
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("account_id = ? AND content_id = ?", intent.UserID, intent.ContentID).
			First(&state).Error; err != nil {
			return err
		}
		if intent.Version <= state.AppliedVersion {
			// 重投或迟到旧命令。
			return nil
		}

		//检验视频是否存在
		var videoCount int64
		if err := tx.Model(&content.Content{}).Where("id = ?", intent.ContentID).Count(&videoCount).Error; err != nil {
			return err
		}
		if videoCount == 0 {
			//视频不存在
			return tx.Model(&state).Update("applied_version", intent.Version).Error
		}

		//是否已经是想要的状态，已经是就不用修改了
		if desired == state.IsLiked {
			return tx.Model(&state).Update("applied_version", intent.Version).Error
		}

		//真的点赞或取消了
		change := int64(-1)
		popularityChange := int64(0)
		//取消点赞后清空时间
		likedAt := any(nil)
		if desired {
			change = 1
			likedAt = time.Now()
			if !state.HasContributedPopularity {
				popularityChange = likePopularityChange
			}
		}

		videoUpdates := map[string]any{
			"likes_count": gorm.Expr("GREATEST(likes_count + ?, 0)", change),
		}
		if popularityChange != 0 {
			videoUpdates["popularity"] = gorm.Expr("GREATEST(popularity + ?, 0)", popularityChange)
		}
		//更新视频点赞和热度
		if err := tx.Model(&content.Content{}).
			Where("id = ?", intent.ContentID).
			UpdateColumns(videoUpdates).Error; err != nil {
			return err
		}
		//更新点赞状态
		if err := tx.Model(&state).Updates(map[string]any{
			"is_liked":                   desired,
			"liked_at":                   likedAt,
			"has_contributed_popularity": state.HasContributedPopularity || desired,
			"applied_version":            intent.Version,
		}).Error; err != nil {
			return err
		}

		//取消点赞后续没有任务
		if !desired {
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
			EventID:          appliedID,      //下游热度、通知各自用它做“已生效事件”的幂等标识
			IntentID:         intent.EventID, //可以追溯这条已生效事件来自哪条用户请求
			UserID:           intent.UserID,
			ContentID:        intent.ContentID,
			Action:           intent.Action,
			Change:           change,
			PopularityChange: popularityChange,
			Version:          intent.Version,
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

// IsLiked 从 LikeState 读取当前点赞状态。
func (r *LikeRepository) IsLiked(ctx context.Context, videoID, accountID uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&LikeState{}).
		Where("content_id = ? AND account_id = ? AND is_liked = ?", videoID, accountID, true).
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
		Where("content_id IN ? AND account_id = ? AND is_liked = ?", videoIDs, accountID, true).
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
		Where("like_states.account_id = ? AND like_states.is_liked = ?", accountID, true).
		Order("like_states.liked_at desc").
		Limit(200).
		Find(&videos).Error
	if err != nil {
		return nil, err
	}
	return videos, nil
}
