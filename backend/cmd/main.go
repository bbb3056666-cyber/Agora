package main

import (
	"context"
	apphttp "github.com/bbb3056666-cyber/Agora/backend/internal/delivery/http"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/config"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/db"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/observability"
	"github.com/bbb3056666-cyber/Agora/backend/internal/platform/rabbitmq"
	rds "github.com/bbb3056666-cyber/Agora/backend/internal/platform/redis"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	//✅
	//-------------------------------------------------
	// 加载根目录 .env 文件的键值对到进程环境变量。
	// 之后 os.Getenv("MYSQL_ROOT_PASSWORD") 就能读到文件里配置的值。
	// 环境变量:键值对形式
	// 进程级
	_ = godotenv.Load(".env")    //当前目录下的.env(./.env),在根目录下执行有效
	_ = godotenv.Load("../.env") //父目录下的.env,在cmd下执行有效

	// 决定用哪个配置文件：先读环境变量 CONFIG_PATH，
	// 没设置则用写死的 configs/config.json
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/config.compose-local.json"
	}
	log.Printf("Loading config from %s", configPath)

	//尝试根据文件路径将配置映射到结构体
	cfg, useDefault, err := config.LoadLocalDev(configPath)
	//文件损坏,直接终止
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if useDefault {
		//路径不存在,使用默认配置
		log.Printf("Config File %s not found, using default local config", configPath)
	} else {
		//使用了配置文件
		log.Printf("Config loaded from file: %s", configPath)
	}

	//✅
	//-------------------------------------------------
	// 连接数据库
	log.Printf("Database config: host=%s port=%d dbname=%s", cfg.Database.Host, cfg.Database.Port, cfg.Database.DBName)
	//返回连接句柄
	gormDB, err := db.NewDB(cfg.Database, cfg.DatabasePool.API)
	if err != nil {
		log.Fatalf("Failed to connect database: %v", err)
	}
	//自动建/更新表
	if err := db.AutoMigrate(gormDB); err != nil {
		log.Fatalf("Failed to auto migrate database: %v", err)
	}
	//关闭数据库连接
	defer db.CloseDB(gormDB)

	//✅
	//-------------------------------------------------
	// 连接 Redis (可选，用于缓存)
	cache, err := rds.NewFromCfg(&cfg.Redis)
	if err != nil {
		log.Printf("Redis config error (cache disabled): %v", err)
		cache = nil
	} else {
		//创建一个带超时的上下文,设置函数执行时间上限
		//超时ctx.Err() 返回 context.DeadlineExceeded
		pingCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		if err := cache.Ping(pingCtx); err != nil {
			log.Printf("Redis not available (cache disabled): %v", err)
			_ = cache.Close()
			cache = nil
		} else {
			//退出前关闭连接
			defer func(cache *rds.Client) {
				err := cache.Close()
				if err != nil {
					log.Printf("Redis failed to close: %v", err)
				}
			}(cache)
			log.Printf("Redis connected (cache enabled)")
		}
	}

	//✅
	//-------------------------------------------------
	// 连接 RabbitMQ (可选，用于消息队列)
	rmq, err := connectRabbitMQWithRetry(&cfg.RabbitMQ, 10)
	if err != nil {
		log.Printf("RabbitMQ unavailable after retries (disabled): %v", err)
		rmq = nil
	} else {
		//关闭连接
		defer rmq.Close()
		log.Printf("RabbitMQ connected")
	}

	// GORM 包装了 database/sql。将底层连接池传给调试服务，供压测时读取等待统计。
	apiSQLDB, err := gormDB.DB()
	if err != nil {
		log.Printf("Failed to get API database pool for debug stats: %v", err)
		apiSQLDB = nil
	}
	pprofServer, err := observability.NewPprofServer("API", cfg.ObservabilityConfig.Pprof.Enabled, cfg.ObservabilityConfig.Pprof.ApiAddr, apiSQLDB)
	if err != nil {
		log.Printf("Failed to start API pprof server: %v", err)
	}
	if pprofServer != nil {
		defer pprofServer.Close()
	}

	//-------------------------------------------------
	// 设置路由
	// 注册所有 API 路由，返回 Gin 引擎
	r := apphttp.SetRouter(gormDB, cache, rmq)
	log.Printf("Server is running on port %d", cfg.Server.Port)

	// 当前进程内监听 8080 端口，阻塞等待 HTTP 请求
	port := ":" + strconv.Itoa(cfg.Server.Port) //:8080
	//底层调用http.ListenAndServe，启动TCP监听，
	//在后台循环接收HTTP请求、新开一个goroutine,调用相应的handler处理
	if err := r.Run(port); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

// connectRabbitMQWithRetry 避免容器刚启动时 Broker 尚未监听，API 却永久以“MQ 已禁用”状态运行。
func connectRabbitMQWithRetry(cfg *config.RabbitMQConfig, maxRetries int) (*rabbitmq.RabbitMQ, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		rmq, err := rabbitmq.NewRabbitMQ(cfg)
		if err == nil {
			return rmq, nil
		}
		lastErr = err
		wait := time.Duration(1<<attempt) * time.Second
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		log.Printf("RabbitMQ unavailable, retrying in %v (%d/%d): %v", wait, attempt+1, maxRetries, err)
		time.Sleep(wait)
	}
	return nil, lastErr
}
