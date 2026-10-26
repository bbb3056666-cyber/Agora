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

// SubmitLikeSync is a benchmark-only baseline: the HTTP transaction applies
// the relation, counter, and durable downstream event before returning.
func (r *LikeRepository) SubmitLikeSync(ctx context.Context, userID, contentID uint, desired bool) error {
	if r == nil || r.db == nil || userID == 0 || contentID == 0 {
		return errors.New("invalid like request")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state LikeState
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("account_id = ? AND content_id = ?", userID, contentID).First(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state = LikeState{AccountID: userID, ContentID: contentID}
			if err := tx.Create(&state).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if state.IsLiked == desired {
			return nil
		}
		state.DesiredLiked = desired
		transition := planLikeTransition(state, time.Now())
		if err := tx.Model(&state).Updates(map[string]any{
			"desired_liked":              desired,
			"is_liked":                   desired,
			"liked_at":                   transition.likedAt,
			"has_contributed_popularity": transition.hasContributedPopularity,
		}).Error; err != nil {
			return err
		}
		if desired {
			eventID, err := outbox.NewEventID()
			if err != nil {
				return err
			}
			payload, err := json.Marshal(rabbitmq.LikeApplied{
				EventID: eventID, UserID: userID, ContentID: contentID,
				Action: "like", Change: transition.change,
				PopularityChange: transition.popularityChange, OccurredAt: time.Now().UTC(),
			})
			if err != nil {
				return err
			}
			if err := tx.Create(&outbox.Message{
				EventType: "like_applied", Payload: payload, Status: "pending",
			}).Error; err != nil {
				return err
			}
		}
		updates := map[string]any{
			"likes_count":         gorm.Expr("GREATEST(likes_count + ?, 0)", transition.change),
			"likes_count_version": gorm.Expr("likes_count_version + 1"),
		}
		if transition.popularityChange != 0 {
			updates["popularity"] = gorm.Expr("GREATEST(popularity + ?, 0)", transition.popularityChange)
		}
		result := tx.Model(&content.Content{}).Where("id = ?", contentID).UpdateColumns(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errLikeContentMissing
		}
		return nil
	})
}
