package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/like"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"log"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"
	redis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// FollowingReader 只暴露关注流读取所需的能力，避免 Feed 依赖 Social 的具体服务实现。
// 测试未涉及关注流时可以传 nil，此时拉模式不追加作者发件箱候选。
type FollowingReader interface {
	GetFollowingIDs(ctx context.Context, followerID uint) ([]uint, error)
}

const (
	popularityWindowMinutes   = 60
	popularityHalfLifeMinutes = 30.0
	popularityEmptyTTL        = 5 * time.Second
)

// ageMinutes 为分钟桶距当前快照的年龄。
// 半衰期为 30 分钟：当前桶权重 1，30 分钟前 0.5，60 分钟前 0.25。
func popularityTimeWeight(ageMinutes int) float64 {
	return math.Pow(0.5, float64(ageMinutes)/popularityHalfLifeMinutes)
}

type FeedService struct {
	followingReader FollowingReader      // 查询用户关注的作者 ID，用于拉模式补齐大 V 发件箱
	feedRepository  *FeedRepository      //L3: MySQL                  → 毫秒级，持久化
	likeRepository  *like.LikeRepository //L3: MySQL                  → 毫秒级，持久化
	redisCache      *rediscache.Client   //L2: Redis (redisCache)     → 微秒级，跨进程共享
	localCache      *cache.Cache         //L1: 本地内存 (localCache)  → 纳秒级，只在当前进程内有效
	cacheTTL        time.Duration
	requestGroup    singleflight.Group // 内存里的一个"调度箱"
	//同一个 key 的 Do 调用只有一个执行，其他全都阻塞等结果
}

type CachedFeedData struct {
	PublicVideos []content.Content `json:"public_videos"`
}

func NewFeedService(repo *FeedRepository, likeRepo *like.LikeRepository, redisCache *rediscache.Client, followingReader FollowingReader) *FeedService {
	return &FeedService{
		followingReader: followingReader,
		feedRepository:  repo,
		likeRepository:  likeRepo,
		redisCache:      redisCache,
		localCache:      cache.New(3*time.Second, 5*time.Second), //默认过期 3 秒  每 5 秒清理一次过期 key
		cacheTTL:        24 * time.Hour,
	}

}

// 根据id批量获取视频信息,使用三级缓存
func (f *FeedService) GetContentByIDs(ctx context.Context, videoIDs []uint) ([]*content.Content, error) {
	// GetContentByIDs 批量获取视频信息
	// 采用 L1(本地缓存) -> L2(Redis) -> L3(MySQL) 三级架构
	if len(videoIDs) == 0 {
		return []*content.Content{}, nil
	}

	//map存放最终结果
	videoMap := make(map[uint]*content.Content)

	//L1:本地缓存
	var missedL1 []uint //记录本地进程内缓存就未命中的id

	for _, id := range videoIDs {
		//用同样的key v1:video:entity:1在进程内存视频信息
		cacheKey := f.redisCache.Key("content:entity:%d", id)
		if f.localCache != nil { //有进程内部缓存
			if v, found := f.localCache.Get(cacheKey); found { //有这个key
				if data, ok := v.(content.Content); ok { //断言成功
					videoMap[id] = &data
					continue
				}
			}
		}
		// 任何环节失败,记录未命中的 ID，准备进入下一级缓存
		missedL1 = append(missedL1, id)
	}

	if len(missedL1) == 0 {
		//全部命中进程缓存
		//将视频按时间排序返回,最晚发的视频在前
		return buildOrderedResult(videoIDs, videoMap), nil
	}

	//L2:redis
	var missedL2 []uint

	//将视频在redis中所有的key收集在切片中
	cacheKeys := make([]string, 0, len(missedL1))
	for _, id := range missedL1 {
		cacheKeys = append(cacheKeys, f.redisCache.Key("content:entity:%d", id))
	}

	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//批量获取值
	results, err := f.redisCache.MGet(cacheCtx, cacheKeys...)
	cancel()

	if err == nil {
		for i, res := range results {
			id := missedL1[i]
			if res != nil { //有key,能找到视频缓存
				//所有接口类型变量都会保存：
				//动态类型 + 动态值,都为nil时才为nil
				if str, ok := res.(string); ok { //断言为string
					var v content.Content
					//反序列化为Video对象(string({"id":1,"author_id":2...})->byte[]->Content)
					if err := json.Unmarshal([]byte(str), &v); err == nil {
						videoMap[id] = &v
						// 回写更新 L1 本地缓存
						if f.localCache != nil {
							f.localCache.Set(cacheKeys[i], v, 5*time.Second)
						}
						continue
					}
				}
			}
			//任何环节失败都进入第三级缓存
			missedL2 = append(missedL2, id)
		}
	} else {
		// 如果 Redis 挂了或者超时了，全部降级到 L3
		missedL2 = missedL1
		log.Printf("L2 Redis MGet 失败，全部降级到 MySQL: %v", err)
	}

	//len(nil)等于0
	if len(missedL2) == 0 {
		return buildOrderedResult(videoIDs, videoMap), nil
	}

	//二级缓存也有没命中的
	//L3:MySQL
	var wg sync.WaitGroup
	var mu sync.Mutex
	//拆开是为了独立失败隔离 + 每个视频异步单独回写缓存
	for _, id := range missedL2 {
		wg.Add(1)
		go func(videoID uint) {
			defer wg.Done()
			sfKey := f.redisCache.Key("sf:entity:%d", videoID)

			//单个进程内的多个协程同一操作合一,多个协程共享一个结果
			//func (g *Group) Do(key string, fn func() (interface{}, error)) (v interface{}, err error, shared bool)
			v, err, _ := f.requestGroup.Do(sfKey, func() (interface{}, error) {
				videoList, err := f.feedRepository.GetByIDs(ctx, []uint{videoID})

				if err != nil || len(videoList) == 0 {
					return nil, err
				}

				safeCopy := *videoList[0]
				cacheKey := f.redisCache.Key("content:entity:%d", safeCopy.ID)
				if b, err := json.Marshal(safeCopy); err == nil {
					//异步回写redis,不受父 goroutine 控制
					go func(k string, b []byte) {
						setCtx, setCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
						defer setCancel()

						_ = f.redisCache.SetBytes(setCtx, k, b, time.Hour)
					}(cacheKey, b)
				}
				return videoList[0], err
			})

			//所有使用相同key的协程拿到的v是同一个地址
			//为了防止修改后全变,每个goroutine需要复制一份

			if err == nil && v != nil {
				//Go 编译器检测到 safeCopy 被通过 map 引用到外部，
				//不把它放在栈上，而是放堆——goroutine 返回也不会回收
				safeCopy := *(v.(*content.Content))
				//多个 goroutine 同时写同一个 videoMap——Go 的 map 并发写会 panic
				mu.Lock()
				videoMap[id] = &safeCopy
				mu.Unlock()
				//写入进程缓存,五秒自动删除
				f.localCache.Set(f.redisCache.Key("content:entity:%d", safeCopy.ID), safeCopy, 5*time.Second)
			}
		}(id)
	}
	//等待所有查库任务结束
	wg.Wait()
	return buildOrderedResult(videoIDs, videoMap), nil
}

