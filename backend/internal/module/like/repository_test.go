package like

import (
	"context"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"testing"
	"time"
)

func TestApplyLikeIntentsRejectsInvalidBatch(t *testing.T) {
	repo := NewLikeRepository(nil)
	if err := repo.ApplyLikeIntents(context.Background(), []rabbitmq.LikeIntent{{UserID: 1, ContentID: 1}}); err == nil {
		t.Fatal("missing database must fail")
	}
	if err := repo.ApplyLikeIntents(context.Background(), nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	tooLarge := make([]rabbitmq.LikeIntent, MaxLikeIntentBatchSize+1)
	if err := repo.ApplyLikeIntents(context.Background(), tooLarge); err == nil {
		t.Fatal("oversized batch must fail before starting a transaction")
	}
}

func TestPlanLikeTransition(t *testing.T) {
	now := time.Date(2026, time.October, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name                      string
		state                     LikeState
		wantChange                int64
		wantPopularityChange      int64
		wantContributedPopularity bool
		wantLikedAt               bool
	}{
		{
			name:                      "first like contributes popularity",
			state:                     LikeState{DesiredLiked: true},
			wantChange:                1,
			wantPopularityChange:      1,
			wantContributedPopularity: true,
			wantLikedAt:               true,
		},
		{
			name:                      "unlike keeps popularity contribution history",
			state:                     LikeState{IsLiked: true, DesiredLiked: false, HasContributedPopularity: true},
			wantChange:                -1,
			wantPopularityChange:      0,
			wantContributedPopularity: true,
			wantLikedAt:               false,
		},
		{
			name:                      "relike does not contribute popularity twice",
			state:                     LikeState{DesiredLiked: true, HasContributedPopularity: true},
			wantChange:                1,
			wantPopularityChange:      0,
			wantContributedPopularity: true,
			wantLikedAt:               true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planLikeTransition(tt.state, now)
			if got.change != tt.wantChange {
				t.Fatalf("change = %d, want %d", got.change, tt.wantChange)
			}
			if got.popularityChange != tt.wantPopularityChange {
				t.Fatalf("popularityChange = %d, want %d", got.popularityChange, tt.wantPopularityChange)
			}
			if got.hasContributedPopularity != tt.wantContributedPopularity {
				t.Fatalf("hasContributedPopularity = %t, want %t", got.hasContributedPopularity, tt.wantContributedPopularity)
			}
			if (got.likedAt != nil) != tt.wantLikedAt {
				t.Fatalf("likedAt present = %t, want %t", got.likedAt != nil, tt.wantLikedAt)
			}
			if got.likedAt != nil && !got.likedAt.Equal(now) {
				t.Fatalf("likedAt = %v, want %v", got.likedAt, now)
			}
		})
	}
}
