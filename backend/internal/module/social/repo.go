package social

import (
	"context"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"

	"gorm.io/gorm"
)

type SocialRepository struct {
	db *gorm.DB
}

func NewSocialRepository(db *gorm.DB) *SocialRepository {
	return &SocialRepository{db: db}
}

// Follow ✅
// 插入关注记录
func (r *SocialRepository) Follow(ctx context.Context, social *Social) error {
	return r.db.WithContext(ctx).Create(social).Error
}

// FollowAndIncrementFollowerCount 在一个事务内维护关注关系与冗余粉丝计数。
// 先插入 socials；插入失败（例如并发重复关注触发唯一索引）时，后续计数不会执行。
// 任一步失败都会回滚，避免“已关注但粉丝数未加”或相反的脏数据。
func (r *SocialRepository) FollowAndIncrementFollowerCount(ctx context.Context, social *Social) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(social).Error; err != nil {
			return err
		}

		result := tx.Model(&account.Account{}).
			Where("id = ?", social.VloggerID).
			UpdateColumn("follower_count", gorm.Expr("follower_count + ?", 1))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// UnfollowAndDecrementFollowerCount 在一个事务内删除关注关系并更新冗余粉丝计数。
// 只有确实删除了一条 socials 记录，才允许粉丝数减一，避免并发重复取关造成计数错误。
func (r *SocialRepository) UnfollowAndDecrementFollowerCount(ctx context.Context, social *Social) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("follower_id = ? AND vlogger_id = ?", social.FollowerID, social.VloggerID).
			Delete(&Social{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		result = tx.Model(&account.Account{}).
			Where("id = ?", social.VloggerID).
			UpdateColumn("follower_count", gorm.Expr("GREATEST(follower_count - ?, 0)", 1))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// GetAllFollowers ✅
// 返回粉丝列表:先根据vloggerID获取关注关系,再获取粉丝IDs,再批量查询粉丝
func (r *SocialRepository) GetAllFollowers(ctx context.Context, VloggerID uint) ([]*account.Account, error) {
	var relations []Social
	//如果没查到,不会创建切片,relations为nil
	if err := r.db.WithContext(ctx).
		Model(&Social{}).
		Where("vlogger_id = ?", VloggerID).
		Limit(200).
		//引用类型也得传值,才能修改len长度,cap容量
		Find(&relations).Error; err != nil {
		return nil, err
	}

	followerIDs := make([]uint, 0, len(relations))
	for _, rel := range relations {
		followerIDs = append(followerIDs, rel.FollowerID)
	}
	if len(followerIDs) == 0 {
		return []*account.Account{}, nil
	}

	//gorm可以识别值切片和一级地址切片
	//可能查不到,返回nil
	var followers []*account.Account
	if err := r.db.WithContext(ctx).
		Model(&account.Account{}).
		Where("id IN ?", followerIDs).
		Find(&followers).Error; err != nil {
		return nil, err
	}
	return followers, nil
}

// ListFollowerIDsAfterID 使用关注关系的主键作游标，分页返回某作者的一批粉丝 ID。
// 不查询 Account 完整字段，Timeline fan-out 只需要 followerID 拼 Redis Key。
func (r *SocialRepository) ListFollowerIDsAfterID(ctx context.Context, vloggerID, afterRelationID uint, limit int) ([]uint, uint, error) {
	if limit <= 0 {
		return nil, afterRelationID, nil
	}

	var relations []Social
	if err := r.db.WithContext(ctx).
		Select("id", "follower_id").
		Where("vlogger_id = ? AND id > ?", vloggerID, afterRelationID).
		Order("id ASC").
		Limit(limit).
		Find(&relations).Error; err != nil {
		return nil, afterRelationID, err
	}

	followerIDs := make([]uint, 0, len(relations))
	nextRelationID := afterRelationID
	for _, relation := range relations {
		followerIDs = append(followerIDs, relation.FollowerID)
		nextRelationID = relation.ID
	}
	return followerIDs, nextRelationID, nil
}

// GetAllVloggers ✅
// 返回关注列表,先根据followerID获取关注关系,再获取关注者IDs,再批量查询被关注者
func (r *SocialRepository) GetAllVloggers(ctx context.Context, FollowerID uint) ([]*account.Account, error) {
	var relations []Social
	if err := r.db.WithContext(ctx).
		Model(&Social{}).
		Where("follower_id = ?", FollowerID).
		Limit(200).
		Find(&relations).Error; err != nil {
		return nil, err
	}

	vloggerIDs := make([]uint, 0, len(relations))
	for _, rel := range relations {
		vloggerIDs = append(vloggerIDs, rel.VloggerID)
	}
	if len(vloggerIDs) == 0 {
		return []*account.Account{}, nil
	}

	var vloggers []*account.Account
	if err := r.db.WithContext(ctx).
		Model(&account.Account{}).
		Where("id IN ?", vloggerIDs).
		Find(&vloggers).Error; err != nil {
		return nil, err
	}
	return vloggers, nil
}

// IsFollowed ✅
// 查询是否有某条关注记录
func (r *SocialRepository) IsFollowed(ctx context.Context, social *Social) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&Social{}).
		Where("follower_id = ? AND vlogger_id = ?", social.FollowerID, social.VloggerID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// CountVloggers ✅
// 查询关注数
func (r *SocialRepository) CountVloggers(ctx context.Context, followerID uint) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&Social{}).Where("follower_id = ?", followerID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// GetRecentVideosByAuthor 根据作者id获取视频
func (r *SocialRepository) GetRecentVideosByAuthor(ctx context.Context, authorID uint, limit int) ([]*content.Content, error) {
	var videos []*content.Content
	// 默认推200条，最多500条，避免大V查太多卡
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	err := r.db.WithContext(ctx).
		Model(&content.Content{}).
		Where("author_id = ?", authorID).
		Order("create_time DESC").
		Limit(limit).
		Find(&videos).Error
	return videos, err
}

func (r *SocialRepository) ListFollowingIDs(ctx context.Context, followerID uint) ([]uint, error) {
	var vloggerIDs []uint
	if err := r.db.WithContext(ctx).
		Model(&Social{}).
		Where("follower_id = ?", followerID).
		Pluck("vlogger_id", &vloggerIDs).Error; err != nil {
		return nil, err
	}
	return vloggerIDs, nil
}