// ListLatest 查询最新视频 (冷热分离 + 游标分页),redis使用zset
func (f *FeedService) ListLatest(ctx context.Context, limit int, latestBefore time.Time, viewerAccountID uint) (ListLatestResponse, error) {
	//redis失效
	if f.redisCache == nil {
		return f.listLatestFromDB(ctx, limit, latestBefore, viewerAccountID)
	}

	// 获取 ZSET 中最老的一条数据
	//v1:feed:global_timeline
	//获得下标最小的[]Z就一个元素,也就是最老的视频id
	zsetTail, err := f.redisCache.ZRangeWithScores(ctx, f.redisCache.Key("feed:global_timeline"), 0, 0)

	if err != nil {
		//redis查询出错,直接降级,保证功能能用
		return f.listLatestFromDB(ctx, limit, latestBefore, viewerAccountID)
	}

	//zset中没有数据,重建zset
	if len(zsetTail) == 0 {
		//全局静态锁：无视所有用户的不同时间戳游标
		//v1:sf:fallback:global_timeline_rebuild
		sfKey := f.redisCache.Key("sf:fallback:global_timeline_rebuild")

		//key 没人跑？ → 第一个 goroutine 跑 fn → 存结果
		//key 已经有人在跑？ → 挂起 → 等第一个人返回 → 共享结果
		//fn 跑完了？ → 所有挂起的唤醒 → 全部返回同结果
		v, err, _ := f.requestGroup.Do(sfKey, func() (interface{}, error) {
			// 无视游标，直接去 MySQL 捞最新的 1000 条
			dbVideos, err := f.feedRepository.ListLatest(ctx, 1000, time.Time{})
			if err != nil {
				return nil, err
			}
			if len(dbVideos) == 0 {
				return "EMPTY_DB", nil // 防无限递归
			}

			// 重建 ZSET
			bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var zElements []redis.Z
			for _, v := range dbVideos {
				zElements = append(zElements, redis.Z{
					Score:  float64(v.CreateTime.UnixMilli()),
					Member: fmt.Sprintf("%d", v.ID),
				})
			}
			if err := f.redisCache.ZAdd(bgCtx, f.redisCache.Key("feed:global_timeline"), zElements...); err != nil {
				return nil, err
			}
			return "SUCCESS", nil
		})

		if err != nil {
			//写入zset错误,直接降级,防止无限递归
			return f.listLatestFromDB(ctx, limit, latestBefore, viewerAccountID)
		}
		if v == "EMPTY_DB" {
			return ListLatestResponse{HasMore: false}, nil
		}

		// 写入zset成功,让所有被阻塞的请求重新查一遍
		return f.ListLatest(ctx, limit, latestBefore, viewerAccountID)
	}

	//zset中有数据
	watermark := int64(zsetTail[0].Score)
	var reqTime int64
	if !latestBefore.IsZero() {
		reqTime = latestBefore.UnixMilli()
	} else {
		reqTime = time.Now().UnixMilli()
	}

	//最终结果
	var baseVideos []*content.Content

	if reqTime <= watermark { //冷数据降级查库
		// 针对个别用户的防并发（此时可以用时间戳做锁，因为冷尾流量极小）
		// 纯粹的性能优化——合并重复请求，减少打到数据库上的查询次数
		// 并发了也不会有问题
		sfKey := f.redisCache.Key("sf:cold:listLatest:%d:%d", limit, reqTime)
		v, err, _ := f.requestGroup.Do(sfKey, func() (interface{}, error) {
			return f.feedRepository.ListLatest(ctx, limit, latestBefore)
		})
		if err != nil {
			return ListLatestResponse{}, err
		}
		//返回的[]*content.Content被interface{}类型的v接住,需要断言成原类型
		baseVideos = v.([]*content.Content)
		// 不回写 ZSET，防止冷数据污染热点时间线

	} else { // 热数据直接查redis
		//第一次查,直接从顶(最新)查
		var maxScore string
		if !latestBefore.IsZero() {
			//有时间,设置为上一次时间-1
			maxScore = fmt.Sprintf("%d", reqTime-1) // 防重复
		} else {
			maxScore = "+inf"
		}

		//降序返回指定分数(上一次时间-1,无下限)范围内的记录,-inf表示无限往下取,取limit条
		videoIDsStr, err := f.redisCache.ZRevRangeByScore(ctx, f.redisCache.Key("feed:global_timeline"), maxScore, "-inf", 0, int64(limit))
		if err != nil {
			return ListLatestResponse{}, err
		}

		//id由字符串转成数字存入切片,分数最高的id排最前,顺序为从晚到早
		var videoIDs []uint
		for _, idStr := range videoIDsStr {
			if id, err := strconv.ParseUint(idStr, 10, 64); err == nil {
				videoIDs = append(videoIDs, uint(id))
			}
		}

		if len(videoIDs) > 0 {
			baseVideos, err = f.GetContentByIDs(ctx, videoIDs)
			if err != nil {
				return ListLatestResponse{}, err
			}
		}

		// 刚好击穿了冷热边界
		if len(baseVideos) < limit {
			remainLimit := limit - len(baseVideos) // 计算还差几个

			//数据库查询的起始时间
			var coldCursor time.Time
			if len(baseVideos) > 0 {
				coldCursor = baseVideos[len(baseVideos)-1].CreateTime
			} else {
				coldCursor = latestBefore
			}

			sfKey := f.redisCache.Key("sf:stitch:listLatest:%d:%d", remainLimit, coldCursor.UnixMilli())
			v, err, _ := f.requestGroup.Do(sfKey, func() (interface{}, error) {
				return f.feedRepository.ListLatest(ctx, remainLimit, coldCursor)
			})

			if err == nil {
				coldVideos := v.([]*content.Content)
				//将冷视频追加到后面
				baseVideos = append(baseVideos, coldVideos...)
			}
		}
	}

	var nextTime int64
	if len(baseVideos) > 0 {
		// 将本页最后一条视频的时间作为下一次请求的游标
		nextTime = baseVideos[len(baseVideos)-1].CreateTime.UnixMilli()
	}
	hasMore := len(baseVideos) == limit

	feedVideos, err := f.buildFeedVideos(ctx, baseVideos, viewerAccountID)
	if err != nil {
		return ListLatestResponse{}, err
	}

	return ListLatestResponse{
		ContentList: feedVideos,
		NextTime:    nextTime,
		HasMore:     hasMore,
	}, nil
}

