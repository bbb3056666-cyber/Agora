package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bbb3056666-cyber/Agora/backend/internal/auth"
	rediscache "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SSEHub struct {
	mu sync.RWMutex //读写锁零值可用,不需要赋值
	//与Mutex区别:
	//Mutex:不管读还是写，同一时刻只能有一个goroutine持有锁，所有操作都串行
	//RWMutex:
	//- 读锁（RLock/RUnlock）：多个goroutine可以同时持有读锁，读操作并发执行
	//- 写锁（Lock/Unlock）：完全独占，只要有写锁，所有读/写操作都要等；只要有读锁，写锁也要等所有读释放才能拿
	clients map[uint][]chan *Notification //管理所有页面通知的字段
	db      *gorm.DB
}

func NewSSEHub(db *gorm.DB) *SSEHub {
	return &SSEHub{clients: make(map[uint][]chan *Notification), db: db}
}

// StartRedisSubscriber
// 订阅独立 Worker 广播的实时通知。
// 每个 API 实例都会订阅；只有持有目标用户 SSE 连接的实例会实际 Push，其余实例直接忽略。
// Pub/Sub 不是可靠存储：订阅断开期间错过的通知仍可通过 MySQL 的 /notification/list 查询。
func (h *SSEHub) StartRedisSubscriber(ctx context.Context, cache *rediscache.Client) {
	if h == nil || cache == nil {
		log.Printf("Notification Redis subscriber disabled (hub or Redis not available)")
		return
	}

	go func() {
		//重连循环
		for {
			pubsub, err := cache.Subscribe(ctx, NotificationRealtimeChannel)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("notification Redis subscriber: 订阅失败，5秒后重试: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					continue
				}
			}

			log.Printf("notification Redis subscriber started, channel=%s", NotificationRealtimeChannel)
			messages := pubsub.Channel()
		consumeLoop:
			for {
				select {
				case <-ctx.Done():
					_ = pubsub.Close()
					return
				case message, ok := <-messages:
					if !ok {
						_ = pubsub.Close()
						log.Printf("notification Redis subscriber: 订阅连接已断开，准备重连")
						//直接结束循环
						break consumeLoop
					}

					var notification Notification
					if err := json.Unmarshal([]byte(message.Payload), &notification); err != nil {
						log.Printf("notification Redis subscriber: 无法解析通知消息: %v", err)
						continue
					}
					if notification.RecipientID == 0 {
						log.Printf("notification Redis subscriber: 忽略缺少 recipient_id 的通知")
						continue
					}
					h.Push(notification.RecipientID, &notification)
				}
			}
		}
	}()
}

// Push
// 将通知发送到对应用户的channel
func (h *SSEHub) Push(recipient uint, n *Notification) {
	//上锁,防止其他goroutine写map时读map会panic
	//多个goroutine可以同时读map,所以用写锁
	h.mu.RLock()
	defer h.mu.RUnlock()
	//获取用户管道切片
	chs, ok := h.clients[recipient]
	if !ok {
		//key不存在,用户不存在
		return
	}
	for _, ch := range chs {
		select {
		case ch <- n: // channel缓冲区没满，通知放进去了，SSE handler那边会取走推给浏览器
		default: // channel缓冲区满了（20个），丢通知，不阻塞
			//通知不是必须实时送达的关键数据，宁可丢一条也不能让 Worker 卡死
		}
	}
}

// Subscribe
// 每开一个标签页,创建一个管道用来接收消息
// 返回对应的管道
func (h *SSEHub) Subscribe(userID uint) chan *Notification {
	//创建一个 channel,用来接收这个连接上要推送给用户的通知
	//Worker 会把通知塞进这个 channel
	ch := make(chan *Notification, 20)
	//上锁,防止并发写map会panic
	h.mu.Lock()
	defer h.mu.Unlock()
	//一个goroutine(标签页)对应一个管道,
	//否则多个goroutine从一个管道取数据,只能被读取一次
	//worker推送消息时往所有管道都发一份
	h.clients[userID] = append(h.clients[userID], ch)
	return ch
}

