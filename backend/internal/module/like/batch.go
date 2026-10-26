package like

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errLikeContentMissing = errors.New("like content is missing")

const MaxLikeIntentBatchSize = 32

// ApplyLikeIntents 只处理同一内容的有界批次，按真实状态变化汇总计数。
func (r *LikeRepository) ApplyLikeIntents(ctx context.Context, intents []rabbitmq.LikeIntent) error {
	if len(intents) == 0 {
		return nil
	}
	if len(intents) > MaxLikeIntentBatchSize {
		return errors.New("like intent batch is too large")
	}
	contentID := intents[0].ContentID
	if r == nil || r.db == nil || contentID == 0 {
		return errors.New("invalid like intent")
	}
	users := make(map[uint]struct{}, len(intents))
	for _, intent := range intents {
		if intent.UserID == 0 || intent.ContentID != contentID {
			return errors.New("like intent batch must contain one content and valid users")
		}
		users[intent.UserID] = struct{}{}
	}
	userIDs := make([]uint, 0, len(users))
	for userID := range users {
		userIDs = append(userIDs, userID)
	}
	slices.Sort(userIDs)

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var totalChange, totalPopularity int64
		var states []LikeState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("content_id = ? AND account_id IN ?", contentID, userIDs).
			Order("account_id ASC").Find(&states).Error; err != nil {
			return err
		}
		if len(states) != len(userIDs) {
			return errors.New("like intent state is missing")
		}
		changedIDs := make([]uint, 0, len(states))
		events := make([]outbox.Message, 0, len(userIDs))
		now := time.Now()
		for _, state := range states {
			if state.DesiredLiked == state.IsLiked {
				continue
			}
			changedIDs = append(changedIDs, state.ID)
			transition := planLikeTransition(state, now)
			totalChange += transition.change
			totalPopularity += transition.popularityChange
			if !transition.desired {
				continue
			}
			appliedID, err := outbox.NewEventID()
			if err != nil {
				return err
			}
			payload, err := json.Marshal(rabbitmq.LikeApplied{
				EventID: appliedID, UserID: state.AccountID, ContentID: contentID,
				Action: "like", Change: transition.change,
				PopularityChange: transition.popularityChange, OccurredAt: now.UTC(),
			})
			if err != nil {
				return err
			}
			events = append(events, outbox.Message{EventType: "like_applied", Payload: payload, Status: "pending"})
		}
		if len(changedIDs) == 0 {
			return nil
		}
		updatedStates := tx.Model(&LikeState{}).Where("id IN ?", changedIDs).UpdateColumns(map[string]any{
			"is_liked":                   gorm.Expr("desired_liked"),
			"liked_at":                   gorm.Expr("CASE WHEN desired_liked THEN ? ELSE NULL END", now),
			"has_contributed_popularity": gorm.Expr("has_contributed_popularity OR desired_liked"),
			"updated_at":                 now,
		})
		if updatedStates.Error != nil {
			return updatedStates.Error
		}
		if updatedStates.RowsAffected != int64(len(changedIDs)) {
			return errors.New("like intent state changed during batch")
		}
		if len(events) > 0 {
			if err := tx.Create(&events).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{"likes_count": gorm.Expr("GREATEST(likes_count + ?, 0)", totalChange)}
		updates["likes_count_version"] = gorm.Expr("likes_count_version + 1")
		if totalPopularity != 0 {
			updates["popularity"] = gorm.Expr("GREATEST(popularity + ?, 0)", totalPopularity)
		}
		updated := tx.Model(&content.Content{}).Where("id = ?", contentID).UpdateColumns(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return errLikeContentMissing
		}
		return nil
	})
	if errors.Is(err, errLikeContentMissing) {
		return nil
	}
	return err
}