// ✅
// redis失效直接走gorm
func (f *FeedService) listLatestFromDB(ctx context.Context, limit int, latestBefore time.Time, viewerAccountID uint) (ListLatestResponse, error) {
	//获取新一页视频
	videos, err := f.feedRepository.ListLatest(ctx, limit, latestBefore)
	if err != nil {
		return ListLatestResponse{}, err
	}
	//得到本页视频信息切片(加上了是否被用户点赞过)
	feedVideos, err := f.buildFeedVideos(ctx, videos, viewerAccountID)
	if err != nil {
		return ListLatestResponse{}, err
	}
	var nextTime int64
	if len(videos) > 0 {
		//nextTime设为最后一个视频的发布时间的毫秒值
		nextTime = videos[len(videos)-1].CreateTime.UnixMilli()
	}
	return ListLatestResponse{
		ContentList: feedVideos,
		NextTime:    nextTime,
		HasMore:     len(videos) == limit, //判断视频有没有刷完
	}, nil
}

// ✅
// 按照点赞数查询视频
func (f *FeedService) ListLikesCount(ctx context.Context, limit int, cursor *LikesCountCursor, viewerAccountID uint) (ListLikesCountResponse, error) {
	//根据点赞数和游标(如果有)查视频,点赞数降序,id降序
	videos, err := f.feedRepository.ListLikesCountWithCursor(ctx, limit, cursor)
	if err != nil {
		return ListLikesCountResponse{}, err
	}
	//视频查没查完(现实不可能)
	hasMore := len(videos) == limit
	//构建并返回本页视频信息切片
	feedVideos, err := f.buildFeedVideos(ctx, videos, viewerAccountID)
	if err != nil {
		return ListLikesCountResponse{}, err
	}
	resp := ListLikesCountResponse{
		ContentList: feedVideos,
		HasMore:     hasMore,
	}
	if len(videos) > 0 {
		lastVideo := videos[len(videos)-1]
		nextLikesCountBefore := lastVideo.LikesCount
		nextIDBefore := lastVideo.ID
		resp.NextLikesCountBefore = &nextLikesCountBefore
		resp.NextIDBefore = &nextIDBefore
	}
	return resp, nil
}

