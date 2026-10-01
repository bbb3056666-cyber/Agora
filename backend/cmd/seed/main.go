package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/auth"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/activity"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/social"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	appdb "github.com/bbb3056666-cyber/Agora/backend/internal/platform/db"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const redisPrefix = "v1:"

type actorFixture struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

type fixture struct {
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"created_at"`
	Feed      struct {
		Authors      int            `json:"authors"`
		Videos       int            `json:"videos"`
		ViewerTokens []actorFixture `json:"viewer_tokens"`
	} `json:"feed"`
	Activity struct {
		ID                uint           `json:"id"`
		Quota             int64          `json:"quota"`
		ParticipantTokens []actorFixture `json:"participant_tokens"`
	} `json:"activity"`
}

func main() {
	configPath := flag.String("config", "configs/config.compose-local.json", "JSON config path")
	prefix := flag.String("prefix", "loadtest", "unique username/title prefix")
	users := flag.Int("users", 1000, "number of test accounts")
	authors := flag.Int("authors", 200, "number of accounts that publish videos")
	videosPerAuthor := flag.Int("videos-per-author", 50, "videos per author")
	followsPerUser := flag.Int("follows-per-user", 20, "authors followed by each viewer")
	activityQuota := flag.Int64("activity-quota", 200, "quota of the generated activity")
	activityUsers := flag.Int("activity-users", 800, "tokens emitted for activity registration")
	output := flag.String("output", "../test/generated/loadtest-fixture.json", "fixture JSON output path")
	tokensOnly := flag.Bool("tokens-only", false, "refresh fixture tokens for an existing seed prefix without inserting data")
	accessTokenTTL := flag.Duration("access-token-ttl", 15*time.Minute, "access token lifetime written to the test fixture")
	flag.Parse()

	if *users < 1 || *authors < 1 || *authors > *users || *videosPerAuthor < 1 || *followsPerUser < 0 || *activityQuota < 1 || *activityUsers < 1 || *activityUsers > *users || *accessTokenTTL <= 0 {
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
	if *tokensOnly {
		refreshFixture(ctx, database, *prefix, *authors, *activityUsers, *accessTokenTTL, *output)
		return
	}

	var existing int64
	if err := database.WithContext(ctx).Model(&account.Account{}).Where("username LIKE ?", *prefix+"_%").Count(&existing).Error; err != nil {
		log.Fatalf("check existing seed: %v", err)
	}
	if existing != 0 {
		log.Fatalf("seed prefix %q already exists; use -prefix with a new value", *prefix)
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte("Loadtest123"), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	accounts := make([]account.Account, 0, *users)
	for i := 0; i < *users; i++ {
		accounts = append(accounts, account.Account{Username: fmt.Sprintf("%s_user_%04d", *prefix, i+1), Password: string(passwordHash)})
	}
	if err := database.WithContext(ctx).CreateInBatches(&accounts, 500).Error; err != nil {
		log.Fatalf("create accounts: %v", err)
	}

	totalVideos := *authors * *videosPerAuthor
	baseTime := time.Now().Add(-time.Duration(totalVideos) * time.Second)
	videos := make([]content.Content, 0, totalVideos)
	for authorIndex := 0; authorIndex < *authors; authorIndex++ {
		owner := accounts[authorIndex]
		for sequence := 0; sequence < *videosPerAuthor; sequence++ {
			position := authorIndex**videosPerAuthor + sequence
			videos = append(videos, content.Content{AuthorID: owner.ID, Username: owner.Username, Title: fmt.Sprintf("%s video %d #loadtest", *prefix, position+1), Description: "generated load-test fixture #loadtest", PlayURL: "http://example.invalid/loadtest.mp4", CoverURL: "http://example.invalid/loadtest.jpg", CreateTime: baseTime.Add(time.Duration(position) * time.Second), LikesCount: int64(position % 1000), Popularity: int64(position % 500)})
		}
	}
	if err := database.WithContext(ctx).CreateInBatches(&videos, 500).Error; err != nil {
		log.Fatalf("create videos: %v", err)
	}

	tag := content.Tag{Name: "loadtest"}
	if err := database.WithContext(ctx).Where("name = ?", tag.Name).FirstOrCreate(&tag).Error; err != nil {
		log.Fatalf("create tag: %v", err)
	}
	links := make([]content.ContentTag, 0, len(videos))
	for _, item := range videos {
		links = append(links, content.ContentTag{ContentID: item.ID, TagID: tag.ID})
	}
	if err := database.WithContext(ctx).CreateInBatches(&links, 500).Error; err != nil {
		log.Fatalf("create video tags: %v", err)
	}

	maxFollows := *followsPerUser
	if maxFollows > *authors {
		maxFollows = *authors
	}
	follows := make([]social.Social, 0, (*users-*authors)*maxFollows)
	for userIndex := *authors; userIndex < *users; userIndex++ {
		for offset := 0; offset < maxFollows; offset++ {
			follows = append(follows, social.Social{FollowerID: accounts[userIndex].ID, VloggerID: accounts[(userIndex+offset)%*authors].ID})
		}
	}
	if len(follows) > 0 {
		if err := database.WithContext(ctx).CreateInBatches(&follows, 500).Error; err != nil {
			log.Fatalf("create follows: %v", err)
		}
	}

	timelineKey := redisPrefix + "feed:global_timeline"
	hotStart := 0
	if len(videos) > 1000 {
		hotStart = len(videos) - 1000
	}
	zset := make([]redis.Z, 0, len(videos)-hotStart)
	for _, item := range videos[hotStart:] {
		zset = append(zset, redis.Z{Score: float64(item.CreateTime.UnixMilli()), Member: fmt.Sprintf("%d", item.ID)})
	}
	if err := rdb.ZAdd(ctx, timelineKey, zset...).Err(); err != nil {
		log.Fatalf("seed timeline: %v", err)
	}

	activity := activity.ContentActivity{ContentID: videos[0].ID, CreatorID: accounts[0].ID, Title: *prefix + " activity", Description: "generated load-test activity", Quota: *activityQuota, RemainingQuota: *activityQuota, StartAt: time.Now().Add(-time.Minute), EndAt: time.Now().Add(24 * time.Hour)}
	if err := database.WithContext(ctx).Create(&activity).Error; err != nil {
		log.Fatalf("create activity: %v", err)
	}
	activityKey := fmt.Sprintf("%scontent_activity:%d", redisPrefix, activity.ID)
	if err := rdb.HSet(ctx, activityKey, map[string]any{"remaining_quota": activity.RemainingQuota, "start_at": activity.StartAt.Unix(), "end_at": activity.EndAt.Unix()}).Err(); err != nil {
		log.Fatalf("seed activity redis hash: %v", err)
	}

	result := fixture{Prefix: *prefix, CreatedAt: time.Now()}
	result.Feed.Authors = *authors
	result.Feed.Videos = len(videos)
	result.Activity.ID = activity.ID
	result.Activity.Quota = activity.Quota
	for index := 0; index < *activityUsers; index++ {
		item := accounts[index]
		token, err := auth.GenerateTokenWithTTL(item.ID, item.Username, 0, *accessTokenTTL)
		if err != nil {
			log.Fatalf("generate token: %v", err)
		}
		actor := actorFixture{ID: item.ID, Username: item.Username, Token: token}
		result.Activity.ParticipantTokens = append(result.Activity.ParticipantTokens, actor)
		if index < 100 {
			result.Feed.ViewerTokens = append(result.Feed.ViewerTokens, actor)
		}
	}

	writeFixture(result, *output)
	log.Printf("seed complete: accounts=%d videos=%d follows=%d activity_id=%d fixture=%s", len(accounts), len(videos), len(follows), activity.ID, *output)
}

func refreshFixture(ctx context.Context, database *gorm.DB, prefix string, authorCount, activityUsers int, accessTokenTTL time.Duration, output string) {
	var accounts []account.Account
	if err := database.WithContext(ctx).Where("username LIKE ?", prefix+"_%").Order("id ASC").Find(&accounts).Error; err != nil {
		log.Fatalf("load seed accounts: %v", err)
	}
	if len(accounts) < activityUsers || authorCount >= len(accounts) {
		log.Fatal("existing seed data does not match the requested account counts")
	}
	var activity activity.ContentActivity
	if err := database.WithContext(ctx).Where("title = ?", prefix+" activity").Order("id DESC").First(&activity).Error; err != nil {
		log.Fatalf("load generated activity: %v", err)
	}
	result := fixture{Prefix: prefix, CreatedAt: time.Now()}
	result.Feed.Authors = authorCount
	result.Activity.ID = activity.ID
	result.Activity.Quota = activity.Quota
	for index := 0; index < activityUsers; index++ {
		item := accounts[index]
		token, err := auth.GenerateTokenWithTTL(item.ID, item.Username, 0, accessTokenTTL)
		if err != nil {
			log.Fatalf("generate token: %v", err)
		}
		actor := actorFixture{ID: item.ID, Username: item.Username, Token: token}
		result.Activity.ParticipantTokens = append(result.Activity.ParticipantTokens, actor)
		if index >= authorCount && len(result.Feed.ViewerTokens) < 100 {
			result.Feed.ViewerTokens = append(result.Feed.ViewerTokens, actor)
		}
	}
	var videoCount int64
	if err := database.WithContext(ctx).Model(&content.Content{}).Where("title LIKE ?", prefix+" video %").Count(&videoCount).Error; err != nil {
		log.Fatalf("count seed videos: %v", err)
	}
	result.Feed.Videos = int(videoCount)
	writeFixture(result, output)
	log.Printf("token fixture refreshed: activity_id=%d fixture=%s", activity.ID, output)
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
