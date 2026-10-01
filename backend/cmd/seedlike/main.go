// seedlike 仅用于本地点赞异步链路压测的数据准备，不参与线上服务启动。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/auth"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	appdb "github.com/bbb3056666-cyber/Agora/backend/internal/platform/db"

	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
)

type actorFixture struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

type fixture struct {
	Prefix        string                    `json:"prefix"`
	CreatedAt     time.Time                 `json:"created_at"`
	ContentIDs    []uint                    `json:"content_ids"`
	TokenPools    map[string][]actorFixture `json:"token_pools"`
	ExpectedLikes map[string]int            `json:"expected_likes"`
}

func main() {
	configPath := flag.String("config", "configs/config.compose-local.json", "JSON config path")
	prefix := flag.String("prefix", "like_loadtest", "unique account and video prefix")
	rates := flag.String("rates", "100,300,500", "comma-separated QPS tiers; users are sized from rate, duration and requests-per-user")
	videosCount := flag.Int("videos", 1000, "videos used to distribute writes across the MySQL videos table")
	requestsPerUser := flag.Int("requests-per-user", 100, "maximum likes per user in one 60-second tier; must match the configured rate limit")
	output := flag.String("output", "../loadtest/generated/like_hot_fixture.json", "fixture JSON output path")
	accessTokenTTL := flag.Duration("access-token-ttl", 4*time.Hour, "access token lifetime written to the fixture")
	flag.Parse()

	if *videosCount < 1 || *requestsPerUser < 1 || *accessTokenTTL <= 0 {
		log.Fatal("invalid videos, requests-per-user or access-token-ttl")
	}
	tiers := parseRates(*rates)
	if len(tiers) == 0 {
		log.Fatal("at least one positive QPS tier is required")
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

	// 一个专用作者发布固定视频；不同用户可对同一视频各自点赞，不触发联合唯一索引冲突。
	author := account.Account{Username: *prefix + "_author", Password: string(passwordHash)}
	if err := database.WithContext(ctx).Create(&author).Error; err != nil {
		log.Fatalf("create author: %v", err)
	}
	videos := make([]content.Content, 0, *videosCount)
	for i := 0; i < *videosCount; i++ {
		videos = append(videos, content.Content{
			AuthorID: author.ID, Username: author.Username,
			Title: fmt.Sprintf("%s video %02d", *prefix, i+1), Description: "generated like load-test fixture",
			PlayURL: "http://example.invalid/loadtest.mp4", CoverURL: "http://example.invalid/loadtest.jpg",
			CreateTime: time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}
	if err := database.WithContext(ctx).CreateInBatches(&videos, 500).Error; err != nil {
		log.Fatalf("create videos: %v", err)
	}

	result := fixture{Prefix: *prefix, CreatedAt: time.Now().UTC(), TokenPools: make(map[string][]actorFixture), ExpectedLikes: make(map[string]int)}
	for _, item := range videos {
		result.ContentIDs = append(result.ContentIDs, item.ID)
	}

	accountSequence := 0
	for _, rate := range tiers {
		poolName := fmt.Sprintf("qps_%d", rate)
		// 向上取整后再加一个用户，吸收 k6 在 60 秒边界可能额外调度的一次迭代。
		userCount := (rate*60)/(*requestsPerUser) + 1
		accounts := make([]account.Account, 0, userCount)
		for i := 0; i < userCount; i++ {
			accountSequence++
			accounts = append(accounts, account.Account{Username: fmt.Sprintf("%s_user_%05d", *prefix, accountSequence), Password: string(passwordHash)})
		}
		if err := database.WithContext(ctx).CreateInBatches(&accounts, 500).Error; err != nil {
			log.Fatalf("create accounts for %s: %v", poolName, err)
		}
		for _, item := range accounts {
			token, err := auth.GenerateTokenWithTTL(item.ID, item.Username, 0, *accessTokenTTL)
			if err != nil {
				log.Fatalf("generate token for %s: %v", poolName, err)
			}
			result.TokenPools[poolName] = append(result.TokenPools[poolName], actorFixture{ID: item.ID, Username: item.Username, Token: token})
		}
		result.ExpectedLikes[poolName] = rate * 60
	}

	writeFixture(result, *output)
	log.Printf("like seed complete: videos=%d users=%d tiers=%v fixture=%s", len(videos), accountSequence, tiers, *output)
}

func parseRates(raw string) []int {
	var rates []int
	for _, item := range strings.Split(raw, ",") {
		var rate int
		if _, err := fmt.Sscanf(strings.TrimSpace(item), "%d", &rate); err != nil || rate <= 0 {
			log.Fatalf("invalid rate %q", item)
		}
		rates = append(rates, rate)
	}
	return rates
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
