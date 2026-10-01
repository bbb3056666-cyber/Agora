// seedpopularity 仅用于本地热门流压测的数据准备，不参与线上服务启动。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/auth"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/like"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	appdb "github.com/bbb3056666-cyber/Agora/backend/internal/platform/db"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

const redisPrefix = "v1:"

type viewerFixture struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

type fixture struct {
	Prefix        string          `json:"prefix"`
	CreatedAt     time.Time       `json:"created_at"`
	Authors       int             `json:"authors"`
	Videos        int             `json:"videos"`
	HotCandidates int             `json:"hot_candidates"`
	ViewerTokens  []viewerFixture `json:"viewer_tokens"`
}

func main() {
	configPath := flag.String("config", "configs/config.compose-local.json", "JSON config path")
	prefix := flag.String("prefix", "popularity_loadtest", "unique account and video prefix")
	authors := flag.Int("authors", 100, "number of video authors")
	videosPerAuthor := flag.Int("videos-per-author", 20, "videos created per author")
	viewers := flag.Int("viewers", 100, "number of logged-in viewers and emitted tokens")
	candidates := flag.Int("hot-candidates", 1200, "number of videos written into the 60-minute hot window")
	output := flag.String("output", "../loadtest/generated/popularity_hot_fixture.json", "fixture JSON output path")
	accessTokenTTL := flag.Duration("access-token-ttl", 4*time.Hour, "access token lifetime written to the fixture")
	flag.Parse()

	if *authors < 1 || *videosPerAuthor < 1 || *viewers < 1 || *candidates < 20 || *candidates > *authors**videosPerAuthor || *accessTokenTTL <= 0 {
		log.Fatal("invalid seed parameters")
	}

	_ = godotenv.Load(".env")
	_ = godotenv.Load("../.env")
	cfg, _, err := config.LoadLocalDev(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	database, err := appdb.NewDB(cfg.Database, config.DefaultDatabasePoolConfig())
	if err != nil {
		log.Fatalf("connect mysql: %v", err)
	}
	defer appdb.CloseDB(database)
	if err := appdb.AutoMigrate(database); err != nil {
		log.Fatalf("migrate mysql: %v", err)
	}

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port), Password: cfg.Redis.Password, DB: cfg.Redis.DB})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("connect redis: %v", err)
	}

	var existing int64
	if err := database.WithContext(ctx).Model(&account.Account{}).Where("username LIKE ?", *prefix+"_%").Count(&existing).Error; err != nil {
		log.Fatalf("check existing data: %v", err)
	}
	if existing != 0 {
		log.Fatalf("seed prefix %q already exists; choose a new prefix instead of overwriting prior test data", *prefix)
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte("Loadtest123"), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	accounts := make([]account.Account, 0, *authors+*viewers)
	for i := 0; i < *authors+*viewers; i++ {
		accounts = append(accounts, account.Account{Username: fmt.Sprintf("%s_user_%04d", *prefix, i+1), Password: string(passwordHash)})
	}
	if err := database.WithContext(ctx).CreateInBatches(&accounts, 500).Error; err != nil {
		log.Fatalf("create accounts: %v", err)
	}

	totalVideos := *authors * *videosPerAuthor
	baseTime := time.Now().UTC().Add(-time.Duration(totalVideos) * time.Second)
	videos := make([]content.Content, 0, totalVideos)
	for authorIndex := 0; authorIndex < *authors; authorIndex++ {
		owner := accounts[authorIndex]
		for sequence := 0; sequence < *videosPerAuthor; sequence++ {
			position := authorIndex**videosPerAuthor + sequence
			videos = append(videos, content.Content{
				AuthorID: owner.ID, Username: owner.Username,
				Title: fmt.Sprintf("%s hot video %d", *prefix, position+1), Description: "generated popularity load-test fixture",
				PlayURL: "http://example.invalid/loadtest.mp4", CoverURL: "http://example.invalid/loadtest.jpg",
				CreateTime: baseTime.Add(time.Duration(position) * time.Second), LikesCount: int64(position % 1000), Popularity: int64(position % 500),
			})
		}
	}
	if err := database.WithContext(ctx).CreateInBatches(&videos, 500).Error; err != nil {
		log.Fatalf("create videos: %v", err)
	}

	// 在当前分钟及其之前 59 个分钟桶中写入 1,200 个候选视频，共约 24,000 条热度事件样本。
	// 每个桶只包含其中一部分成员，模拟互动事件在时间窗口中的分布，而不是把全量视频复制到每个桶。
	asOf := time.Now().UTC().Truncate(time.Minute)
	const bucketCount = 60
	const entriesPerBucket = 400
	keys := make([]string, 0, bucketCount)
	weights := make([]float64, 0, bucketCount)
	for minuteOffset := 0; minuteOffset < bucketCount; minuteOffset++ {
		key := redisPrefix + "hot:content:1m:" + asOf.Add(-time.Duration(minuteOffset)*time.Minute).Format("200601021504")
		keys = append(keys, key)
		weights = append(weights, popularityWeight(minuteOffset))
		entries := make([]redis.Z, 0, entriesPerBucket)
		for item := 0; item < entriesPerBucket; item++ {
			candidateIndex := (minuteOffset*entriesPerBucket + item) % *candidates
			entries = append(entries, redis.Z{Member: fmt.Sprintf("%d", videos[candidateIndex].ID), Score: float64(1 + (candidateIndex*17+minuteOffset*7)%10)})
		}
		if err := rdb.ZAdd(ctx, key, entries...).Err(); err != nil {
			log.Fatalf("write hot bucket %s: %v", key, err)
		}
		if err := rdb.Expire(ctx, key, 2*time.Hour).Err(); err != nil {
			log.Fatalf("expire hot bucket %s: %v", key, err)
		}
	}
	mergeKey := redisPrefix + "hot:content:merge:1m:" + asOf.Format("200601021504")
	// 先按读侧相同的权重合并一次，得到固定的首页候选，为每个测试用户写入点赞关系。
	if err := rdb.ZUnionStore(ctx, mergeKey, &redis.ZStore{Keys: keys, Weights: weights, Aggregate: "SUM"}).Err(); err != nil {
		log.Fatalf("build hot snapshot for likes: %v", err)
	}
	topMembers, err := rdb.ZRevRange(ctx, mergeKey, 0, 19).Result()
	if err != nil || len(topMembers) != 20 {
		log.Fatalf("load hot snapshot top 20: members=%d err=%v", len(topMembers), err)
	}
	likedAt := time.Now()
	likeStates := make([]like.LikeState, 0, *viewers*len(topMembers))
	for viewerIndex := 0; viewerIndex < *viewers; viewerIndex++ {
		viewer := accounts[*authors+viewerIndex]
		for _, member := range topMembers {
			videoID, err := strconv.ParseUint(member, 10, 64)
			if err != nil || videoID == 0 {
				log.Fatalf("invalid hot video ID %q: %v", member, err)
			}
			likeStates = append(likeStates, like.LikeState{
				ContentID: uint(videoID),
				AccountID: viewer.ID,
				IsLiked:   true,
				LikedAt:   &likedAt,
			})
		}
	}
	if err := database.WithContext(ctx).CreateInBatches(&likeStates, 500).Error; err != nil {
		log.Fatalf("create homepage likes: %v", err)
	}

	// 删除仅当前分钟的合并快照；下一次热门流读取会用完整窗口重新生成它。
	if err := rdb.Del(ctx, mergeKey).Err(); err != nil {
		log.Fatalf("delete stale hot snapshot: %v", err)
	}

	result := fixture{Prefix: *prefix, CreatedAt: time.Now().UTC(), Authors: *authors, Videos: len(videos), HotCandidates: *candidates}
	for viewerIndex := 0; viewerIndex < *viewers; viewerIndex++ {
		item := accounts[*authors+viewerIndex]
		token, err := auth.GenerateTokenWithTTL(item.ID, item.Username, 0, *accessTokenTTL)
		if err != nil {
			log.Fatalf("generate viewer token: %v", err)
		}
		result.ViewerTokens = append(result.ViewerTokens, viewerFixture{ID: item.ID, Username: item.Username, Token: token})
	}
	writeFixture(result, *output)
	log.Printf("popularity seed complete: accounts=%d videos=%d candidates=%d likes=%d fixture=%s", len(accounts), len(videos), *candidates, len(likeStates), *output)
}

func popularityWeight(ageMinutes int) float64 {
	return math.Pow(0.5, float64(ageMinutes)/30)
}

func writeFixture(value fixture, output string) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Fatalf("encode fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		log.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(output, data, 0o600); err != nil {
		log.Fatalf("write fixture: %v", err)
	}
}