// ✅
// 按照关注列表查询视频,有redis和数据库两种方案
func (f *FeedService) ListByFollowing(ctx context.Context, limit int, latestBefore time.Time, viewerAccountID uint) (ListByFollowingResponse, error) {
	listByFollowingFromDB := func(ctx context.Context, limit int, latestBefore time.Time, viewerAccountID uint) (ListByFollowingResponse, error) {
		videos, err := f.feedRepository.ListByFollowing(ctx, limit, viewerAccountID, latestBefore)
		if err != nil {
			return ListByFollowingResponse{}, err
		}
		var nextTime int64
		if len(videos) > 0 {
			nextTime = videos[len(videos)-1].CreateTime.UnixMilli()
		}
		feedVideos, err := f.buildFeedVideos(ctx, videos, viewerAccountID)
		if err != nil {
			return ListByFollowingResponse{}, err
		}
		return ListByFollowingResponse{
			ContentList: feedVideos,
			NextTime:    nextTime,
			HasMore:     len(videos) == limit,
		}, nil
	}

	// 未登录/Redis不可用，直接降级走DB
	if viewerAccountID == 0 || f.redisCache == nil {
		return listByFollowingFromDB(ctx, limit, latestBefore, viewerAccountID)
	}

	// 用户个人关注流ZSET key
	timelineKey := f.redisCache.Key("feed:follower:%d", viewerAccountID)

	// 拿ZSET最老的一条，判断水位线
	zsetTail, err := f.redisCache.ZRangeWithScores(ctx, timelineKey, 0, 0)
	if err != nil {
		// Redis出错降级
		return listByFollowingFromDB(ctx, limit, latestBefore, viewerAccountID)
	}

	// ZSET为空，重建（第一次刷/缓存过期）
	if len(zsetTail) == 0 {
		sfKey := f.redisCache.Key("sf:fallback:following_timeline:%d", viewerAccountID)
		v, err, _ := f.requestGroup.Do(sfKey, func() (interface{}, error) {
			// 查DB拿关注的人最近1000条视频
			dbVideos, err := f.feedRepository.ListByFollowing(ctx, 1000, viewerAccountID, time.Time{})
			if err != nil {
				return nil, err
			}
			if len(dbVideos) == 0 {
				return "EMPTY_DB", nil
			}
			// 批量写入用户ZSET
			bgCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var zElements []redis.Z
			for _, vid := range dbVideos {
				zElements = append(zElements, redis.Z{
					Score:  float64(vid.CreateTime.UnixMilli()),
					Member: fmt.Sprintf("%d", vid.ID),
				})
			}
			_ = f.redisCache.ZAdd(bgCtx, timelineKey, zElements...)
			return "SUCCESS", nil
		})
		if err != nil {
			//写入zset失败,直接降级走db
			return listByFollowingFromDB(ctx, limit, latestBefore, viewerAccountID)
		}
		if v == "EMPTY_DB" {
			return ListByFollowingResponse{HasMore: false}, nil
		}
		// 重建成功重新查
		return f.ListByFollowing(ctx, limit, latestBefore, viewerAccountID)
	}

	watermark := int64(zsetTail[0].Score)
	reqTime := time.Now().UnixMilli()
	if !latestBefore.IsZero() {
		reqTime = latestBefore.UnixMilli()
	}

	var baseVideos []*content.Content
	if reqTime <= watermark {
		// 冷数据降级查DB
		sfKey := f.redisCache.Key("sf:cold:listFollowing:%d:%d:%d", viewerAccountID, limit, reqTime)
		v, err, _ := f.requestGroup.Do(sfKey, func() (interface{}, error) {
			return f.feedRepository.ListByFollowing(ctx, limit, viewerAccountID, latestBefore)
		})
		if err != nil {
			return ListByFollowingResponse{}, err
		}
		baseVideos = v.([]*content.Content)
	} else {
		// 热数据直接从用户ZSET取
		maxScore := "+inf"
		if !latestBefore.IsZero() {
			maxScore = fmt.Sprintf("%d", reqTime-1)
		}
		//从收件箱取limit条
		videoIDsStr, err := f.redisCache.ZRevRangeByScore(ctx, timelineKey, maxScore, "-inf", 0, int64(limit))
		if err != nil {
			return ListByFollowingResponse{}, err
		}
		var videoIDs []uint
		for _, idStr := range videoIDsStr {
			if id, err := strconv.ParseUint(idStr, 10, 64); err == nil {
				videoIDs = append(videoIDs, uint(id))
			}
		}

		// 大 V 不会把新视频 fan-out 到用户收件箱；从其作者发件箱补齐候选视频，再与收件箱候选在服务内归并。
		pullContentIDs, err := f.listPullAuthorCandidateIDs(ctx, viewerAccountID, maxScore, limit)
		if err != nil {
			return ListByFollowingResponse{}, err
		}
		//此时对于时间已经无序了
		videoIDs = append(videoIDs, pullContentIDs...)

		if len(videoIDs) > 0 {
			baseVideos, err = f.GetContentByIDs(ctx, videoIDs)
			if err != nil {
				return ListByFollowingResponse{}, err
			}
		}

		//去重,排序,取limit条
		baseVideos = mergeVideosByCreateTime(baseVideos, limit)

		// 不够补冷数据
		if len(baseVideos) < limit {
			remainLimit := limit - len(baseVideos)
			var coldCursor time.Time
			if len(baseVideos) > 0 {
				coldCursor = baseVideos[len(baseVideos)-1].CreateTime
			} else {
				coldCursor = latestBefore
			}
			sfKey := f.redisCache.Key("sf:stitch:listFollowing:%d:%d:%d", viewerAccountID, remainLimit, coldCursor.UnixMilli())
			v, err, _ := f.requestGroup.Do(sfKey, func() (interface{}, error) {
				return f.feedRepository.ListByFollowing(ctx, remainLimit, viewerAccountID, coldCursor)
			})
			if err == nil {
				coldVideos := v.([]*content.Content)
				baseVideos = append(baseVideos, coldVideos...)
			}
		}
	}

	var nextTime int64
	if len(baseVideos) > 0 {
		nextTime = baseVideos[len(baseVideos)-1].CreateTime.UnixMilli()
	}
	hasMore := len(baseVideos) == limit
	feedVideos, err := f.buildFeedVideos(ctx, baseVideos, viewerAccountID)
	if err != nil {
		return ListByFollowingResponse{}, err
	}

	return ListByFollowingResponse{
		ContentList: feedVideos,
		NextTime:    nextTime,
		HasMore:     hasMore,
	}, nil

	//纯依靠数据库的函数
	//doListByFollowingFromDB := func() (ListByFollowingResponse, error) {
	//	videos, err := f.feedRepository.ListByFollowing(ctx, limit, viewerAccountID, latestBefore)
	//	if err != nil {
	//		return ListByFollowingResponse{}, err
	//	}
	//	//这一页最晚时间,下一页最早时间
	//	var nextTime int64
	//	if len(videos) > 0 {
	//		nextTime = videos[len(videos)-1].CreateTime.Unix()
	//	} else {
	//		nextTime = 0
	//	}
	//	hasMore := len(videos) == limit
	//	//构建[]FeedContentItem
	//	feedVideos, err := f.buildFeedVideos(ctx, videos, viewerAccountID)
	//	if err != nil {
	//		return ListByFollowingResponse{}, err
	//	}
	//	resp := ListByFollowingResponse{
	//		ContentList: feedVideos,
	//		NextTime:  nextTime,
	//		HasMore:   hasMore,
	//	}
	//	return resp, nil
	//}

	//redis逻辑
	//var cacheKey string
	//if viewerAccountID != 0 && f.redisCache != nil {
	//	before := int64(0)
	//	if !latestBefore.IsZero() {
	//		before = latestBefore.Unix()
	//	}
	//	cacheKey = f.redisCache.Key("feed:listByFollowing:limit=%d:accountID=%d:before=%d", limit, viewerAccountID, before)
	//	cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//	defer cancel()
	//
	//	//第一次查redis
	//	b, err := f.redisCache.GetBytes(cacheCtx, cacheKey)
	//	if err == nil {
	//		var cached ListByFollowingResponse
	//		if err := json.Unmarshal(b, &cached); err == nil {
	//			return cached, nil
	//		}
	//	} else if rediscache.IsMiss(err) { // 缓存未命中
	//		lockKey := "lock:" + cacheKey
	//		// 缓存未命中，尝试加锁
	//		token, locked, _ := f.redisCache.Lock(cacheCtx, lockKey, 500*time.Millisecond)
	//		if locked {
	//			//释放锁
	//			defer func() { _ = f.redisCache.Unlock(context.Background(), lockKey, token) }()
	//			//双检锁（Double-Checked Locking）模式
	//			//为什么要二次检查?
	//			if b, err := f.redisCache.GetBytes(cacheCtx, cacheKey); err == nil {
	//				var cached ListByFollowingResponse
	//				if err := json.Unmarshal(b, &cached); err == nil {
	//					return cached, nil
	//				}
	//			} else {
	//				// 从数据库中查询
	//				resp, err := doListByFollowingFromDB()
	//				if err != nil {
	//					return ListByFollowingResponse{}, err
	//				}
	//				if b, err := json.Marshal(resp); err == nil {
	//					//写入redis缓存
	//					_ = f.redisCache.SetBytes(cacheCtx, cacheKey, b, f.cacheTTL)
	//				}
	//				return resp, nil
	//			}
	//		} else {
	//			//没拿到锁的循环五次,每次20ms,反复等待拿到锁的写入redis
	//			for i := 0; i < 5; i++ {
	//				time.Sleep(20 * time.Millisecond)
	//				if b, err := f.redisCache.GetBytes(cacheCtx, cacheKey); err == nil {
	//					var cached ListByFollowingResponse
	//					if err := json.Unmarshal(b, &cached); err == nil {
	//						return cached, nil
	//					}
	//				}
	//			}
	//		}
	//	}
	//}

	//redis没用,直接查数据库
	//resp, err := doListByFollowingFromDB()
	//if err != nil {
	//	return ListByFollowingResponse{}, err
	//}
	//if cacheKey != "" {
	//	//五次循环都没查到的情况,自己写入redis
	//	if b, err := json.Marshal(resp); err == nil {
	//		cacheCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	//		defer cancel()
	//		//没写进redis也没关系,影响不大直接忽略
	//		_ = f.redisCache.SetBytes(cacheCtx, cacheKey, b, f.cacheTTL)
	//	}
	//}
	//return resp, nil
}

