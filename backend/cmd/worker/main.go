package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/worker"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/activity"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/comment"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/like"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/social"
	"github.com/bbb3056666-cyber/Agora/backend/internal/outbox"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/db"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/observability"
	mqrabbit "github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"

	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
)

const (
	TimelineExchange          = mqrabbit.TimelineExchange
	TimelineQueue             = mqrabbit.TimelineQueue
	TimelineRoutingKey        = mqrabbit.TimelineRoutingKey
	TimelinePublishRoutingKey = mqrabbit.TimelinePublishRoutingKey
	TimelineRetryModule       = mqrabbit.TimelineRetryModule

	SocialExchange         = mqrabbit.SocialExchange
	SocialQueue            = mqrabbit.SocialQueue
	SocialRoutingKey       = mqrabbit.SocialRoutingKey
	SocialFollowRoutingKey = mqrabbit.SocialFollowRoutingKey

	PopularityExchange   = mqrabbit.PopularityExchange
	PopularityQueue      = mqrabbit.PopularityQueue
	PopularityRoutingKey = mqrabbit.PopularityRoutingKey

	ActivityExchange           = mqrabbit.ActivityExchange
	ActivityQueue              = mqrabbit.ActivityQueue
	ActivityRegisterRoutingKey = mqrabbit.ActivityRegisterRoutingKey
	ActivitySuccessRoutingKey  = mqrabbit.ActivitySuccessRoutingKey

	LikeAppliedExchange   = mqrabbit.LikeAppliedExchange
	LikeAppliedRoutingKey = mqrabbit.LikeAppliedRoutingKey

	CommentIntentExchange    = mqrabbit.CommentIntentExchange
	CommentIntentQueue       = mqrabbit.CommentIntentQueue
	CommentIntentRoutingKey  = mqrabbit.CommentIntentRoutingKey
	CommentIntentRetryModule = mqrabbit.CommentIntentRetryModule
	CommentAppliedExchange   = mqrabbit.CommentAppliedExchange
	CommentAppliedRoutingKey = mqrabbit.CommentAppliedRoutingKey

	NotificationLikeQueue     = mqrabbit.NotificationLikeQueue
	NotificationCommentQueue  = mqrabbit.NotificationCommentQueue
	NotificationSocialQueue   = mqrabbit.NotificationSocialQueue
	NotificationActivityQueue = mqrabbit.NotificationActivityQueue

	LikeIntentWorkerCount = mqrabbit.LikeIntentPartitions
)

