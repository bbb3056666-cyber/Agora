package activity

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

var ErrOrderDuplicated = errors.New("activity order already exists")

type ActivityRepository struct{ db *gorm.DB }

func NewActivityRepository(db *gorm.DB) *ActivityRepository { return &ActivityRepository{db: db} }

// CreateActivity 插入一条活动
func (r *ActivityRepository) CreateActivity(ctx context.Context, activity *ContentActivity) error {
	return r.db.WithContext(ctx).Create(activity).Error
}

func (r *ActivityRepository) FindActivity(ctx context.Context, id uint) (*ContentActivity, error) {
	var activity ContentActivity
	if err := r.db.WithContext(ctx).First(&activity, id).Error; err != nil {
		return nil, err
	}
	return &activity, nil
}

func (r *ActivityRepository) ListActivities(ctx context.Context) ([]ContentActivity, error) {
	var activities []ContentActivity
	err := r.db.WithContext(ctx).Order("id DESC").Limit(100).Find(&activities).Error
	return activities, err
}

// 根据视频id找所有活动
func (r *ActivityRepository) ListByContentID(ctx context.Context, videoID uint) ([]ContentActivity, error) {
	var activities []ContentActivity
	err := r.db.WithContext(ctx).Where("content_id = ?", videoID).Order("id DESC").Find(&activities).Error
	return activities, err
}

// CreateOrderAndDeduct 在同一事务里先占订单唯一键，再扣 MySQL 库存。
// 消息重复时唯一键冲突，不会再扣一次库存。
func (r *ActivityRepository) CreateOrderAndDeduct(ctx context.Context, eventID string, activityID, userID uint) error {
	//开启事务,报名记录和库存必须同时成功
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		order := &ActivityRegistration{
			ActivityID: activityID,
			AccountID:  userID,
			EventID:    eventID,
		}
		//插入报名记录
		if err := tx.Create(order).Error; err != nil {
			var mysqlErr *mysql.MySQLError
			if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
				//重复报名
				return ErrOrderDuplicated
			}
			return err
		}
		//修改库存
		result := tx.Model(&ContentActivity{}).
			Where("id = ? AND remaining_quota > 0", activityID).
			UpdateColumn("remaining_quota", gorm.Expr("remaining_quota - 1"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("mysql stock exhausted or activity not found")
		}
		return nil
	})
}

// 查询是否有某个报名
func (r *ActivityRepository) HasOrder(ctx context.Context, activityID, userID uint) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&ActivityRegistration{}).Where("activity_id = ? AND account_id = ?", activityID, userID).Count(&count).Error
	return count > 0, err
}
