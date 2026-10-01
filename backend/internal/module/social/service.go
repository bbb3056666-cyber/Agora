package social

import (
	"context"
	"errors"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"log"
	"strconv"
	"time"
)

type SocialService struct {
	repo        *SocialRepository
	accountrepo *account.AccountRepository
	socialMQ    *rabbitmq.SocialMQ
	cache       *rediscache.Client
}

func NewSocialService(repo *SocialRepository, accountrepo *account.AccountRepository, socialMQ *rabbitmq.SocialMQ, cache *rediscache.Client) *SocialService {
	return &SocialService{repo: repo, accountrepo: accountrepo, socialMQ: socialMQ, cache: cache}
}

func (s *SocialService) isLegal(ctx context.Context, social *Social) (bool, error) {
	//关注者存在
	if _, err := s.accountrepo.FindByID(ctx, social.FollowerID); err != nil {
		return false, err
	}
	//被关注者存在
	if _, err := s.accountrepo.FindByID(ctx, social.VloggerID); err != nil {
		return false, err
	}
	//自己不能关注自己
	if social.FollowerID == social.VloggerID {
		return false, errors.New("can not follow self")
	}
	//查询是否关注过
	isFollowed, err := s.repo.IsFollowed(ctx, social)
	if err != nil {
		return false, err
	}
	return isFollowed, nil
}

// ✅
// 关注
func (s *SocialService) Follow(ctx context.Context, social *Social) error {
	if isFollowed, err := s.isLegal(ctx, social); err != nil || isFollowed {
		if err != nil {
			return err
		}
		return errors.New("already followed")
	}

	// 关注关系和被关注者的冗余粉丝数必须一起提交：任一失败都会回滚。
	if err := s.repo.FollowAndIncrementFollowerCount(ctx, social); err != nil {
		return err
	}

	// MySQL 已提交后再同步已存在的 Redis 缓存；缓存失败不能回滚真实业务数据。
	s.incrementFollowerCountCache(context.Background(), social.VloggerID)
	// 粉丝数可能刚好跨过阈值，更新作者的推/拉模式标记。
	s.syncPullAuthorMarker(context.Background(), social.VloggerID)

	// 更新该用户的关注列表缓存
	s.refreshFollowingIDsCache(context.Background(), social.FollowerID)

	// 最后发 MQ，失败只记日志不影响业务
	if s.socialMQ != nil {
		if err := s.socialMQ.Follow(ctx, social.FollowerID, social.VloggerID); err != nil {
			log.Printf("social MQ Follow 发布失败: %v", err)
		}
	}
	return nil
}

// ✅
// 取关
func (s *SocialService) Unfollow(ctx context.Context, social *Social) error {
	if isFollowed, err := s.isLegal(ctx, social); err != nil || !isFollowed {
		if err != nil {
			return err
		}
		return errors.New("not followed")
	}

	// 删除关系与粉丝数减一在同一个事务内完成，防止计数与 socials 不一致。
	if err := s.repo.UnfollowAndDecrementFollowerCount(ctx, social); err != nil {
		return err
	}

	// MySQL 已提交后再同步已存在的 Redis 缓存；缓存未命中时由读侧按 MySQL 回填。
	s.decrementFollowerCountCache(context.Background(), social.VloggerID)
	// 取关也可能让作者从拉模式切回推模式。
	s.syncPullAuthorMarker(context.Background(), social.VloggerID)

	// 删除关注列表缓存中的记录
	s.removeFollowingIDFromCache(context.Background(), social.FollowerID, social.VloggerID)

	// 最后发 MQ
	if s.socialMQ != nil {
		if err := s.socialMQ.UnFollow(ctx, social.FollowerID, social.VloggerID); err != nil {
			log.Printf("social MQ UnFollow 发布失败: %v", err)
		}
	}
	return nil
}

// refreshFollowingIDsCache 在关注成功后，从 MySQL 读取完整关注列表并覆盖写入 Redis。
func (s *SocialService) refreshFollowingIDsCache(ctx context.Context, followerID uint) {
	if s.cache == nil {
		return
	}

	vloggerIDs, err := s.repo.ListFollowingIDs(ctx, followerID)
	if err != nil {
		log.Printf("查询关注列表以回填缓存失败: followerID=%d, err=%v", followerID, err)
		return
	}

	if err := s.writeFollowingIDsCache(ctx, followerID, vloggerIDs); err != nil {
		return
	}
}

