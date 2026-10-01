package feed

import (
	"context"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/social"
	"time"

	"gorm.io/gorm"
)

type FeedRepository struct {
	db *gorm.DB
}

func NewFeedRepository(db *gorm.DB) *FeedRepository {
	return &FeedRepository{db: db}
}

// ✅
// 根据latestBefore和limit从数据库返回视频切片
func (repo *FeedRepository) ListLatest(ctx context.Context, limit int, latestBefore time.Time) ([]*content.Content, error) {
	var videos []*content.Content
	// Model 非必须，仅显式标明查 videos 表，方便阅读（Find 时会自动推断）
	query := repo.db.WithContext(ctx).Model(&content.Content{}).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Order("create_time DESC")
	//有时间,前面返回过视频
	if !latestBefore.IsZero() {
		query = query.Where("create_time < ?", latestBefore)
	}
	if err := query.Limit(limit).Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// ✅
// 根据点赞数和游标(如果有)查视频,点赞数降序,id降序
func (repo *FeedRepository) ListLikesCountWithCursor(ctx context.Context, limit int, cursor *LikesCountCursor) ([]*content.Content, error) {
	//切片元素只能是指针或值，多一层指针 GORM 就罢工
	var videos []*content.Content
	query := repo.db.WithContext(ctx).Model(&content.Content{}).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Order("likes_count DESC, id DESC")

	if cursor != nil {
		//非第一次查询
		query = query.Where(
			"(likes_count < ?) OR (likes_count = ? AND id < ?)",
			cursor.LikesCount,
			cursor.LikesCount, cursor.ID,
		)
	}

	if err := query.Limit(limit).Find(&videos).Error; err != nil {
		//MySQL 连不上 → driver: bad connection
		//ctx 取消（用户关页面）→ context canceled
		//SQL 语法错（但 GORM 生成的几乎不会）
		return nil, err
	}
	//没查到,videos还是nil
	return videos, nil
}

// ✅
// 根据用户关注和上一页最早视频发布时间返回视频切片
func (repo *FeedRepository) ListByFollowing(ctx context.Context, limit int, viewerAccountID uint, latestBefore time.Time) ([]*content.Content, error) {
	var videos []*content.Content
	query := repo.db.WithContext(ctx).Model(&content.Content{}).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Order("create_time DESC")
	if viewerAccountID > 0 {
		//子查询,查出所有关注的人的id
		followingSubQuery := repo.db.WithContext(ctx).
			Model(&social.Social{}).
			Select("vlogger_id").
			Where("follower_id = ?", viewerAccountID)
		//增加条件,IN IDs
		query = query.Where("author_id IN (?)", followingSubQuery)
	}
	if !latestBefore.IsZero() {
		//如果不是第一次查询,加上时间游标来分页
		query = query.Where("create_time < ?", latestBefore)
	}
	if err := query.Limit(limit).Find(&videos).Error; err != nil {
		//异常情况,数据库断开,用户退出等
		return nil, err
	}
	return videos, nil
}

// ✅
// 根据热度,时间,id返回视频
func (repo *FeedRepository) ListByPopularity(ctx context.Context, limit int, popularityBefore int64, timeBefore time.Time, idBefore uint) ([]*content.Content, error) {
	var videos []*content.Content
	query := repo.db.WithContext(ctx).Model(&content.Content{}).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Order("popularity DESC, create_time DESC, id DESC")

	// 只有当游标完整提供时才加过滤（popularity 允许为 0）
	if !timeBefore.IsZero() && idBefore > 0 {
		query = query.Where(
			"(popularity < ?) OR (popularity = ? AND create_time < ?) OR (popularity = ? AND create_time = ? AND id < ?)",
			popularityBefore,
			popularityBefore, timeBefore,
			popularityBefore, timeBefore, idBefore,
		)
	}

	if err := query.Limit(limit).Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// ✅
// 根据id批量返回视频信息
func (repo *FeedRepository) GetByIDs(ctx context.Context, ids []uint) ([]*content.Content, error) {
	var videos []*content.Content
	if len(ids) == 0 {
		return videos, nil
	}
	if err := repo.db.WithContext(ctx).Model(&content.Content{}).
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return nil, err
	}
	return videos, nil
}

// ✅
// 根据tag返回videos
func (repo *FeedRepository) ListByTag(ctx context.Context, tagName string, limit int) ([]*content.Content, error) {
	var videos []*content.Content
	//连表查询tag对应的video
	err := repo.db.WithContext(ctx).Model(&content.Content{}).Table("contents").
		Preload("Assets", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Joins("JOIN content_tags ON content_tags.content_id = contents.id").
		Joins("JOIN tags ON tags.id = content_tags.tag_id").
		Where("tags.name = ?", tagName).
		Order("contents.create_time desc").
		Limit(limit).
		Find(&videos).Error
	return videos, err
}