// listPullAuthorCandidateIDs 找出当前用户关注的大 V，
// 并从每位大 V 的发件箱取本页候选视频。
func (f *FeedService) listPullAuthorCandidateIDs(
	ctx context.Context,
	viewerAccountID uint,
	maxScore string,
	limit int,
) ([]uint, error) {
	if f.followingReader == nil {
		return []uint{}, nil
	}

	authorIDs, err := f.followingReader.GetFollowingIDs(ctx, viewerAccountID)
	if err != nil {
		return nil, err
	}
	if len(authorIDs) == 0 {
		// SMISMEMBER 至少需要一个成员；空关注列表本身是合法结果。
		return []uint{}, nil
	}

	members := make([]any, len(authorIDs))
	for i, authorID := range authorIDs {
		members[i] = authorID
	}

	pullFlags, err := f.redisCache.SMIsMember(
		ctx,
		f.redisCache.Key("feed:pull_authors"),
		members...,
	)
	if err != nil {
		return nil, err
	}

	candidateIDs := make([]uint, 0)
	for i, authorID := range authorIDs {
		if !pullFlags[i] {
			continue
		}

		//从大v视频集合里取视频
		ids, err := f.redisCache.ZRevRangeByScore(
			ctx,
			f.redisCache.Key("feed:author:%d", authorID),
			maxScore,
			"-inf",
			0,
			int64(limit),
		)
		if err != nil {
			return nil, err
		}

		for _, idStr := range ids {
			id, err := strconv.ParseUint(idStr, 10, 64)
			if err != nil {
				continue
			}
			candidateIDs = append(candidateIDs, uint(id))
		}
	}

	return candidateIDs, nil
}

