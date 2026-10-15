package feed

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
)

func popularityTestService(t *testing.T) (*FeedService, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewFeedService(nil, nil, rediscache.NewClient(rdb, "test:"), nil), mr
}

func TestPopularityEmptyWindowExpiresAndRebuilds(t *testing.T) {
	service, mr := popularityTestService(t)
	ctx := context.Background()
	cache := service.redisCache
	mergeKey := cache.Key("hot:content:merge:test")
	emptyKey := cache.Key("hot:content:empty:test")
	bucketKey := cache.Key("hot:content:bucket:test")

	for i := 0; i < 2; i++ {
		empty, err := service.ensurePopularitySnapshot(ctx, mergeKey, emptyKey, []string{bucketKey}, []float64{1})
		if err != nil || !empty {
			t.Fatalf("empty snapshot attempt %d: empty=%t, err=%v", i, empty, err)
		}
	}
	if !mr.Exists(emptyKey) || mr.Exists(mergeKey) {
		t.Fatal("empty window must have a marker but no merged snapshot")
	}
	if err := cache.ZAdd(ctx, bucketKey, goredis.Z{Score: 1, Member: "42"}); err != nil {
		t.Fatalf("add hot content: %v", err)
	}
	mr.FastForward(popularityEmptyTTL)
	empty, err := service.ensurePopularitySnapshot(ctx, mergeKey, emptyKey, []string{bucketKey}, []float64{1})
	if err != nil || empty || !mr.Exists(mergeKey) {
		t.Fatalf("rebuilt snapshot: empty=%t, exists=%t, err=%v", empty, mr.Exists(mergeKey), err)
	}
}

func TestPopularityFallbackSingleflightAndTTL(t *testing.T) {
	service, mr := popularityTestService(t)
	ctx := context.Background()
	var loads atomic.Int32
	load := func() ([]*content.Content, error) {
		loads.Add(1)
		time.Sleep(10 * time.Millisecond)
		return []*content.Content{}, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			videos, err := service.cachedPopularityFallback(ctx, 100, 20, load)
			if err != nil || len(videos) != 0 {
				t.Errorf("cached fallback: len=%d, err=%v", len(videos), err)
			}
		}()
	}
	wg.Wait()
	if got := loads.Load(); got != 1 {
		t.Fatalf("database loads = %d, want 1", got)
	}

	other := NewFeedService(nil, nil, service.redisCache, nil)
	if _, err := other.cachedPopularityFallback(ctx, 100, 20, load); err != nil {
		t.Fatalf("cross-instance cache read: %v", err)
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("cross-instance database loads = %d, want 1", got)
	}

	mr.FastForward(popularityEmptyTTL)
	if _, err := service.cachedPopularityFallback(ctx, 100, 20, load); err != nil {
		t.Fatalf("fallback after expiry: %v", err)
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("database loads after expiry = %d, want 2", got)
	}
}
