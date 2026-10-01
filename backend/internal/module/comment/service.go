package comment

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/apierror"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
)

// CommentService 的 HTTP 路径只做校验并可靠记录 Intent。
// 评论真正落库、热度更新和通知事件由独立 Worker 串起。
type CommentService struct {
	repo              *CommentRepository
	contentRepository *content.ContentRepository
}

func NewCommentService(repo *CommentRepository, videoRepo *content.ContentRepository) *CommentService {
	return &CommentService{repo: repo, contentRepository: videoRepo}
}

func (s *CommentService) Publish(ctx context.Context, comment *Comment) error {
	if s == nil || s.repo == nil || s.contentRepository == nil || comment == nil {
		return errors.New("comment service is not initialized")
	}
	comment.Username = strings.TrimSpace(comment.Username)
	comment.Content = strings.TrimSpace(comment.Content)
	if comment.ContentID == 0 || comment.AuthorID == 0 {
		return errors.New("content_id and author_id are required")
	}
	if comment.Content == "" {
		return errors.New("content is required")
	}
	exists, err := s.contentRepository.IsExist(ctx, comment.ContentID)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("video not found")
	}

	eventID, err := outbox.NewEventID()
	if err != nil {
		return err
	}
	return s.repo.SubmitIntent(ctx, rabbitmq.CommentIntent{
		EventID:    eventID,
		Action:     "publish",
		Username:   comment.Username,
		ContentID:  comment.ContentID,
		AuthorID:   comment.AuthorID,
		Content:    comment.Content,
		OccurredAt: time.Now().UTC(),
	})
}

func (s *CommentService) Delete(ctx context.Context, commentID uint, accountID uint) error {
	if s == nil || s.repo == nil {
		return errors.New("comment service is not initialized")
	}
	comment, err := s.repo.GetByID(ctx, commentID)
	if err != nil {
		return err
	}
	if comment == nil {
		return errors.New("comment not found")
	}
	if comment.AuthorID != accountID {
		return apierror.ErrUnauthorized
	}
	eventID, err := outbox.NewEventID()
	if err != nil {
		return err
	}
	return s.repo.SubmitIntent(ctx, rabbitmq.CommentIntent{
		EventID:    eventID,
		Action:     "delete",
		CommentID:  comment.ID,
		Username:   comment.Username,
		ContentID:  comment.ContentID,
		AuthorID:   accountID,
		Content:    comment.Content,
		OccurredAt: time.Now().UTC(),
	})
}

func (s *CommentService) GetAll(ctx context.Context, videoID uint) ([]Comment, error) {
	if s == nil || s.repo == nil || s.contentRepository == nil {
		return nil, errors.New("comment service is not initialized")
	}
	exists, err := s.contentRepository.IsExist(ctx, videoID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("video not found")
	}
	return s.repo.GetAllComments(ctx, videoID)
}
