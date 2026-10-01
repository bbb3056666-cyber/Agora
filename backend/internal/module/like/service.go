package like

import (
	"context"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
)

// LikeService 的 HTTP 关键路径只可靠记录命令，不再预查点赞状态、直接发 MQ 或更新热度。
// 状态裁决统一由 LikeIntentWorker 完成，避免并发请求基于同一旧状态各自产生副作用。
type LikeService struct {
	likeRepository *LikeRepository
}

func NewLikeService(repo *LikeRepository) *LikeService {
	return &LikeService{likeRepository: repo}
}

func (s *LikeService) Like(ctx context.Context, accountID, videoID uint) error {
	if s == nil || s.likeRepository == nil {
		return errors.New("like service is not initialized")
	}
	return s.likeRepository.SubmitLikeIntent(ctx, accountID, videoID, "like")
}

func (s *LikeService) Unlike(ctx context.Context, accountID, videoID uint) error {
	if s == nil || s.likeRepository == nil {
		return errors.New("like service is not initialized")
	}
	return s.likeRepository.SubmitLikeIntent(ctx, accountID, videoID, "unlike")
}

func (s *LikeService) IsLiked(ctx context.Context, videoID, accountID uint) (bool, error) {
	return s.likeRepository.IsLiked(ctx, videoID, accountID)
}

func (s *LikeService) ListLikedContents(ctx context.Context, accountID uint) ([]content.Content, error) {
	return s.likeRepository.ListLikedContents(ctx, accountID)
}