// mergeVideosByCreateTime 对来自用户收件箱和多个作者发件箱的候选视频排序,取limit条
func mergeVideosByCreateTime(videos []*content.Content, limit int) []*content.Content {
	//对视频去重
	unique := make(map[uint]*content.Content, len(videos))
	for _, v := range videos {
		if v != nil {
			unique[v.ID] = v
		}
	}

	merged := make([]*content.Content, 0, len(unique))
	for _, v := range unique {
		merged = append(merged, v)
	}

	//按时间排序
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].CreateTime.Equal(merged[j].CreateTime) {
			return merged[i].ID > merged[j].ID
		}
		return merged[i].CreateTime.After(merged[j].CreateTime)
	})

	//取limit条
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// ✅
// 热度,时间,id
func (f *FeedService) ListByPopularity(
	ctx context.Context,
	limit int,
	reqAsOf int64,
	offset int,
	viewerAccountID uint,
	latestPopularity int64,
	latestBefore time.Time,
	latestIDBefore uint,
) (ListByPopularityResponse, error) {
	asOf := time.Now().UTC().Truncate(time.Minute)
	if reqAsOf > 0 {
		asOf = time.Unix(reqAsOf, 0).UTC().Truncate(time.Minute)
	}
	emptyWindow := false
	// Redis 热榜（稳定分页：as_of + offset）
	if f.redisCache != nil {
		//得到 60 个 key，并为每个分钟桶生成同下标的衰减权重：
		//v1:hot:video:1m:202607231435   ← 当前分钟
		//...
		//v1:hot:video:1m:202607231336   ← 59分钟前
		const win = popularityWindowMinutes
		keys := make([]string, 0, win)
		weights := make([]float64, 0, win)
		for i := 0; i < win; i++ {
			keys = append(keys, f.redisCache.Key("hot:content:1m:%s", asOf.Add(-time.Duration(i)*time.Minute).Format("200601021504")))
			weights = append(weights, popularityTimeWeight(i))
		}

		//窗口名称
		// dest = "v1:hot:video:merge:1m:202607231435"
		dest := f.redisCache.Key("hot:content:merge:1m:%s", asOf.Format("200601021504")) // 快照key：同一个as_of页内复用
		emptyKey := f.redisCache.Key("hot:content:empty:1m:%s", asOf.Format("200601021504"))
		opCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
		defer cancel()

		empty, snapshotErr := f.ensurePopularitySnapshot(opCtx, dest, emptyKey, keys, weights)
		emptyWindow = snapshotErr == nil && empty
		var members []string
		var err error
		if snapshotErr == nil && !empty {
			members, err = f.redisCache.ZRevRange(opCtx, dest, int64(offset), int64(offset+limit-1))
		} else {
			err = snapshotErr
		}
		if err == nil && len(members) == 0 {
			if offset > 0 {
				return ListByPopularityResponse{
					ContentList: []FeedContentItem{},
					AsOf:        asOf.Unix(),
					NextOffset:  offset,
					HasMore:     false,
				}, nil
			}
		}
		//将视频id转化为uint存入切片
		if err == nil && len(members) > 0 {
			ids := make([]uint, 0, len(members))
			for _, m := range members {
				u, err := strconv.ParseUint(m, 10, 64)
				if err == nil && u > 0 {
					ids = append(ids, uint(u))
				}
			}

			orderedVideos, err := f.GetContentByIDs(ctx, ids)
			if err == nil {
				items, err := f.buildFeedVideos(ctx, orderedVideos, viewerAccountID)
				if err != nil {
					return ListByPopularityResponse{}, err
				}
				resp := ListByPopularityResponse{
					ContentList: items,
					AsOf:        asOf.Unix(),
					NextOffset:  offset + len(items),
					HasMore:     len(items) == limit, //没有更多视频的时候,前端会自动刷新,获取最新热榜
				}
				if len(orderedVideos) > 0 {
					last := orderedVideos[len(orderedVideos)-1]
					nextPopularity := last.Popularity
					nextBefore := last.CreateTime
					nextID := last.ID
					resp.NextLatestPopularity = &nextPopularity
					resp.NextLatestBefore = &nextBefore
					resp.NextLatestIDBefore = &nextID
				}
				return resp, nil
			}
		}
	}

	//纯数据库
	loadFromDB := func() ([]*content.Content, error) {
		return f.feedRepository.ListByPopularity(ctx, limit, latestPopularity, latestBefore, latestIDBefore)
	}
	var videos []*content.Content
	var err error
	if emptyWindow && offset == 0 && latestBefore.IsZero() && latestIDBefore == 0 {
		videos, err = f.cachedPopularityFallback(ctx, asOf.Unix(), limit, loadFromDB)
	} else {
		videos, err = loadFromDB()
	}
	if err != nil {
		return ListByPopularityResponse{}, err
	}
	//根据videos构建并返回[]FeedContentItem
	items, err := f.buildFeedVideos(ctx, videos, viewerAccountID)
	if err != nil {
		return ListByPopularityResponse{}, err
	}
	listByPopularityResponse := ListByPopularityResponse{
		ContentList: items,
		AsOf:        0,
		NextOffset:  0,
		HasMore:     len(items) == limit,
	}
	if len(videos) > 0 {
		last := videos[len(videos)-1]
		nextPopularity := last.Popularity
		nextBefore := last.CreateTime
		nextID := last.ID
		listByPopularityResponse.NextLatestPopularity = &nextPopularity
		listByPopularityResponse.NextLatestBefore = &nextBefore
		listByPopularityResponse.NextLatestIDBefore = &nextID
	}
	return listByPopularityResponse, nil
}