func main() {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.json"
	}
	log.Printf("Loading config from %s", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Config loaded from file: %s", configPath)

	// 连接数据库（带重试）
	var sqlDB *gorm.DB //数据库连接句柄,全程只有一份
	connectWithRetry("MySQL", 10, func() error {
		var err error
		sqlDB, err = db.NewDB(cfg.Database, cfg.DatabasePool.Worker)
		return err
	})
	defer db.CloseDB(sqlDB)

	// 连接 Redis（用于流行度更新）
	cache, err := rediscache.NewFromCfg(&cfg.Redis)
	if err != nil {
		log.Printf("Redis config error (popularity worker disabled): %v", err)
		cache = nil
	} else {
		pingCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if err := cache.Ping(pingCtx); err != nil {
			log.Printf("Redis not available (popularity worker disabled): %v", err)
			_ = cache.Close()
			cache = nil
		} else {
			defer cache.Close()
			log.Printf("Redis connected (popularity worker enabled)")
		}
	}

	// 连接 RabbitMQ（带重试）
	url := "amqp://" + cfg.RabbitMQ.Username + ":" + cfg.RabbitMQ.Password + "@" + cfg.RabbitMQ.Host + ":" + strconv.Itoa(cfg.RabbitMQ.Port) + "/"
	var conn *amqp.Connection
	connectWithRetry("RabbitMQ", 10, func() error {
		var err error
		conn, err = amqp.Dial(url)
		return err
	})
	defer conn.Close()

	// 准备所有worker要用的组件
	accountRepo := account.NewAccountRepository(sqlDB)
	socialRepo := social.NewSocialRepository(sqlDB)
	likeRepo := like.NewLikeRepository(sqlDB)
	commentRepo := comment.NewCommentRepository(sqlDB)
	timelineSocialService := social.NewSocialService(socialRepo, accountRepo, nil, cache)

	// activityService
	// 成功写库后，发布报名成功的事件到NotificationActivityQueue
	activityMQ, err := mqrabbit.NewActivityMQ(&mqrabbit.RabbitMQ{Conn: conn})
	if err != nil {
		log.Fatalf("Failed to initialize activity MQ publisher: %v", err)
	}
	activityRepo := activity.NewActivityRepository(sqlDB)
	activityService := activity.NewActivityService(activityRepo, nil, cache, activityMQ)

	// 启动outbox轮询器
	// Outbox 轮询属于后台可靠投递：查询待投递记录、等待 Broker Confirm、再删除记录。
	// 它放在 Worker 进程，避免与 HTTP 请求共用 API 的数据库连接池。
	pollerMQ := &mqrabbit.RabbitMQ{Conn: conn}
	timelineMQ, err := mqrabbit.NewTimelineMQ(pollerMQ)
	if err != nil {
		log.Printf("TimelineMQ init failed (outbox timeline delivery disabled): %v", err)
		timelineMQ = nil
	}
	likeMQ, err := mqrabbit.NewLikeMQ(pollerMQ)
	if err != nil {
		log.Printf("LikeMQ init failed (outbox like delivery disabled): %v", err)
		likeMQ = nil
	}
	commentMQ, err := mqrabbit.NewCommentMQ(pollerMQ)
	if err != nil {
		log.Printf("CommentMQ init failed (outbox comment delivery disabled): %v", err)
		commentMQ = nil
	}
	// 视频、点赞、评论的 Outbox 均由 worker 进程轮询；Confirm 成功后才删除待投递记录。
	outbox.StartPoller(sqlDB, timelineMQ, likeMQ, commentMQ)

	// API进程是发布方，需要确保自己发消息到Exchange/Queue时拓扑已经存在（比如MQ重启后队列丢了，API启动时自动重建）
	// Worker进程是消费方，自己声明一遍拓扑不依赖API先启动，独立部署/重启都没问题
	// 用临时 Channel 声明拓扑（持久化队列，声明一次即可） Exchange / Queue 只是一次性的配置操作：告诉 RabbitMQ 创建并绑定拓扑，
	// 此处串行执行即可；它不是持续的消息收发操作。
	// 持续收发时不能让多个 goroutine 共用同一个 Channel：并发 Publish 可能导致协议帧交错；
	// 消费与发布共用 Channel 还会让 QoS、错误和重连互相影响，因此各 Worker 后续各自创建 Channel。
	topoCh, err := conn.Channel()
	if err != nil {
		log.Fatalf("Failed to open topology channel: %v", err)
	}

	// 声明业务模块，交换机、事件队列、绑定关系
	// 点赞意图的分区队列在下方单独声明。
	// 时间线，关注，点赞成功，评论意图，评论成功，活动报名，热度事件
	// 热度、活动消费者依赖 Redis，Redis 不可用时跳过
	for _, topology := range []struct {
		name          string
		exchange      string
		queue         string
		bindingKey    string
		requiresRedis bool
	}{
		{"timeline", TimelineExchange, TimelineQueue, TimelineRoutingKey, true},
		{"social", SocialExchange, SocialQueue, SocialRoutingKey, false},
		{"comment-intent", CommentIntentExchange, CommentIntentQueue, CommentIntentRoutingKey, false},
		{"activity", ActivityExchange, ActivityQueue, ActivityRegisterRoutingKey, true},
		{"popularity", PopularityExchange, PopularityQueue, PopularityRoutingKey, true},
		{"like-applied-popularity", LikeAppliedExchange, PopularityQueue, LikeAppliedRoutingKey, true},
		{"comment-applied-popularity", CommentAppliedExchange, PopularityQueue, CommentAppliedRoutingKey, true},
	} {
		if topology.requiresRedis && cache == nil {
			log.Printf("Skip %s topology: Redis is unavailable", topology.name)
			continue
		}
		if err := declareTopology(topoCh, topology.exchange, topology.queue, topology.bindingKey); err != nil {
			log.Fatalf("Failed to declare %s topology: %v", topology.name, err)
		}
	}

	// 声明通知模块，交换机、通知队列、绑定关系
	// 点赞，评论，关注，报名成功
	for _, topology := range []struct {
		exchange   string
		queue      string
		bindingKey string
	}{
		{LikeAppliedExchange, NotificationLikeQueue, LikeAppliedRoutingKey},
		{CommentAppliedExchange, NotificationCommentQueue, CommentAppliedRoutingKey},
		{SocialExchange, NotificationSocialQueue, SocialFollowRoutingKey},
		{ActivityExchange, NotificationActivityQueue, ActivitySuccessRoutingKey},
	} {
		if err := declareTopology(topoCh, topology.exchange, topology.queue, topology.bindingKey); err != nil {
			log.Fatalf("Failed to declare notification topology for %s: %v", topology.queue, err)
		}
	}

	// 声明重试模块，重试交换机，延迟队列，目的交换机，routing key
	// 时间线，评论请求；点赞意图的分区重试队列在下方单独声明。
	// 时间线消费者会在处理失败时将消息投递到延迟重试队列。Worker 也声明一次，
	// 因此 Worker 可独立于 API 进程启动或在 RabbitMQ 重启后自行恢复完整拓扑。
	for _, retry := range []struct {
		name             string
		module           string
		targetExchange   string
		targetRoutingKey string
	}{
		{"timeline", TimelineRetryModule, TimelineExchange, TimelinePublishRoutingKey},
		{"comment intent", CommentIntentRetryModule, CommentIntentExchange, CommentIntentRoutingKey},
	} {
		if err := mqrabbit.DeclareModuleRetryTopologies(
			topoCh,
			retry.module,
			retry.targetExchange,
			retry.targetRoutingKey,
		); err != nil {
			log.Fatalf("Failed to declare %s retry topology: %v", retry.name, err)
		}
	}
	if err := mqrabbit.DeclareLikeIntentPartitions(topoCh); err != nil {
		log.Fatalf("Failed to declare partitioned like intent topology: %v", err)
	}
	_ = topoCh.Close()

	/*
		Go 1.16加的标准库API，用来监听系统退出信号，实现优雅关闭：
		两个信号分别是：
		os.Interrupt（SIGINT）：用户按 Ctrl+C 触发
		syscall.SIGTERM：docker stop、k8s pod 关停、systemctl stop 发的终止信号
		（和SIGKILL不同，这个可以捕获，给时间做清理）
		返回两个值：
		ctx：普通context，收到上面任意一个信号时会自动取消（ctx.Done() 关闭）
		stop：手动取消context的函数，defer stop() 确保main函数退出时释放context资源，避免goroutine泄漏
	*/
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		closed := conn.NotifyClose(make(chan *amqp.Error, 1))
		select {
		case err := <-closed:
			if ctx.Err() == nil {
				log.Fatalf("RabbitMQ connection closed; restarting worker to rebuild channels and publishers: %v", err)
			}
		case <-ctx.Done():
		}
	}()

	// 启动所有业务worker
	// 事件线worker
	worker.StartTimelineConsumer(
		mqrabbit.TimelineQueue,
		cache,
		&mqrabbit.RabbitMQ{Conn: conn},
		timelineSocialService,
	)
	// 每个事件 Worker 独立 Channel + 自动重连
	// 共用同一套独占 Channel、断线重连和退出处理逻辑，差异只在创建函数。
	for _, spec := range []struct {
		name          string
		requiresRedis bool
		run           func(ch *amqp.Channel) error
	}{
		{
			name: "SocialWorker",
			run: func(ch *amqp.Channel) error {
				return worker.NewSocialWorker(ch, socialRepo, cache, SocialQueue).Run(ctx)
			},
		},
		{
			name: "CommentWorker",
			run: func(ch *amqp.Channel) error {
				return worker.NewCommentWorker(ch, &mqrabbit.RabbitMQ{Conn: conn}, commentRepo, CommentIntentQueue).Run(ctx)
			},
		},
		{
			name:          "PopularityWorker",
			requiresRedis: true,
			run: func(ch *amqp.Channel) error {
				return worker.NewPopularityWorker(ch, cache, PopularityQueue).Run(ctx)
			},
		},
		{
			name:          "ActivityWorker",
			requiresRedis: true,
			run: func(ch *amqp.Channel) error {
				return worker.NewActivityWorker(ch, activityService, ActivityQueue).Run(ctx)
			},
		},
	} {
		if spec.requiresRedis && cache == nil {
			log.Printf("Skip %s: Redis is unavailable", spec.name)
			continue
		}
		go runWorkerWithRetry(ctx, spec.name, conn, spec.run)
	}
	for i := 0; i < LikeIntentWorkerCount; i++ {
		queue := mqrabbit.LikeIntentPartitionQueue(i)
		name := fmt.Sprintf("LikeIntentWorker-%d", i)
		go runWorkerWithRetry(ctx, name, conn, func(ch *amqp.Channel) error {
			return worker.NewLikeIntentWorker(ch, &mqrabbit.RabbitMQ{Conn: conn}, likeRepo, queue).Run(ctx)
		})
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		var afterID uint
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				nextID, checked, corrected, err := likeRepo.ReconcileLikeCounts(checkCtx, afterID, 100)
				cancel()
				if err != nil {
					log.Printf("like count reconciliation failed: %v", err)
					continue
				}
				afterID = nextID
				if corrected > 0 {
					log.Printf("like count reconciliation: checked=%d corrected=%d", checked, corrected)
				}
			}
		}
	}()

	// 启动所有通知worker
	// 通知由独立 Worker 消费：先写 MySQL，再通过 Redis Pub/Sub 广播给所有 API 实例。
	// API 进程只保留 Redis 订阅和本地 SSE 连接管理，不再直接消费 RabbitMQ。
	var notificationPublisher worker.NotificationPublisher
	if cache != nil {
		notificationPublisher = cache
	}
	worker.StartNotificationConsumers(
		ctx,
		&mqrabbit.RabbitMQ{Conn: conn},
		sqlDB,
		notificationPublisher,
	)

	//pprof性能检测
	pprofServer, err := observability.NewPprofServer("Worker", cfg.ObservabilityConfig.Pprof.Enabled, cfg.ObservabilityConfig.Pprof.WorkerAddr)
	if err != nil {
		log.Printf("Failed to start worker pprof server: %v", err)
	}
	if pprofServer != nil {
		defer pprofServer.Close()
	}

	// 等待退出信号
	<-ctx.Done()
	log.Printf("Worker shutting down...")
	time.Sleep(2 * time.Second) // 等待正在处理的消息完成
	log.Printf("Worker stopped")
}

