package activity

import (
	"context"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	"log"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var (
	ErrInvalidActivity       = errors.New("invalid content activity")
	ErrContentNotFound       = errors.New("content not found")
	ErrNotContentOwner       = errors.New("only the content author can create an activity")
	ErrQuotaExhausted        = errors.New("activity quota exhausted")
	ErrAlreadyRegistered     = errors.New("already registered for this activity")
	ErrActivityNotStarted    = errors.New("activity has not started")
	ErrActivityEnded         = errors.New("activity has ended")
	ErrActivityCacheMiss     = errors.New("activity cache not found")
	ErrActivityMQUnavailable = errors.New("activity mq is unavailable")
)

type ActivityService struct {
	repo   *ActivityRepository
	videos *content.ContentRepository
	cache  *rediscache.Client
	mq     *rabbitmq.ActivityMQ
}

func NewActivityService(
	repo *ActivityRepository,
	videos *content.ContentRepository,
	cache *rediscache.Client,
	mq *rabbitmq.ActivityMQ,
) *ActivityService {
	return &ActivityService{
		repo:   repo,
		videos: videos,
		cache:  cache,
		mq:     mq,
	}
}

// CreateActivity 先确认视频存在且归当前用户所有，再建立限量报名活动。
// 向活动表插入一条数据
// 初始化redis中活动信息: hash(剩余名额,开始时间,结束时间),set(报名人)
func (s *ActivityService) CreateActivity(ctx context.Context, creatorID uint, req CreateActivityRequest) (*ContentActivity, error) {
	if s == nil || s.repo == nil || s.videos == nil || creatorID == 0 || req.ContentID == 0 || req.Title == "" || req.Quota <= 0 {
		return nil, ErrInvalidActivity
	}
	startAt, endAt := time.Unix(req.StartAt, 0), time.Unix(req.EndAt, 0)
	if req.StartAt <= 0 || req.EndAt <= req.StartAt || req.Quota > 100000 {
		return nil, ErrInvalidActivity
	}

	v, err := s.videos.GetByID(ctx, req.ContentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrContentNotFound
		}
		return nil, err
	}
	if v.AuthorID != creatorID {
		return nil, ErrNotContentOwner
	}

	activity := &ContentActivity{
		ContentID:      req.ContentID,
		CreatorID:      creatorID,
		Title:          req.Title,
		Description:    req.Description,
		Quota:          req.Quota,
		RemainingQuota: req.Quota,
		StartAt:        startAt,
		EndAt:          endAt,
	}
	if err := s.repo.CreateActivity(ctx, activity); err != nil {
		return nil, err
	}
	if err := s.initActivityCache(ctx, activity); err != nil {
		return nil, err
	}
	return activity, nil
}

// 根据视频id找所有活动
func (s *ActivityService) ListByContentID(ctx context.Context, videoID uint) ([]ContentActivity, error) {
	if s == nil || s.repo == nil || videoID == 0 {
		return nil, ErrInvalidActivity
	}
	return s.repo.ListByContentID(ctx, videoID)
}

func activityMetaKey(cache *rediscache.Client, activityID uint) string {
	return cache.Key("content_activity:%d", activityID)
}

func activityAccountsKey(cache *rediscache.Client, activityID uint) string {
	return cache.Key("content_activity:%d:accounts", activityID)
}

func activityResultKey(cache *rediscache.Client, activityID, accountID uint) string {
	return cache.Key("content_activity:%d:result:%d", activityID, accountID)
}

// initActivityCache 为后续 Lua 报名脚本准备名额和时间窗口。
// accounts 是 Set，首次 SADD 时自动创建，因此这里无需写一个空集合。
func (s *ActivityService) initActivityCache(ctx context.Context, activity *ContentActivity) error {
	if s.cache == nil {
		return errors.New("redis is required for video activity registration")
	}
	if activity == nil {
		return ErrInvalidActivity
	}

	// 活动结束后继续保留一天，方便用户查询刚结束活动的报名结果。
	ttl := time.Until(activity.EndAt.Add(24 * time.Hour))
	if ttl <= 0 {
		return ErrInvalidActivity
	}

	metaKey := activityMetaKey(s.cache, activity.ID)
	if err := s.cache.HSet(ctx, metaKey,
		"remaining_quota", activity.RemainingQuota,
		"start_at", activity.StartAt.Unix(),
		"end_at", activity.EndAt.Unix(),
	); err != nil {
		return err
	}
	return s.cache.Expire(ctx, metaKey, ttl)
}

// reserveScript 将活动状态、重复报名和名额扣减放在 Redis 一次原子执行中。
// KEYS[1]：活动 Hash；KEYS[2]：已报名用户 Set；ARGV[1]：用户 ID；ARGV[2]：当前 Unix 秒。
var reserveScript = goredis.NewScript(`
local remaining = redis.call("HGET", KEYS[1], "remaining_quota")
local startAt = redis.call("HGET", KEYS[1], "start_at")
local endAt = redis.call("HGET", KEYS[1], "end_at")
if not remaining or not startAt or not endAt then return -5 end

local now = tonumber(ARGV[2])
if now < tonumber(startAt) then return -3 end
if now >= tonumber(endAt) then return -4 end
if redis.call("SISMEMBER", KEYS[2], ARGV[1]) == 1 then return -2 end
if tonumber(remaining) <= 0 then return -1 end

redis.call("HINCRBY", KEYS[1], "remaining_quota", -1)
redis.call("SADD", KEYS[2], ARGV[1])
-- Set 第一次创建时也要有和活动状态一致的生命周期，避免活动结束后留下用户集合。
redis.call("EXPIRE", KEYS[2], tonumber(endAt) - now + 86400)
return 0
`)

