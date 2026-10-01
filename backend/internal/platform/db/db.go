package db

import (
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/worker"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/activity"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/comment"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/like"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/message"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/social"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// NewDB ✅
// 连接数据库,返回数据库句柄,用来操作所有表的增删改查
func NewDB(dbcfg config.DatabaseConfig, poolCfg config.DatabasePoolConfig) (*gorm.DB, error) {
	//拼接配置字符串
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		dbcfg.User, dbcfg.Password, dbcfg.Host, dbcfg.Port, dbcfg.DBName)

	//连接数据库,获得句柄
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	// GORM wraps database/sql. Configure the underlying pool so connections are
	// reused instead of repeatedly establishing and closing MySQL TCP sessions.
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if poolCfg.MaxOpenConns <= 0 {
		poolCfg = config.DefaultDatabasePoolConfig()
	}
	if poolCfg.MaxIdleConns < 0 {
		poolCfg.MaxIdleConns = 0
	}
	if poolCfg.MaxIdleConns > poolCfg.MaxOpenConns {
		poolCfg.MaxIdleConns = poolCfg.MaxOpenConns
	}
	sqlDB.SetMaxOpenConns(poolCfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(poolCfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(poolCfg.ConnMaxLifetimeSeconds) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(poolCfg.ConnMaxIdleTimeSeconds) * time.Second)
	return db, nil
}

// AutoMigrate ✅
// 根据 Go struct 定义自动在 MySQL 建表。表已存在则只新增列，不删列不改类型。
func AutoMigrate(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&account.Account{}, &account.RefreshToken{}, &content.Content{}, &content.ContentAsset{}, &like.LikeState{}, &comment.Comment{}, &comment.CommentPopularityState{},
		&social.Social{}, &outbox.Message{}, &content.Tag{}, &content.ContentTag{},
		&message.Message{}, &worker.Notification{},
		&activity.ContentActivity{}, &activity.ActivityRegistration{},
	); err != nil {
		return err
	}
	return nil
}

// CloseDB ✅
// 关闭数据库连接
func CloseDB(db *gorm.DB) error {
	//*gorm.DB 没有 Close() 方法,先获取*sql.DB
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
