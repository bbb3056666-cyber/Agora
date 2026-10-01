package popularity

import (
	"context"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	goredis "github.com/redis/go-redis/v9"
)

func TestApplyEventIsIdempotent(t *testing.T) {
	mr := miniredis.RunT(t)
	cache := rediscache.NewClient(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}), "test:")
	defer cache.Close()

	ctx := context.Background()
	// 模拟 RabbitMQ 将完全相同的事件重新投递两次。
	applied, err := ApplyEvent(ctx, cache, "event-1", 101, 3)
	if err != nil {
		t.Fatalf("apply first event: %v", err)
	}
	if !applied {
		t.Fatal("first event should be applied")
	}

	applied, err = ApplyEvent(ctx, cache, "event-1", 101, 3)
	if err != nil {
		t.Fatalf("apply duplicate event: %v", err)
	}
	if applied {
		t.Fatal("duplicate event should not be applied")
	}

	var windowKey string
	for _, key := range mr.Keys() {
		if strings.Contains(key, "hot:content:1m:") {
			windowKey = key
			break
		}
	}
	if windowKey == "" {
		t.Fatal("popularity window key not found")
	}

	scores, err := cache.ZRangeWithScores(ctx, windowKey, 0, -1)
	if err != nil {
		t.Fatalf("read popularity score: %v", err)
	}
	// 两次调用只累计一次：3，而不是 6。
	if len(scores) != 1 || scores[0].Score != 3 {
		t.Fatalf("popularity scores = %#v, want one score of 3", scores)
	}
}