// Reserve 负责 Redis 的快速原子预报名；成功后由下一步的 MQ 异步创建最终报名记录。
func (s *ActivityService) Reserve(ctx context.Context, activityID, accountID uint) error {
	if s == nil || s.cache == nil || activityID == 0 || accountID == 0 {
		return ErrInvalidActivity
	}
	result, err := s.cache.RunScriptInt(
		ctx,
		reserveScript,
		[]string{activityMetaKey(s.cache, activityID), activityAccountsKey(s.cache, activityID)},
		accountID,
		time.Now().Unix(),
	)
	if err != nil {
		return err
	}
	switch result {
	case 0:
		return nil
	case -1: //库存不足
		return ErrQuotaExhausted
	case -2: //已经有名额
		return ErrAlreadyRegistered
	case -3: //早于开始时间
		return ErrActivityNotStarted
	case -4: //过期
		return ErrActivityEnded
	case -5: //没找到参数
		return ErrActivityCacheMiss
	default:
		return errors.New("unexpected activity reservation result")
	}
}

// Register 先在 Redis 原子预报名，再投递最终创建报名记录的异步任务。
// MQ 在当前调用中明确返回失败时，补偿 Redis，避免无效请求永久占用名额。
func (s *ActivityService) Register(ctx context.Context, activityID, accountID uint) error {
	if s == nil || s.mq == nil {
		return ErrActivityMQUnavailable
	}

	if err := s.Reserve(ctx, activityID, accountID); err != nil {
		return err
	}

	if err := s.mq.PublishRegistration(ctx, activityID, accountID); err != nil {
		if rollbackErr := s.cancelReservation(ctx, activityID, accountID); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}

	// 状态缓存写失败不回滚：消息已经投递，Worker 成功后仍会回写 SUCCESS；
	// 查询接口还会回查 MySQL 和预报名 Set。
	_ = s.setRegistrationResult(ctx, activityID, accountID, "PENDING")
	return nil
}

var cancelReserveScript = goredis.NewScript(`
if redis.call("SISMEMBER", KEYS[2], ARGV[1]) == 0 then return 0 end
redis.call("SREM", KEYS[2], ARGV[1])
return redis.call("HINCRBY", KEYS[1], "remaining_quota", 1)
`)

// 恢复redis库存
func (s *ActivityService) cancelReservation(ctx context.Context, activityID, accountID uint) error {
	_, err := s.cache.RunScriptInt(
		ctx,
		cancelReserveScript,
		[]string{activityMetaKey(s.cache, activityID), activityAccountsKey(s.cache, activityID)},
		accountID,
	)
	return err
}

// PersistRegistration 由 Worker 调用。MySQL 的唯一索引和 remaining_quota > 0 是最终兜底。
func (s *ActivityService) PersistRegistration(ctx context.Context, eventID string, activityID, accountID uint) error {
	if s == nil || s.repo == nil {
		return errors.New("activity repository is not initialized")
	}

	//同一事务里先占订单唯一键，再扣 MySQL 库存
	err := s.repo.CreateOrderAndDeduct(ctx, eventID, activityID, accountID)
	if err != nil && !errors.Is(err, ErrOrderDuplicated) {
		return err
	}

	//
	_ = s.mq.PublishRegistrationSuccess(ctx, eventID, activityID, accountID)

	if err := s.setRegistrationResult(ctx, activityID, accountID, "SUCCESS"); err != nil {
		log.Println("failed to persist registration result:", err)
	}
	return nil
}

// FailRegistration 仅在 Worker 已重试多次、仍无法写库时调用，释放预留名额供其他人报名。
func (s *ActivityService) FailRegistration(ctx context.Context, activityID, accountID uint) error {
	if s == nil || s.cache == nil {
		return errors.New("redis is required for activity registration")
	}
	rollbackErr := s.cancelReservation(ctx, activityID, accountID)
	resultErr := s.setRegistrationResult(ctx, activityID, accountID, "FAILED")
	return errors.Join(rollbackErr, resultErr)
}

// redis设置报名结果
func (s *ActivityService) setRegistrationResult(ctx context.Context, activityID, accountID uint, result string) error {
	if s.cache == nil {
		return errors.New("redis is required for activity registration")
	}
	return s.cache.SetBytes(
		ctx,
		activityResultKey(s.cache, activityID, accountID),
		[]byte(result),
		24*time.Hour,
	)
}

// RegistrationResult 优先读 Redis 状态,记录插入成功后会在redis里加入结果
// 未命中时由 MySQL 报名记录和 Redis Set 兜底。
func (s *ActivityService) RegistrationResult(ctx context.Context, activityID, accountID uint) (string, error) {
	if s == nil || s.repo == nil || s.cache == nil || activityID == 0 || accountID == 0 {
		return "", ErrInvalidActivity
	}
	if value, err := s.cache.GetBytes(ctx, activityResultKey(s.cache, activityID, accountID)); err == nil {
		return string(value), nil
	} else if !rediscache.IsMiss(err) {
		return s.resultFromDatabase(ctx, activityID, accountID)
	}
	return s.resultFromDatabase(ctx, activityID, accountID)
}

// 从数据库查报名结果
func (s *ActivityService) resultFromDatabase(ctx context.Context, activityID, accountID uint) (string, error) {
	hasRegistration, err := s.repo.HasOrder(ctx, activityID, accountID)
	if err != nil {
		return "", err
	}
	if hasRegistration {
		return "SUCCESS", nil
	}
	//PENDING: Redis 预报名已成功，等待 MQ Worker 落 MySQL
	pending, err := s.cache.SIsMember(ctx, activityAccountsKey(s.cache, activityID), accountID)
	if err != nil {
		return "", err
	}
	if pending {
		return "PENDING", nil
	}
	return "NOT_REGISTERED", nil
}
