package activity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	redis "github.com/redis/go-redis/v9"
)

func TestReserve_OneAccountOnceAndNoOverQuota(t *testing.T) {
	mr := miniredis.RunT(t)
	client := rediscache.NewClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "test:")
	service := NewActivityService(nil, nil, client, nil)
	activity := &ContentActivity{ID: 1, RemainingQuota: 1, StartAt: time.Now().Add(-time.Minute), EndAt: time.Now().Add(time.Hour)}
	if err := service.initActivityCache(context.Background(), activity); err != nil {
		t.Fatalf("init activity cache: %v", err)
	}

	if err := service.Reserve(context.Background(), activity.ID, 1001); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := service.Reserve(context.Background(), activity.ID, 1001); !errors.Is(err, ErrAlreadyRegistered) {
		t.Fatalf("same account error = %v, want %v", err, ErrAlreadyRegistered)
	}
	if err := service.Reserve(context.Background(), activity.ID, 1002); !errors.Is(err, ErrQuotaExhausted) {
		t.Fatalf("over quota error = %v, want %v", err, ErrQuotaExhausted)
	}
}
