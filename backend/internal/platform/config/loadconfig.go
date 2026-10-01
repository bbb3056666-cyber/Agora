package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// ✅
type Config struct {
	Server              ServerConfig        `json:"server"`
	Database            DatabaseConfig      `json:"database"`
	DatabasePool        DatabasePoolsConfig `json:"database_pool"`
	Redis               RedisConfig         `json:"redis"`
	RabbitMQ            RabbitMQConfig      `json:"rabbitmq"`
	ObservabilityConfig ObservabilityConfig `json:"observability"`
}

type ServerConfig struct {
	Port int `json:"port"`
}

type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
}

// DatabasePoolConfig controls one process's database/sql connection pool.
// Durations are expressed in seconds in JSON to keep configuration portable.
type DatabasePoolConfig struct {
	MaxOpenConns           int `json:"max_open_conns"`
	MaxIdleConns           int `json:"max_idle_conns"`
	ConnMaxLifetimeSeconds int `json:"conn_max_lifetime_seconds"`
	ConnMaxIdleTimeSeconds int `json:"conn_max_idle_time_seconds"`
}

// DatabasePoolsConfig keeps API and worker connection budgets separate because
// they are independent processes and therefore own independent pools.
type DatabasePoolsConfig struct {
	API    DatabasePoolConfig `json:"api"`
	Worker DatabasePoolConfig `json:"worker"`
}

func DefaultDatabasePoolConfig() DatabasePoolConfig {
	return DatabasePoolConfig{
		MaxOpenConns:           10,
		MaxIdleConns:           5,
		ConnMaxLifetimeSeconds: 1800,
		ConnMaxIdleTimeSeconds: 300,
	}
}

type RedisConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
	DB       int    `json:"db"` //哪个redis数据库
}

type RabbitMQConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type ObservabilityConfig struct {
	Pprof PprofConfig `json:"pprof"`
}
type PprofConfig struct {
	Enabled    bool   `json:"enabled"`
	ApiAddr    string `json:"api_addr"`
	WorkerAddr string `json:"worker_addr"`
}

// ✅
// 加载配置文件到结构体,不存在则用默认值,文件损坏则报错
func LoadLocalDev(filename string) (Config, bool, error) {
	//将路径对应的配置文件映射到结构体
	cfg, err := Load(filename)
	//成功,直接返回
	if err == nil {
		return cfg, false, nil
	}

	//如果是文件不存在
	if errors.Is(err, os.ErrNotExist) {
		return DefaultLocalConfig(), true, nil // 文件不存在，用硬编码默认值
	}
	//如果是文件损坏,直接返回空配置和错误,上层main直接终止程序
	return Config{}, false, err // 文件损坏等真错误
}

// ✅
// 读文件,反序列化写入结构体
// 读字节流 → json.Unmarshal 解析 → ApplyEnvOverrides 覆盖
func Load(filename string) (Config, error) {
	//一次性把整个文件的内容全部读出来，返回[]byte字节切片
	//适合读小文件（比如配置文件，几KB到几MB）
	data, err := os.ReadFile(filename)
	if err != nil {
		return Config{}, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	//按json格式反序列化写入结构体
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", filename, err)
	}

	ApplyEnvOverrides(&cfg) // 用环境变量覆盖配置值，环境变量优先级高于文件
	return cfg, nil
}

// ✅
// 用环境变量覆盖配置
func ApplyEnvOverrides(cfg *Config) {
	if cfg == nil {
		return
	}
	if v := os.Getenv("SERVER_PORT"); v != "" {
		//string conversion
		//ASCII to integer
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}
	if v := os.Getenv("MYSQL_HOST"); v != "" {
		cfg.Database.Host = v
	}
	if v := os.Getenv("MYSQL_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Database.Port = port
		}
	}
	if v := os.Getenv("MYSQL_USER"); v != "" {
		cfg.Database.User = v
	}
	if v := os.Getenv("MYSQL_ROOT_PASSWORD"); v != "" {
		cfg.Database.Password = v
	}
	if v := os.Getenv("MYSQL_PASSWORD"); v != "" {
		cfg.Database.Password = v
	}
	if v := os.Getenv("MYSQL_DATABASE"); v != "" {
		cfg.Database.DBName = v
	}
	if v := os.Getenv("REDIS_HOST"); v != "" {
		cfg.Redis.Host = v
	}
	if v := os.Getenv("REDIS_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Redis.Port = port
		}
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("REDIS_DB"); v != "" {
		if db, err := strconv.Atoi(v); err == nil {
			cfg.Redis.DB = db
		}
	}
	if v := os.Getenv("RABBITMQ_HOST"); v != "" {
		cfg.RabbitMQ.Host = v
	}
	if v := os.Getenv("RABBITMQ_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.RabbitMQ.Port = port
		}
	}
	if v := os.Getenv("RABBITMQ_USER"); v != "" {
		cfg.RabbitMQ.Username = v
	}
	if v := os.Getenv("RABBITMQ_PASS"); v != "" {
		cfg.RabbitMQ.Password = v
	}
}

// ✅
// 加载硬编码配置
func DefaultLocalConfig() Config {
	cfg := Config{
		Server: ServerConfig{
			Port: 8080,
		},
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     3306,
			User:     "root",
			Password: "123456",
			DBName:   "agora",
		},
		DatabasePool: DatabasePoolsConfig{
			API: DatabasePoolConfig{
				MaxOpenConns:           80,
				MaxIdleConns:           40,
				ConnMaxLifetimeSeconds: 1800,
				ConnMaxIdleTimeSeconds: 300,
			},
			Worker: DatabasePoolConfig{
				MaxOpenConns:           24,
				MaxIdleConns:           12,
				ConnMaxLifetimeSeconds: 1800,
				ConnMaxIdleTimeSeconds: 300,
			},
		},
		Redis: RedisConfig{
			Host:     "localhost",
			Port:     6379,
			Password: "123456",
			DB:       0,
		},
		RabbitMQ: RabbitMQConfig{
			Host:     "localhost",
			Port:     5672,
			Username: "admin",
			Password: "password123",
		},
		ObservabilityConfig: ObservabilityConfig{
			Pprof: PprofConfig{
				Enabled:    true,
				ApiAddr:    "localhost:6060",
				WorkerAddr: "localhost:6061",
			},
		},
	}
	ApplyEnvOverrides(&cfg) // 用环境变量覆盖配置值，环境变量优先级高于文件
	return cfg
}
