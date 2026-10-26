package like

import (
	"context"
	"database/sql"
	"errors"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"gorm.io/gorm"
)

// ReconcileLikeCounts 每次仅扫描一小批新建且计数从零开始的内容。
// 版本条件更新避免校准覆盖并发 Worker 刚提交的增量；历史内容默认不参与。
func (r *LikeRepository) ReconcileLikeCounts(ctx context.Context, afterID uint, limit int) (nextID uint, checked, corrected int, err error) {
	if r == nil || r.db == nil || limit <= 0 || limit > 1000 {
		return 0, 0, 0, errors.New("invalid like reconciliation request")
	}
	var ids []uint
	if err = r.db.WithContext(ctx).Model(&content.Content{}).
		Where("likes_reconcile_enabled = ? AND id > ?", true, afterID).
		Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, 0, 0, err
	}
	if len(ids) == 0 {
		return 0, 0, 0, nil
	}
	for _, id := range ids {
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current struct {
				LikesCount        int64
				LikesCountVersion uint64
			}
			if err := tx.Model(&content.Content{}).Select("likes_count", "likes_count_version").Where("id = ? AND likes_reconcile_enabled = ?", id, true).Take(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			checked++
			var actual int64
			if err := tx.Model(&LikeState{}).Where("content_id = ? AND is_liked = ?", id, true).Count(&actual).Error; err != nil {
				return err
			}
			if current.LikesCount == actual {
				return nil
			}
			updated := tx.Model(&content.Content{}).
				Where("id = ? AND likes_reconcile_enabled = ? AND likes_count_version = ?", id, true, current.LikesCountVersion).
				Updates(map[string]any{"likes_count": actual, "likes_count_version": gorm.Expr("likes_count_version + 1")})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected == 1 {
				corrected++
			}
			return nil
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		if err != nil {
			return 0, checked, corrected, err
		}
	}
	if len(ids) == limit {
		return ids[len(ids)-1], checked, corrected, nil
	}
	return 0, checked, corrected, nil
}
