package redis

import (
	"context"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestIncrementWithExpireSetsTTLWithoutExtendingWindow(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	client := &Client{
		rdb: goredis.NewClient(&goredis.Options{Addr: mr.Addr()}),
	}
	defer client.Close()

	ctx := context.Background()
	key := "feedsystem:ratelimit:test"
	expire := 30 * time.Second

	count, err := client.IncrementWithExpire(ctx, key, expire)
	if err != nil {
		t.Fatalf("first increment: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}

	firstTTL := mr.TTL(key)
	if firstTTL <= 0 || firstTTL > expire {
		t.Fatalf("expected ttl in (0, %s], got %s", expire, firstTTL)
	}

	mr.FastForward(5 * time.Second)
	ttlBeforeSecond := mr.TTL(key)

	count, err = client.IncrementWithExpire(ctx, key, expire)
	if err != nil {
		t.Fatalf("second increment: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}

	ttlAfterSecond := mr.TTL(key)
	if ttlAfterSecond != ttlBeforeSecond {
		t.Fatalf("expected ttl to stay at %s, got %s", ttlBeforeSecond, ttlAfterSecond)
	}
}

func TestZUnionStoreAppliesWeights(t *testing.T) {
	mr := miniredis.RunT(t)
	client := &Client{rdb: goredis.NewClient(&goredis.Options{Addr: mr.Addr()})}
	defer client.Close()

	ctx := context.Background()
	// 同一个视频在当前桶得 1 分，在较早桶得 2 分；旧桶权重只算一半。
	if err := client.ZAdd(ctx, "current", goredis.Z{Score: 1, Member: "101"}); err != nil {
		t.Fatalf("add current bucket: %v", err)
	}
	if err := client.ZAdd(ctx, "older", goredis.Z{Score: 2, Member: "101"}); err != nil {
		t.Fatalf("add older bucket: %v", err)
	}
	if err := client.ZUnionStore(ctx, "merged", []string{"current", "older"}, []float64{1, 0.5}, "SUM"); err != nil {
		t.Fatalf("weighted union: %v", err)
	}

	scores, err := client.ZRangeWithScores(ctx, "merged", 0, -1)
	if err != nil {
		t.Fatalf("read merged score: %v", err)
	}
	// 1 × 1 + 2 × 0.5 = 2。
	if len(scores) != 1 || scores[0].Score != 2 {
		t.Fatalf("merged scores = %#v, want one score of 2", scores)
	}
}
