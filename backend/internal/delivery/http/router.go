package http

import (
	"context"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/jwt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/ratelimit"
	"github.com/bbb3056666-cyber/Agora/backend/internal/delivery/worker"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/account"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/activity"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/comment"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/content"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/feed"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/like"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/message"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/social"
	"github.com/bbb3056666-cyber/Agora/backend/internal/module/upload"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func SetRouter(db *gorm.DB, cache *rediscache.Client, rmq *rabbitmq.RabbitMQ) *gin.Engine {
	// gin.New() 不会自动打印每个请求；高并发下默认访问日志会带来额外 stdout I/O。
	// Recovery 始终保留，访问日志仅在显式开启时挂载。
	r := gin.New()
	r.Use(gin.Recovery())
	if os.Getenv("GIN_ACCESS_LOG") == "true" {
		r.Use(gin.Logger())
	}

	// 不信任任何代理,直接拿TCP连接的对端IP作为客户端IP
	// 也就是直连的机器IP，如果前面有Nginx反向代理，拿到的就是Nginx服务器的IP，不是用户真实IP
	// 忽略所有X-Forwarded-For/X-Real-IP头，防止有人伪造IP绕过限流/封禁
	// 如果信任,则从X-Forwarded-For头里取真实客户端IP
	if err := r.SetTrustedProxies([]string{"127.0.0.1"}); err != nil { //信任本机代理
		log.Printf("SetTrustedProxies failed: %v", err)
	}

	// 健康检查：Docker/K8s 定期访问判断服务是否存活，不存活则重启容器
	r.GET("/healthz", func(c *gin.Context) {
		/*
			HTTP状态码是应用层（HTTP协议）约定的业务语义码，和底层TCP连接成功与否完全无关
			只要TCP三次握手成功，服务器收到请求并返回了响应，不管状态码是200/401/404/500，网络连接都是正常的
			只有TCP连接失败、超时、被拒绝、DNS解析失败这种才是网络层面的错误，没有HTTP状态码
		*/
		c.JSON(200, gin.H{"status": "ok"})
	})

	// 静态文件服务：/static/xxx → 读取本地 .run/uploads/xxx
	// 例：/static/avatars/1/a.jpg → .run/uploads/avatars/1/a.jpg
	// 和 r.GET 不同，Static 匹配所有子路径，直接返回文件，不执行函数
	// 收到GET /static/xxx.mp4请求时，自动去磁盘找./.run/uploads/xxx.mp4文件
	// 找到文件就直接返回给前端
	r.Static("/static", "./.run/uploads")

	// ------------------------------------------------------------
	// rate_limit 限流中间件,redis实现
	// KeyByIP=按IP限流
	//同一个ip一分钟最多只能登录十次
	loginLimitHandler := ratelimit.GetLimiter(cache, "account_login", 10, time.Minute, ratelimit.GetIP) // 登录：每分钟最多10次/每个IP
	//同一个ip一小时最多只能注册五次
	registerLimitHandler := ratelimit.GetLimiter(cache, "account_register", 5, time.Hour, ratelimit.GetIP)

	//KeyByAccount=按用户ID限流
	//同一个用户一分钟最多只能点赞或取消一百次
	likeLimitHandler := ratelimit.GetLimiter(cache, "like_write", 100, time.Minute, ratelimit.GetAccount)
	//同一个用户一分钟最多只能评论或删评十次
	commentLimitHandler := ratelimit.GetLimiter(cache, "comment_write", 10, time.Minute, ratelimit.GetAccount)
	//同一个用户一分钟最多只能关注或取关二十次
	socialLimitHandler := ratelimit.GetLimiter(cache, "social_write", 20, time.Minute, ratelimit.GetAccount)
	// ------------------------------------------------------------

	//消息队列
	//Popularity
	popularityMQ, err := rabbitmq.NewPopularityMQ(rmq) // 热度事件发布到 MQ
	if err != nil {
		log.Printf("PopularityMQ init failed (mq disabled): %v", err)
		popularityMQ = nil
	}
	//Social
	socialMQ, err := rabbitmq.NewSocialMQ(rmq)
	if err != nil {
		log.Printf("SocialMQ init failed (mq disabled): %v", err)
		socialMQ = nil
	}
	//Activity
	activityMQ, err := rabbitmq.NewActivityMQ(rmq)
	if err != nil {
		log.Printf("activityMQ init failed (activity registration disabled): %v", err)
		activityMQ = nil
	}

	// ------------------------------------------------------------
	// account
	// 三层架构：Repository（数据库操作）→ Service（业务逻辑）→ Handler（HTTP收发包）
	accountRepository := account.NewAccountRepository(db)                 //只有一个数据库句柄
	accountService := account.NewAccountService(accountRepository, cache) //有句柄和redis缓存
	accountHandler := account.NewAccountHandler(accountService)           //和service一样,可以用来调用Service

	/*
		type RouterGroup struct {
		engine Engine // 不是指针，值类型，编译错误：invalid recursive type RouterGroup
		// ...
		}
		type Engine struct {
		RouterGroup // Engine里包含RouterGroup，RouterGroup又包含Engine，无限嵌套，编译器算不出结构体大小
		}
		相当于
			type Engine struct {
			    engine Engine
			}
			// 编译错误：invalid recursive type Engine
	*/
	//-----------------------------------------------------------
	// account 模块
	//路由分组
	accountGroup := r.Group("/account")
	//公开接口,不需要登录
	// accountHandler.CreateAccount 是方法值：Go 自动把 accountHandler 绑定到方法中的h(h := accountHandler )
	// 把 func (h *AccountHandler) CreateAccount(c *gin.Context) 变成 func(c *Context)，匹配 gin.HandlerFunc
	{
		accountGroup.POST("/register", registerLimitHandler, accountHandler.CreateAccount) //✅
		accountGroup.POST("/login", loginLimitHandler, accountHandler.Login)               //✅
		accountGroup.POST("/changePassword", accountHandler.ChangePassword)                //✅
		accountGroup.POST("/findByID", accountHandler.FindByID)                            //✅
		accountGroup.POST("/findByUsername", accountHandler.FindByUsername)                //✅
		accountGroup.POST("/refresh", accountHandler.Refresh)                              //✅
	}
	//不添加额外路径的新分组:Group("")
	protectedAccountGroup := accountGroup.Group("")
	//加中间件
	// JWTAuth 鉴权中间件：没 token 或 token 失效 → 返回 401，前端自动调 /refresh 刷新
	protectedAccountGroup.Use(jwt.JWTAuth(cache))
	//需要登录的请求，鉴权失败返回 401
	{
		protectedAccountGroup.POST("/logout", accountHandler.Logout)               //✅
		protectedAccountGroup.POST("/rename", accountHandler.Rename)               //✅
		protectedAccountGroup.POST("/uploadAvatar", accountHandler.UploadAvatar)   //✅
		protectedAccountGroup.POST("/updateProfile", accountHandler.UpdateProfile) //✅
	}
	//-----------------------------------------------------------

	// ----------------------------------------------------------
	// content 模块
	//只包含redis
	chunkHandler := upload.NewChunkUploadHandler(cache) // 分块上传会话存 Redis

	//三层架构
	contentRepository := content.NewContentRepository(db)
	contentService := content.NewContentService(contentRepository, cache, popularityMQ)
	contentHandler := content.NewContentHandler(contentService, accountService)
	contentGroup := r.Group("/content")
	//无需登录
	{
		contentGroup.POST("/listByAuthorID", contentHandler.ListByAuthorID)
		contentGroup.POST("/getDetail", contentHandler.GetDetail)
	}
	//需要登录的请求
	protectedContentGroup := contentGroup.Group("")
	protectedContentGroup.Use(jwt.JWTAuth(cache))
	{
		protectedContentGroup.POST("/upload/video", contentHandler.UploadVideoAsset)
		protectedContentGroup.POST("/upload/images", contentHandler.UploadImages)
		protectedContentGroup.POST("/upload/cover", contentHandler.UploadCover)
		protectedContentGroup.POST("/publish", contentHandler.PublishContent)
		protectedContentGroup.POST("/delete", contentHandler.DeleteContent)
		//分块上传视频
		protectedContentGroup.POST("/chunk/init", chunkHandler.InitChunkUpload)
		protectedContentGroup.POST("/chunk/upload", chunkHandler.UploadChunk)
		protectedContentGroup.POST("/chunk/status", chunkHandler.ChunkStatus)
		protectedContentGroup.POST("/chunk/complete", chunkHandler.CompleteChunkUpload)
	}

	// 内容限量活动：只有作者能创建；报名逻辑接 Redis + MQ。
	activityRepository := activity.NewActivityRepository(db)
	activityService := activity.NewActivityService(activityRepository, contentRepository, cache, activityMQ)
	activityHandler := activity.NewActivityHandler(activityService)

	contentGroup.POST("/activity/list", activityHandler.ListByContentID)
	protectedContentGroup.POST("/activity/create", activityHandler.CreateActivity)
	protectedContentGroup.POST("/activity/register", activityHandler.Register)
	protectedContentGroup.POST("/activity/result", activityHandler.RegistrationResult)
	// ------------------------------------------------------------

	// ------------------------------------------------------------
	// like
	//三层架构
	likeRepository := like.NewLikeRepository(db)
	likeService := like.NewLikeService(likeRepository)
	likeHandler := like.NewLikeHandler(likeService)
	//点赞路由,全都需要登录
	likeGroup := r.Group("/like")
	protectedLikeGroup := likeGroup.Group("")
	protectedLikeGroup.Use(jwt.JWTAuth(cache))
	{
		protectedLikeGroup.POST("/like", likeLimitHandler, likeHandler.Like)           //✅
		protectedLikeGroup.POST("/unlike", likeLimitHandler, likeHandler.Unlike)       //✅
		protectedLikeGroup.POST("/isLiked", likeHandler.IsLiked)                       //✅
		protectedLikeGroup.POST("/listMyLikedContents", likeHandler.ListMyLikedVideos) //✅
	}
	// ------------------------------------------------------------

	// ------------------------------------------------------------
	// comment
	//三层架构
	commentRepository := comment.NewCommentRepository(db)
	commentService := comment.NewCommentService(commentRepository, contentRepository)
	commentHandler := comment.NewCommentHandler(commentService, accountService)
	//评论路由
	commentGroup := r.Group("/comment")
	{
		//没登陆也可以获取所有评论
		commentGroup.POST("/listAll", commentHandler.GetAllComments) //✅
	}
	protectedCommentGroup := commentGroup.Group("")
	protectedCommentGroup.Use(jwt.JWTAuth(cache))
	{
		protectedCommentGroup.POST("/publish", commentLimitHandler, commentHandler.PublishComment) //✅
		protectedCommentGroup.POST("/delete", commentLimitHandler, commentHandler.DeleteComment)   //✅
	}
	// ------------------------------------------------------------

	// ------------------------------------------------------------
	// social
	//三层架构
	socialRepository := social.NewSocialRepository(db)
	socialService := social.NewSocialService(socialRepository, accountRepository, socialMQ, cache)
	socialHandler := social.NewSocialHandler(socialService)
	//关注路由分组
	socialGroup := r.Group("/social")
	protectedSocialGroup := socialGroup.Group("")
	//登录才有关注功能
	protectedSocialGroup.Use(jwt.JWTAuth(cache))
	{
		protectedSocialGroup.POST("/follow", socialLimitHandler, socialHandler.Follow)     //✅
		protectedSocialGroup.POST("/unfollow", socialLimitHandler, socialHandler.Unfollow) //✅
		protectedSocialGroup.POST("/getAllFollowers", socialHandler.GetAllFollowers)       //✅
		protectedSocialGroup.POST("/getAllVloggers", socialHandler.GetAllVloggers)         //✅
		protectedSocialGroup.POST("/getCounts", socialHandler.GetCounts)                   //✅
	}
	// ------------------------------------------------------------

	// ------------------------------------------------------------
	//三个 Repository 分别属于三个模块，放在 account handler 里会耦合 video 和 social
	accountGroup.POST("/getProfile", GetProfile(accountService, contentRepository, socialService, socialRepository))
	// ------------------------------------------------------------

	// ------------------------------------------------------------
	// feed
	//三层架构
	feedRepository := feed.NewFeedRepository(db)
	feedService := feed.NewFeedService(feedRepository, likeRepository, cache, socialService)
	feedHandler := feed.NewFeedHandler(feedService)
	//feed分组
	feedGroup := r.Group("/feed")
	softFeedGroup := feedGroup.Group("")
	// SoftJWTAuth：有 token 就解析（用于标记 is_liked），没登录也放行，游客可刷推荐流
	softFeedGroup.Use(jwt.SoftJWTAuth(cache))
	{
		softFeedGroup.POST("/listLatest", feedHandler.ListLatest)             //✅(redis冷热分离/纯sql)
		softFeedGroup.POST("/listLikesCount", feedHandler.ListLikesCount)     //✅(纯sql)
		softFeedGroup.POST("/listByPopularity", feedHandler.ListByPopularity) //✅(redis热榜/纯sql)
		softFeedGroup.POST("/listByTag", feedHandler.ListByTag)               //✅(纯sql)
	}
	protectedFeedGroup := feedGroup.Group("")
	// JWTAuth：关注流必须登录才能知道"我关注了谁"，未登录返回 401
	protectedFeedGroup.Use(jwt.JWTAuth(cache))
	{
		protectedFeedGroup.POST("/listByFollowing", feedHandler.ListByFollowing) //✅（推拉结合）
	}
	// ------------------------------------------------------------

	// ------------------------------------------------------------
	// message
	//三层架构
	messageRepo := message.NewRepository(db)
	messageService := message.NewService(messageRepo)
	messageHandler := message.NewHandler(messageService)
	//消息路由分组
	messageGroup := r.Group("/message")
	protectedMessageGroup := messageGroup.Group("")
	protectedMessageGroup.Use(jwt.JWTAuth(cache))
	{
		protectedMessageGroup.POST("/send", messageHandler.Send) //✅
		protectedMessageGroup.POST("/list", messageHandler.List) //✅
	}
	// ------------------------------------------------------------
	//上面为同步的一问一答接口（account/video/like/comment/social/feed/message）

	// ------------------------------------------------------------
	// SSE notification(Server-Sent Events)
	// 实时通知推送系统
	// 创建sse中枢,管理所有用户从消息队列接收消息的channel
	sseHub := worker.NewSSEHub(db)

	// 订阅独立 Worker 通过 Redis Pub/Sub 广播的在线通知；
	// 本 API 实例收到后只向自己持有的 SSE 连接推送，离线消息由 MySQL 通知列表兜底。
	sseHub.StartRedisSubscriber(context.Background(), cache)

	// 通知路由，建立sse连接
	notifGroup := r.Group("/notification")
	notifGroup.Use(sseHub.SSERequireAuth())
	sseHub.RegisterRoutes(notifGroup)

	return r
}

// GetProfile 获取个人资料
func GetProfile(accountService *account.AccountService, contentRepository *content.ContentRepository, socialService *social.SocialService, socialRepository *social.SocialRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req account.GetProfileRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		if req.AccountID == 0 {
			c.JSON(400, gin.H{"error": "account_id is required"})
			return
		}
		//根据id找到账户
		acc, err := accountService.FindByID(c.Request.Context(), req.AccountID)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		contentCount, _ := contentRepository.CountByAuthor(c.Request.Context(), req.AccountID)
		totalLikes, _ := contentRepository.TotalLikesByAuthor(c.Request.Context(), req.AccountID)
		followerCount, _ := socialService.GetFollowerCount(c.Request.Context(), req.AccountID)
		vloggerCount, _ := socialRepository.CountVloggers(c.Request.Context(), req.AccountID)

		c.JSON(200, account.GetProfileResponse{
			Account:      account.FindByIDResponse{ID: acc.ID, Username: acc.Username, AvatarURL: acc.AvatarURL, Bio: acc.Bio},
			ContentCount: contentCount, TotalLikes: totalLikes,
			FollowerCount: followerCount, VloggerCount: vloggerCount,
		})
	}
}
