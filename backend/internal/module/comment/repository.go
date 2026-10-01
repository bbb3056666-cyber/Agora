package comment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const commentPopularityChange int64 = 2

type CommentRepository struct {
	db *gorm.DB
}

func NewCommentRepository(db *gorm.DB) *CommentRepository {
	return &CommentRepository{db: db}
}

// SubmitIntent 将已校验的评论操作和待投递事件一起写入事务发件箱。
func (r *CommentRepository) SubmitIntent(ctx context.Context, intent rabbitmq.CommentIntent) error {
	if r == nil || r.db == nil || intent.EventID == "" || intent.ContentID == 0 || intent.AuthorID == 0 {
		return errors.New("invalid comment intent")
	}
	if intent.Action != "publish" && intent.Action != "delete" {
		return errors.New("invalid comment action")
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Create(&outbox.Message{
		EventType: "comment_intent",
		Payload:   payload,
		Status:    "pending",
	}).Error
}

// ApplyIntent 将评论真实状态、视频热度和已生效事件放入同一事务。
// 发布用 SourceEventID 唯一索引防重；删除按评论 ID 幂等。
func (r *CommentRepository) ApplyIntent(ctx context.Context, intent rabbitmq.CommentIntent) error {
	if r == nil || r.db == nil || intent.EventID == "" || intent.ContentID == 0 || intent.AuthorID == 0 {
		return errors.New("invalid comment intent")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		switch intent.Action {
		case "publish":
			return r.applyPublishIntent(tx, intent)
		case "delete":
			return r.applyDeleteIntent(tx, intent)
		default:
			return nil
		}
	})
}

func (r *CommentRepository) applyPublishIntent(tx *gorm.DB, intent rabbitmq.CommentIntent) error {
	if strings.TrimSpace(intent.Content) == "" {
		return nil
	}
	var count int64
	//检查视频存在
	if err := tx.Model(&content.Content{}).Where("id = ?", intent.ContentID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	//将评论插入数据库
	comment := Comment{
		SourceEventID: intent.EventID, //broker回复时网络断开，outbox重发，幂等
		AuthorID:      intent.AuthorID,
		Username:      strings.TrimSpace(intent.Username),
		ContentID:     intent.ContentID,
		Content:       strings.TrimSpace(intent.Content),
	}
	//收到重发时，什么也不做
	result := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_event_id"}},
		DoNothing: true,
	}).Create(&comment)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		//收到outbox的重发，直接返回
		return nil
	}
	// 同一用户首次评论该视频时才占到状态行并贡献热度；并发首评也只会成功一次。
	state := CommentPopularityState{AuthorID: intent.AuthorID, ContentID: intent.ContentID}
	// 插入用户-视频轻量表
	// 无记录，插入，有记录，什么都不做
	result = tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "author_id"}, {Name: "content_id"}},
		DoNothing: true,
	}).Create(&state)
	if result.Error != nil {
		return result.Error
	}

	popularityChange := int64(0)
	//判断是否是第一次评论
	if result.RowsAffected == 1 {
		popularityChange = commentPopularityChange
	}
	return r.finishApplied(tx, intent, comment.ID, popularityChange)
}

func (r *CommentRepository) applyDeleteIntent(tx *gorm.DB, intent rabbitmq.CommentIntent) error {
	if intent.CommentID == 0 {
		return nil
	}
	var existing Comment
	if err := tx.Where("id = ?", intent.CommentID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	// API 已检查归属；Worker 再检查一次，防止过期或伪造意图删除别人的评论。
	if existing.AuthorID != intent.AuthorID || existing.ContentID != intent.ContentID {
		return nil
	}
	if err := tx.Delete(&existing).Error; err != nil {
		return err
	}
	// 删除评论不回收热度，也没有通知等下游副作用，不必再写 Applied Outbox。
	return nil
}

// 仅在评论时调用
func (r *CommentRepository) finishApplied(tx *gorm.DB, intent rabbitmq.CommentIntent, commentID uint, popularityChange int64) error {
	//更新数据库中视频热度
	if popularityChange != 0 {
		if err := tx.Model(&content.Content{}).Where("id = ?", intent.ContentID).UpdateColumn(
			"popularity", gorm.Expr("GREATEST(popularity + ?, 0)", popularityChange),
		).Error; err != nil {
			return err
		}
	}
	//一次评论成功的唯一id
	appliedID, err := outbox.NewEventID()
	if err != nil {
		return err
	}
	applied := rabbitmq.CommentApplied{
		EventID:          appliedID,
		IntentID:         intent.EventID,
		Action:           intent.Action,
		CommentID:        commentID,
		Username:         intent.Username,
		ContentID:        intent.ContentID,
		AuthorID:         intent.AuthorID,
		Content:          intent.Content,
		PopularityChange: popularityChange,
		OccurredAt:       time.Now().UTC(),
	}
	payload, err := json.Marshal(applied)
	if err != nil {
		return err
	}
	return tx.Create(&outbox.Message{
		EventType: "comment_applied",
		Payload:   payload,
		Status:    "pending",
	}).Error
}

func (r *CommentRepository) GetAllComments(ctx context.Context, videoID uint) ([]Comment, error) {
	var comments []Comment
	err := r.db.WithContext(ctx).Where("content_id = ?", videoID).Order("created_at asc").Limit(200).Find(&comments).Error
	return comments, err
}

func (r *CommentRepository) GetByID(ctx context.Context, id uint) (*Comment, error) {
	var comment Comment
	if err := r.db.WithContext(ctx).First(&comment, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &comment, nil
}