// 反复尝试连接服务器
func connectWithRetry(name string, maxRetries int, fn func() error) {
	for i := 0; i < maxRetries; i++ {
		if err := fn(); err == nil {
			log.Printf("%s is ready", name)
			//一直重试到sqlDB被成功赋值
			return
		}
		//等1,2,4s
		wait := time.Duration(1<<i) * time.Second
		//最多等30s
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		log.Printf("%s 不可用，%v 后重试 (%d/%d)...", name, wait, i+1, maxRetries)
		time.Sleep(wait)
	}
	//数据库/RabbitMQ连接失败,直接退出
	log.Fatalf("%s: 超过最大重试次数", name)
}

// 声明交换机、队列、绑定及对应死信队列。
// Worker 与 API 必须复用这一个实现：RabbitMQ 对同名队列要求声明参数完全一致。
func declareTopology(ch *amqp.Channel, exchangeName, queueName, bindingKey string) error {
	return mqrabbit.DeclareTopic(ch, exchangeName, queueName, bindingKey)
}

// runWorkerWithRetry 为每个 Worker 创建独立 Channel，断开后自动重连
// 创建Channel并设置Qos,传入ch调用每个worker专属的fn
func runWorkerWithRetry(ctx context.Context, name string, conn *amqp.Connection, fn func(*amqp.Channel) error) {
	for {
		select {
		//程序退出,管道关闭,所有监听的协程同时收到消息(struct{}{},false)
		case <-ctx.Done():
			return
		default:
		}

		//每轮循环新建一个Channel
		ch, err := conn.Channel()
		if err != nil {
			//创建失败5s后重试
			log.Printf("%s: 创建 Channel 失败: %v, 5秒后重试", name, err)
			time.Sleep(5 * time.Second)
			continue
		}
		if err := ch.Qos(
			50,
			0,
			false,
		); err != nil {
			log.Printf("%s: QoS 设置失败: %v", name, err)
		}

		log.Printf("%s started, consuming", name)
		if err := fn(ch); err != nil {
			if ctx.Err() != nil {
				//收到SIGINT/SIGTERM被主动取消时：
				//返回context.Canceled，错误内容固定是"context canceled"
				//程序关闭,直接退出
				_ = ch.Close()
				return
			}
			//否则5s后重试
			log.Printf("%s: %v, 5秒后重连...", name, err)
		}
		//虽然Channel已经断了，但本地Go侧的amqp.Channel对象还占着内存、goroutine资源，
		//Close()会把这些本地资源清理掉，避免内存泄漏
		//就算Close一个已经断开的Channel也不会报错、不会panic，
		//amqp库内部做了容错，重复关闭/关闭已断开的Channel都是安全的
		//nil.Close会panic
		_ = ch.Close()
		time.Sleep(5 * time.Second)
	}
}