// removeFollowingIDFromCache 在取关成功后，仅删除该作者 ID。
// Key 不存在时 SREM 会忽略，后续由读侧从 MySQL 重建。
func (s *SocialService) removeFollowingIDFromCache(ctx context.Context, followerID, vloggerID uint) {
	if s.cache == nil {
		return
	}

	followingKey := s.cache.Key("social:following:%d", followerID)
	if err := s.cache.SRem(ctx, followingKey, vloggerID); err != nil {
		log.Printf(
			"删除关注列表缓存成员失败: followerID=%d, vloggerID=%d, err=%v",
			followerID, vloggerID, err,
		)
	}
}

// incrementFollowerCountCache 只更新已被读侧回填的粉丝数缓存。
// Redis 缓存不是事实来源；未命中时不创建错误的初始值，之后由 MySQL follower_count 回填。
func (s *SocialService) incrementFollowerCountCache(ctx context.Context, vloggerID uint) {
	if s.cache == nil {
		return
	}

	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	cacheKey := s.cache.Key("social:follower_count:%d", vloggerID)
	if _, err := s.cache.IncrIfExists(cacheCtx, cacheKey); err != nil {
		log.Printf("更新粉丝数缓存失败: vloggerID=%d, err=%v", vloggerID, err)
	}
}

// decrementFollowerCountCache 与关注成功后的缓存递增相反。
func (s *SocialService) decrementFollowerCountCache(ctx context.Context, vloggerID uint) {
	if s.cache == nil {
		return
	}

	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	cacheKey := s.cache.Key("social:follower_count:%d", vloggerID)
	if _, err := s.cache.DecrIfExists(cacheCtx, cacheKey); err != nil {
		log.Printf("更新粉丝数缓存失败: vloggerID=%d, err=%v", vloggerID, err)
	}
}

// SyncPullAuthorMarker 按给定粉丝数维护大 V 集合。Timeline Consumer 调用失败时可 Nack，
// 关注/取关调用失败只记日志，因为此时 MySQL 事务已经提交，不能再回滚真实关系。
func (s *SocialService) SyncPullAuthorMarker(ctx context.Context, vloggerID uint, followerCount int64) error {
	if s.cache == nil {
		return errors.New("redis cache is not initialized")
	}
	markerKey := s.cache.Key("feed:pull_authors")
	if followerCount >= PullModeFollowerThreshold {
		return s.cache.SAdd(ctx, markerKey, vloggerID)
	}
	return s.cache.SRem(ctx, markerKey, vloggerID)
}

// syncPullAuthorMarker 是关注/取关后的尽力同步版本；粉丝数从 MySQL 读取，避免缓存短暂不一致。
func (s *SocialService) syncPullAuthorMarker(ctx context.Context, vloggerID uint) {
	if s.cache == nil {
		return
	}
	count, err := s.accountrepo.GetFollowerCount(ctx, vloggerID)
	if err != nil {
		log.Printf("查询粉丝数以更新推拉标记失败: vloggerID=%d, err=%v", vloggerID, err)
		return
	}
	markerCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := s.SyncPullAuthorMarker(markerCtx, vloggerID, count); err != nil {
		log.Printf("更新推拉模式标记失败: vloggerID=%d, err=%v", vloggerID, err)
	}
}

//// 删除redis中的关注列表
//// v1:feed:listByFollowing:limit=10:accountID=123:before=1753281300
//func (s *SocialService) invalidateFollowingFeedCache(ctx context.Context, accountID uint) {
//	if s.cache == nil {
//		return
//	}
//	//v1:feed:listByFollowing:*:accountID=1:*
//	pattern := s.cache.Key("feed:listByFollowing:*:accountID=%d:*", accountID)
//	//按模式删除所有key
//	if err := s.cache.DelByPattern(ctx, pattern); err != nil {
//		log.Printf("失效 Following 缓存失败: accountID=%d, err=%v", accountID, err)
//	}
//}