// Unsubscribe 用户关页面/连接断开时调用，把这个 channel 从 Hub 里清理掉。
func (h *SSEHub) Unsubscribe(userID uint, ch chan *Notification) {
	//修改map,写锁
	h.mu.Lock()
	defer h.mu.Unlock()
	//复制一份{指针、len、cap}
	chs := h.clients[userID]
	for i, c := range chs {
		//找到要删除的位置i
		if c == ch {
			//把0到i与i+1到最后的拼接起来
			chs = append(chs[:i], chs[i+1:]...)
			if len(chs) == 0 {
				//所有连接都断开了,从 map 里删除 key,
				//否则会出现一堆值是[]的key,浪费空间
				delete(h.clients, userID)
			} else {
				//更新后的chs写回h.clients[userID](len变了,-1)
				h.clients[userID] = chs
			}
			//关闭channel
			close(c)
			//直接返回
			return
		}
	}
}

// 获取账户id
func sseAccountID(c *gin.Context) (uint, bool) {
	accountID, ok := c.Get("accountID")
	if !ok {
		return 0, false
	}
	userID, ok := accountID.(uint)
	return userID, ok && userID != 0
}

// SSERequireAuth 为了 GET /stream 这个 SSE 连接
// SSE 是浏览器原生 EventSource 发起的，它只支持 GET 请求，
// 所以鉴权不能放 body，token 放在 query 参数 ?token=xxx 或者 Header 里
func (h *SSEHub) SSERequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		//获取查询参数token
		token := c.Query("token")
		if token == "" {
			token = c.GetHeader("Authorization")
			if len(token) > 7 && token[:7] == "Bearer " {
				token = token[7:]
			}
		}
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		//验证签名,过期时间,获取id,用户名
		claims, err := auth.ParseToken(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		c.Set("accountID", claims.AccountID)
		c.Next()
	}
}

