package like

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	mysqlcfg "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestLikeBatchMySQL(t *testing.T) {
	dsn := os.Getenv("AGORA_LIKE_TEST_DSN")
	if dsn == "" {
		t.Skip("set AGORA_LIKE_TEST_DSN to an isolated MySQL test database")
	}
	config, err := mysqlcfg.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(config.DBName, "agora_like_opt_test_") {
		t.Fatal("integration test requires a dedicated agora_like_opt_test_ database")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&outbox.Message{}, &LikeState{}, &content.Content{}); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&content.Content{}, &LikeState{}, &outbox.Message{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	video := content.Content{AuthorID: 1, Username: "test", ContentType: content.ContentTypeArticle, Title: "test", Body: "test", LikesReconcileEnabled: true}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	for _, userID := range []uint{1, 2, 3} {
		if err := db.Create(&LikeState{AccountID: userID, ContentID: video.ID, DesiredLiked: true, HasContributedPopularity: userID == 3}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := NewLikeRepository(db)
	batch := []rabbitmq.LikeIntent{{UserID: 1, ContentID: video.ID}, {UserID: 1, ContentID: video.ID}, {UserID: 2, ContentID: video.ID}}
	if err := repo.ApplyLikeIntents(ctx, batch); err != nil {
		t.Fatal(err)
	}
	assertLikeCounts(t, db, video.ID, 2, 2, 2)
	if err := repo.ApplyLikeIntents(ctx, batch); err != nil {
		t.Fatal(err)
	}
	assertLikeCounts(t, db, video.ID, 2, 2, 2)

	if err := db.Model(&LikeState{}).Where("account_id = ? AND content_id = ?", 1, video.ID).Update("desired_liked", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyLikeIntents(ctx, []rabbitmq.LikeIntent{{UserID: 1, ContentID: video.ID}, {UserID: 3, ContentID: video.ID}}); err != nil {
		t.Fatal(err)
	}
	assertLikeCounts(t, db, video.ID, 2, 2, 3)
	var state LikeState
	if err := db.Where("account_id = ? AND content_id = ?", 1, video.ID).Take(&state).Error; err != nil || state.IsLiked {
		t.Fatalf("cancel was not applied: state=%+v err=%v", state, err)
	}

	if err := db.Create(&LikeState{AccountID: 4, ContentID: video.ID + 1000, DesiredLiked: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyLikeIntent(ctx, rabbitmq.LikeIntent{UserID: 4, ContentID: video.ID + 1000}); err != nil {
		t.Fatal(err)
	}
	state = LikeState{}
	if err := db.Where("account_id = ?", 4).Take(&state).Error; err != nil || state.IsLiked {
		t.Fatalf("missing content committed a like: state=%+v err=%v", state, err)
	}

	if err := db.Model(&content.Content{}).Where("id = ?", video.ID).UpdateColumn("likes_count", 99).Error; err != nil {
		t.Fatal(err)
	}
	_, checked, corrected, err := repo.ReconcileLikeCounts(ctx, 0, 100)
	if err != nil || checked != 1 || corrected != 1 {
		t.Fatalf("reconcile checked=%d corrected=%d err=%v", checked, corrected, err)
	}
	assertLikeCounts(t, db, video.ID, 2, 2, 3)
	if err := db.Model(&LikeState{}).Where("account_id = ? AND content_id = ?", 2, video.ID).
		Update("desired_liked", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyLikeIntents(ctx, []rabbitmq.LikeIntent{
		{UserID: 2, ContentID: video.ID}, {UserID: 99, ContentID: video.ID},
	}); err == nil {
		t.Fatal("a missing relation must abort the entire batch")
	}
	assertLikeCounts(t, db, video.ID, 2, 2, 3)
	if err := db.Model(&LikeState{}).Where("account_id = ? AND content_id = ?", 2, video.ID).
		Update("desired_liked", true).Error; err != nil {
		t.Fatal(err)
	}

	checkMixedBatch(t, db, repo)
}

func checkMixedBatch(t *testing.T, db *gorm.DB, repo *LikeRepository) {
	t.Helper()
	video := content.Content{
		AuthorID: 1, Username: "test", ContentType: content.ContentTypeArticle,
		Title: "mixed", Body: "mixed", LikesCount: 16, Popularity: 16,
	}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	batch := make([]rabbitmq.LikeIntent, 0, MaxLikeIntentBatchSize)
	for userID := uint(100); userID < 100+MaxLikeIntentBatchSize; userID++ {
		wasLiked := userID < 116
		state := LikeState{
			AccountID: userID, ContentID: video.ID, IsLiked: wasLiked,
			DesiredLiked: !wasLiked, HasContributedPopularity: wasLiked,
		}
		if wasLiked {
			state.LikedAt = &now
		}
		if err := db.Create(&state).Error; err != nil {
			t.Fatal(err)
		}
		batch = append(batch, rabbitmq.LikeIntent{UserID: userID, ContentID: video.ID})
	}
	ctx := context.Background()
	if err := repo.ApplyLikeIntents(ctx, batch); err != nil {
		t.Fatal(err)
	}
	assertLikeCounts(t, db, video.ID, 16, 32, 19)
	if err := repo.ApplyLikeIntents(ctx, batch); err != nil {
		t.Fatal(err)
	}
	assertLikeCounts(t, db, video.ID, 16, 32, 19)
	var first, last LikeState
	if err := db.Where("account_id = ? AND content_id = ?", 100, video.ID).Take(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("account_id = ? AND content_id = ?", 131, video.ID).Take(&last).Error; err != nil {
		t.Fatal(err)
	}
	if first.IsLiked || first.LikedAt != nil || !first.HasContributedPopularity {
		t.Fatalf("unlike state incorrect: %+v", first)
	}
	if !last.IsLiked || last.LikedAt == nil || !last.HasContributedPopularity {
		t.Fatalf("new like state incorrect: %+v", last)
	}
}

func assertLikeCounts(t *testing.T, db *gorm.DB, contentID uint, likes, popularity, events int64) {
	t.Helper()
	var video content.Content
	if err := db.First(&video, contentID).Error; err != nil {
		t.Fatal(err)
	}
	if video.LikesCount != likes || video.Popularity != popularity {
		t.Fatalf("likes=%d popularity=%d, want %d/%d", video.LikesCount, video.Popularity, likes, popularity)
	}
	var count int64
	if err := db.Model(&outbox.Message{}).Where("event_type = ?", "like_applied").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != events {
		t.Fatalf("applied events=%d, want %d", count, events)
	}
}