// ✅
// 返回粉丝列表
func (s *SocialService) GetAllFollowers(ctx context.Context, VloggerID uint) ([]*account.Account, error) {
	//确认用户存在
	_, err := s.accountrepo.FindByID(ctx, VloggerID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetAllFollowers(ctx, VloggerID)
}

// ListFollowerIDsAfterID 为 Timeline Worker 提供游标分页的粉丝 ID 查询。
func (s *SocialService) ListFollowerIDsAfterID(ctx context.Context, vloggerID, afterRelationID uint, limit int) ([]uint, uint, error) {
	return s.repo.ListFollowerIDsAfterID(ctx, vloggerID, afterRelationID, limit)
}

// ✅
// 返回关注列表
func (s *SocialService) GetAllVloggers(ctx context.Context, FollowerID uint) ([]*account.Account, error) {
	//确认用户存在
	_, err := s.accountrepo.FindByID(ctx, FollowerID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetAllVloggers(ctx, FollowerID)
}

// GetFollowerCount 用 Cache Aside 读取粉丝数。
// Redis 只是加速层：未命中、故障或脏值时回源 MySQL accounts.follower_count，
// 再回填 Redis；不再对 socials 做 COUNT(*)。
func (s *SocialService) GetFollowerCount(ctx context.Context, vloggerID uint) (int64, error) {
	cacheKey := ""
	if s.cache != nil {
		cacheKey = s.cache.Key("social:follower_count:%d", vloggerID)
		if cached, err := s.cache.GetBytes(ctx, cacheKey); err == nil {
			if count, parseErr := strconv.ParseInt(string(cached), 10, 64); parseErr == nil && count >= 0 {
				return count, nil
			}
		}
	}

	count, err := s.accountrepo.GetFollowerCount(ctx, vloggerID)
	if err != nil {
		return 0, err
	}

	if s.cache != nil {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := s.cache.SetBytes(cacheCtx, cacheKey, []byte(strconv.FormatInt(count, 10)), 0); err != nil {
			log.Printf("回填粉丝数缓存失败: vloggerID=%d, err=%v", vloggerID, err)
		}
	}
	return count, nil
}

// ✅
// 查关注数
func (s *SocialService) CountVloggers(ctx context.Context, followerID uint) (int64, error) {
	return s.repo.CountVloggers(ctx, followerID)
}

func (s *SocialService) IsFollowed(ctx context.Context, social *Social) (bool, error) {
	_, err := s.accountrepo.FindByID(ctx, social.FollowerID)
	if err != nil {
		return false, err
	}
	_, err = s.accountrepo.FindByID(ctx, social.VloggerID)
	if err != nil {
		return false, err
	}
	return s.repo.IsFollowed(ctx, social)
}

// GetFollowingIDs 用 Cache Aside 获取用户关注的作者 ID 列表。
func (s *SocialService) GetFollowingIDs(ctx context.Context, followerID uint) ([]uint, error) {
	followingKey := ""
	readyKey := ""

	if s.cache != nil {
		followingKey = s.cache.Key("social:following:%d", followerID)
		readyKey = s.cache.Key("social:following:ready:%d", followerID)

		// ready 存在，说明 Set 已从 MySQL 完整构建过；
		// 即使 Set 为空，也无需回源数据库。
		if _, err := s.cache.GetBytes(ctx, readyKey); err == nil {
			members, err := s.cache.SMembers(ctx, followingKey)
			if err == nil {
				ids := make([]uint, 0, len(members))
				valid := true

				for _, member := range members {
					id, parseErr := strconv.ParseUint(member, 10, 64)
					if parseErr != nil {
						valid = false
						break
					}
					ids = append(ids, uint(id))
				}

				if valid {
					return ids, nil
				}
			}
		}
	}

	// Redis 未命中、故障或缓存脏数据：MySQL 是事实来源。
	ids, err := s.repo.ListFollowingIDs(ctx, followerID)
	if err != nil {
		return nil, err
	}

	// 回填缓存失败不影响本次正常返回。
	if s.cache != nil {
		if err := s.writeFollowingIDsCache(ctx, followerID, ids); err != nil {
			log.Printf("回填关注列表缓存失败: followerID=%d, err=%v", followerID, err)
		}
	}

	return ids, nil
}

func (s *SocialService) writeFollowingIDsCache(ctx context.Context, followerID uint, vloggerIDs []uint) error {
	if s.cache == nil {
		return nil
	}
	followingKey := s.cache.Key("social:following:%d", followerID)
	//用于区分空缓存(无关注)和没缓存
	readyKey := s.cache.Key("social:following:ready:%d", followerID)

	// 先清掉旧缓存，保证后面写入的是完整列表。
	if err := s.cache.Del(ctx, followingKey); err != nil {
		log.Printf("清理关注列表缓存失败: followerID=%d, err=%v", followerID, err)
		return err
	}
	if err := s.cache.Del(ctx, readyKey); err != nil {
		log.Printf("清理关注列表 ready 标记失败: followerID=%d, err=%v", followerID, err)
		return err
	}

	if len(vloggerIDs) > 0 {
		members := make([]any, len(vloggerIDs))
		for i, id := range vloggerIDs {
			members[i] = id
		}
		if err := s.cache.SAdd(ctx, followingKey, members...); err != nil {
			log.Printf("回填关注列表缓存失败: followerID=%d, err=%v", followerID, err)
			return err
		}
	}

	// 即使关注列表为空，也写 ready，用来区分“空列表”和“缓存未命中”。
	if err := s.cache.SetBytes(ctx, readyKey, []byte("1"), 0); err != nil {
		log.Printf("写入关注列表 ready 标记失败: followerID=%d, err=%v", followerID, err)
		return err
	}
	return nil
}