// SSEHandler
// 与浏览器建立长连接
// 用户登录后，打开页面的时候，前端代码就会自动发起这个 GET 请求建立长连接，
// 这个连接一直保持，直到用户关页面/关浏览器
// 普通 HTTP：请求 → 响应 → 连接关闭，一次请求一次应答
// SSE：请求 → 连接一直开着 → 服务器随时往连接里推数据，多次推送
// SSE 不是什么特殊连接，它就是一个普通的 HTTP 请求 + 响应，核心技巧就是：提前写状态码，不 return，持续往 body 里写数据。
// SSE（Server-Sent Events）只是给这种用法起了个名字，加上了几个约定
func (h *SSEHub) SSEHandler(c *gin.Context) {
	//获取账户id
	userID, ok := sseAccountID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid account"})
		return
	}

	// 声明是SSE流,要保持连接，来一段处理一段 数据格式约定为 data: xxx\n\n
	// 浏览器原生提供了 EventSource 对象帮你解析这个格式、自动重连
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	// 告诉中间的代理服务器（Nginx/CDN/浏览器）：不要缓存这个响应,否则心跳/通知可能被缓存在代理服务器,下一次被直接返回.解决缓存重复使用
	c.Writer.Header().Set("Cache-Control", "no-cache")
	//告诉 Nginx：不要缓冲这个响应，来一点转发一点。不加的话，Nginx 默认会攒够4k或8k字节才发给浏览器，SSE实时性就废了.解决延迟发送
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	// 告诉中间代理这是长连接，别关
	c.Writer.Header().Set("Connection", "keep-alive")
	//立刻写头发出去,200 + 响应头立刻发到浏览器 → 浏览器知道连接建立了，准备好接收事件
	c.Writer.WriteHeader(http.StatusOK)

	//创建一个channel
	ch := h.Subscribe(userID)
	//连接断开,关闭相应channel
	defer h.Unsubscribe(userID, ch)

	//请求结束（用户关页面/断开连接）时，这个 ctx 会自动取消,ctx.Done()自动触发
	//后面的 select 里 case <-ctx.Done() 就会触发，
	//SSEHandler 退出循环，连接关闭
	ctx := c.Request.Context()
	/*
		如何创建子上下文
		在这个 ctx 基础上派生：
		// 加超时
		childCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// 加截止时间
		childCtx, cancel := context.WithDeadline(ctx, time.Now().Add(5*time.Second))
		// 加自定义值
		childCtx := context.WithValue(ctx, "key", "value")
		// 手动取消
		childCtx, cancel := context.WithCancel(ctx)
		子上下文会继承父上下文的取消信号，父 ctx 取消了，子 ctx 也跟着取消
	*/

	/*
		Writer    ResponseWriter
		Writer 真实类型是*http.response,实现了ResponseWriter和Flusher接口
		被ResponseWriter这个接口的变量接收,就只能用ResponseWriter的方法
		需要把Writer断言成Flusher接口(只要底层结构体实现了这个接口断言就成功)
		现在flusher底层是*http.response,被Flusher接口接收,能使用Flush()方法
		断言成具体类型需要底层就是这个类型,断言成接口只要求底层有接口的方法,不关心具体是什么类型
	*/
	flusher, _ := c.Writer.(http.Flusher)

	//监听当前用户当前页面对应的管道的消息*Notification
	for {
		select {
		case <-ctx.Done(): //tcp连接断开(关闭页面/断网),管道关闭,返回零值(struct{}{}),false
			return
			//从管道取出消息
		case n, ok := <-ch:
			if !ok {
				//其他情况,管道关闭,ok=false
				return
			}
			b, _ := json.Marshal(n)
			//把消息写到HTTP响应体里,
			//b := []byte(`{"id":1}`)
			//最终写出去：data: {"id":1}\n\n
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", b)
			if flusher != nil {
				//把缓冲区里的数据立刻发给浏览器
				//HTTP 响应默认是缓冲的，
				//你写的数据先存在内存缓冲区里，等到响应结束（handler 返回）才一次性发出去
				//但 SSE 是长连接，handler 永远不返回，必须手动 Flush 才能把数据推出去
				flusher.Flush()
			}
			//返回一个 <-chan Time（只读 channel），
			//这个 channel 在30秒后会自动写入一个时间值
		case <-time.After(30 * time.Second):
			//: 开头是 SSE 协议的注释，浏览器 EventSource.onmessage 不会触发，直接忽略
			//让中间代理/防火墙知道连接还活着，不要掐断
			_, _ = fmt.Fprintf(c.Writer, ": keepalive\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// ListHandler 用户打开通知列表,返回历史通知,包括点赞,评论,关注
// 需要查数据库
func (h *SSEHub) ListHandler(c *gin.Context) {
	userID, ok := sseAccountID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid account"})
		return
	}

	var notifications []Notification
	if err := h.db.WithContext(c.Request.Context()).
		Where("recipient_id = ?", userID).
		Order("created_at desc").
		Limit(50).
		Find(&notifications).Error; err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	if notifications == nil {
		notifications = []Notification{}
	}
	c.JSON(200, gin.H{"notifications": notifications})
}

// 标记已读,单条或多条
func (h *SSEHub) MarkReadHandler(c *gin.Context) {
	userID, ok := sseAccountID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid account"})
		return
	}

	var req struct {
		ID *uint `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var err error
	if req.ID != nil {
		//传 id → 标记单条已读
		err = h.db.WithContext(c.Request.Context()).
			Model(&Notification{}).Where("id = ? AND recipient_id = ?", *req.ID, userID).
			Update("is_read", true).Error
	} else {
		//不传 id → 标记全部已读
		err = h.db.WithContext(c.Request.Context()).
			Model(&Notification{}).Where("recipient_id = ?", userID).
			Update("is_read", true).Error
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "ok"})
}

// 统计未读消息的数量
func (h *SSEHub) UnreadCountHandler(c *gin.Context) {
	userID, ok := sseAccountID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid account"})
		return
	}

	//统计未读消息的数量
	var count int64
	if err := h.db.WithContext(c.Request.Context()).
		Model(&Notification{}).Where("recipient_id = ? AND is_read = ?", userID, false).
		Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"count": count})
}

func (h *SSEHub) RegisterRoutes(group *gin.RouterGroup) {
	//浏览器发来请求建立连接
	group.GET("/stream", h.SSEHandler)
	//通知列表
	group.POST("/list", h.ListHandler)
	//标记已读
	group.POST("/markRead", h.MarkReadHandler)
	//统计未读消息的数量
	group.POST("/unreadCount", h.UnreadCountHandler)
}
