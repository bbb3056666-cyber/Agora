package content

import (
	"context"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"

	"gorm.io/gorm"
)

type ContentRepository struct {
	db *gorm.DB
}

func NewContentRepository(db *gorm.DB) *ContentRepository {
	return &ContentRepository{db: db}
}

func (vr *ContentRepository) CreateContent(ctx context.Context, video *Content) error {
	if err := vr.db.WithContext(ctx).Create(video).Error; err != nil {
		return err
	}
	return nil
}

func (vr *ContentRepository) CreateMsg(ctx context.Context, Msg *outbox.Message) error {
	if err := vr.db.WithContext(ctx).Create(Msg).Error; err != nil {
		return err
	}
	return nil
}

// 删除视频
func (vr *ContentRepository) DeleteContent(ctx context.Context, id uint) error {
	return vr.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("content_id = ?", id).Delete(&ContentAsset{}).Error; err != nil {
			return err
		}
		if err := tx.Where("content_id = ?", id).Delete(&ContentTag{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&Content{}).Error
	})
}

// ✅
// 根据作者id按时间顺序返回最新两百条视频
func (vr *ContentRepository) ListByAuthorID(ctx context.Context, authorID int64) ([]Content, error) {
	var videos []Content
	if err := vr.db.WithContext(ctx).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Where("author_id = ?", authorID).
		Order("create_time desc").
		Limit(200).
		Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// ✅
// 由视频id获取视频对象
func (vr *ContentRepository) GetByID(ctx context.Context, id uint) (*Content, error) {
	var video Content
	if err := vr.db.WithContext(ctx).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		First(&video, id).Error; err != nil {
		return nil, err
	}
	return &video, nil
}

func (vr *ContentRepository) UpdateLikesCount(ctx context.Context, id uint, likesCount int64) error {
	if err := vr.db.WithContext(ctx).Model(&Content{}).
		Where("id = ?", id).
		Update("likes_count", likesCount).Error; err != nil {
		return err
	}
	return nil
}

// ✅
// 根据视频id检查视频是否存在
func (vr *ContentRepository) IsExist(ctx context.Context, id uint) (bool, error) {
	var video Content
	if err := vr.db.WithContext(ctx).First(&video, id).Error; err != nil {
		//未找到记录
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	//存在
	return true, nil
}

func (vr *ContentRepository) UpdatePopularity(ctx context.Context, id uint, change int64) error {
	if err := vr.db.WithContext(ctx).Model(&Content{}).
		Where("id = ?", id).
		Update("popularity", gorm.Expr("popularity + ?", change)).Error; err != nil {
		return err
	}
	return nil
}

// 将视频点赞数+change
func (vr *ContentRepository) ChangeLikesCount(ctx context.Context, id uint, change int64) error {
	if err := vr.db.WithContext(ctx).Model(&Content{}).
		Where("id = ?", id).
		UpdateColumn("likes_count", gorm.Expr("GREATEST(likes_count + ?, 0)", change)).Error; err != nil {
		return err
	}
	return nil
}

// 将视频热度+change
func (vr *ContentRepository) ChangePopularity(ctx context.Context, id uint, change int64) error {
	if err := vr.db.WithContext(ctx).Model(&Content{}).
		Where("id = ?", id).
		UpdateColumn("popularity", gorm.Expr("GREATEST(popularity + ?, 0)", change)).Error; err != nil {
		return err
	}
	return nil
}

// 根据用户id获取视频总数
func (vr *ContentRepository) CountByAuthor(ctx context.Context, authorID uint) (int64, error) {
	var count int64
	if err := vr.db.WithContext(ctx).Model(&Content{}).Where("author_id = ?", authorID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// 根据用户id获取视频点赞总数
func (vr *ContentRepository) TotalLikesByAuthor(ctx context.Context, authorID uint) (int64, error) {
	var total int64
	if err := vr.db.WithContext(ctx).Model(&Content{}).Where("author_id = ?", authorID).Select("COALESCE(SUM(likes_count), 0)").Scan(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}