func (f *FeedService) ensurePopularitySnapshot(ctx context.Context, dest, emptyKey string, keys []string, weights []float64) (bool, error) {
	empty, err := f.redisCache.Exists(ctx, emptyKey)
	if err != nil || empty {
		return empty, err
	}
	exists, err := f.redisCache.Exists(ctx, dest)
	if err != nil || exists {
		return false, err
	}

	v, err, _ := f.requestGroup.Do("sf:"+dest, func() (interface{}, error) {
		empty, err := f.redisCache.Exists(ctx, emptyKey)
		if err != nil || empty {
			return empty, err
		}
		exists, err := f.redisCache.Exists(ctx, dest)
		if err != nil || exists {
			return false, err
		}
		if err := f.redisCache.ZUnionStore(ctx, dest, keys, weights, "SUM"); err != nil {
			return false, err
		}
		members, err := f.redisCache.ZRevRange(ctx, dest, 0, 0)
		if err != nil {
			return false, err
		}
		if len(members) == 0 {
			if err := f.redisCache.Del(ctx, dest); err != nil {
				return false, err
			}
			return true, f.redisCache.SetBytes(ctx, emptyKey, []byte("1"), popularityEmptyTTL)
		}
		return false, f.redisCache.Expire(ctx, dest, 5*time.Minute)
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}

func (f *FeedService) cachedPopularityFallback(ctx context.Context, asOf int64, limit int, load func() ([]*content.Content, error)) ([]*content.Content, error) {
	cacheKey := f.redisCache.Key("feed:popularity:fallback:%d:%d", asOf, limit)
	readCached := func() ([]*content.Content, bool) {
		cacheCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
		defer cancel()
		data, err := f.redisCache.GetBytes(cacheCtx, cacheKey)
		if err != nil {
			return nil, false
		}
		var videos []*content.Content
		if err := json.Unmarshal(data, &videos); err != nil {
			return nil, false
		}
		return videos, true
	}
	if videos, ok := readCached(); ok {
		return videos, nil
	}

	v, err, _ := f.requestGroup.Do("sf:"+cacheKey, func() (interface{}, error) {
		if videos, ok := readCached(); ok {
			return videos, nil
		}
		videos, err := load()
		if err != nil {
			return nil, err
		}
		if videos == nil {
			videos = []*content.Content{}
		}
		if data, err := json.Marshal(videos); err == nil {
			cacheCtx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
			defer cancel()
			_ = f.redisCache.SetBytes(cacheCtx, cacheKey, data, popularityEmptyTTL)
		}
		return videos, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]*content.Content), nil
}

// ✅
// 构建并返回本页视频信息切片(和video相比多了IsLiked,少了popularity)
func (f *FeedService) buildFeedVideos(ctx context.Context, videos []*content.Content, viewerAccountID uint) ([]FeedContentItem, error) {
	//videoIDs := make([]uint, len(videos)) //len=N, cap=N(全为零值)
	//for i, v := range videos {
	//	videoIDs[i] = v.ID
	//}
	//从videos里获取所有视频id,后续确认是否点过赞
	videoIDs := make([]uint, 0, len(videos)) //len=0, cap=N 预先分配好底层数组,append不用频繁扩容
	for _, v := range videos {
		videoIDs = append(videoIDs, v.ID) //append: 在len后追加新元素
	}
	//批量获取用户是否点赞,得到map
	likedMap, err := f.likeRepository.BatchGetLiked(ctx, videoIDs, viewerAccountID)
	if err != nil {
		return nil, err
	}

	feedVideos := make([]FeedContentItem, 0, len(videos))
	for _, v := range videos {
		assets := make([]FeedAsset, 0, len(v.Assets))
		for _, asset := range v.Assets {
			assets = append(assets, FeedAsset{Type: asset.AssetType, URL: asset.URL, SortOrder: asset.SortOrder})
		}
		feedVideos = append(feedVideos, FeedContentItem{
			ID:          v.ID,
			Author:      FeedAuthor{ID: v.AuthorID, Username: v.Username},
			ContentType: v.ContentType,
			Title:       v.Title,
			Description: v.Description,
			Body:        v.Body,
			PlayURL:     v.PlayURL,
			CoverURL:    v.CoverURL,
			Assets:      assets,
			CreateTime:  v.CreateTime.Unix(),
			LikesCount:  v.LikesCount,
			IsLiked:     likedMap[v.ID],
		})
	}
	return feedVideos, nil
}

// orderedIDs的顺序按照分数从大到小,也就是最晚的在前
func buildOrderedResult(orderedIDs []uint, dataMap map[uint]*content.Content) []*content.Content {
	res := make([]*content.Content, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if v, exist := dataMap[id]; exist && v != nil {
			res = append(res, v)
		}
	}
	return res
}

// ✅
// 根据tag返回[]FeedContentItem
func (f *FeedService) ListByTag(ctx context.Context, tagName string, limit int, viewerAccountID uint) ([]FeedContentItem, error) {
	videos, err := f.feedRepository.ListByTag(ctx, tagName, limit)
	if err != nil {
		return nil, err
	}
	return f.buildFeedVideos(ctx, videos, viewerAccountID)
}
