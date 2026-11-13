package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Http     HttConfig
	Postgres PostgresConfig
	Redis    RedisConfig

	StartupTimeout time.Duration
}

type HttConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type PostgresConfig struct {
	DSN         string
	MaxConns    int32
	MinConns    int32
	PingTimeout time.Duration
}

type RedisConfig struct {
	Addr        string
	Username    string
	Password    string
	DB          int
	PingTimeout time.Duration
}

func (c Config) Validate() error {
	if c.Postgres.DSN == "" {
		return fmt.Errorf("DATABASE_URL 不能为空")
	}
	if c.Postgres.MaxConns <= 0 {
		return fmt.Errorf("PG_MAX_CONNS 必须大于 0")
	}
	if c.Postgres.MinConns < 0 ||
		c.Postgres.MinConns > c.Postgres.MaxConns {
		return fmt.Errorf("PG_MIN_CONNS 必须在 0 到 PG_MAX_CONNS 之间")
	}
	if c.Redis.DB < 0 {
		return fmt.Errorf("REDIS_DB 不能为负数")
	}
	if c.StartupTimeout <= 0 {
		return fmt.Errorf("启动超时必须大于 0")
	}
	return nil
}

func Load() (Config, error) {
	// 设置默认值、读取环境变量、校验配置
	if err := godotenv.Load("day9/.env"); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("加载 .env 失败: %w", err)
		}
	}

	var httpStru HttConfig
	httpStru.Addr = envOrDefault("HTTP_ADDR", ":8080")
	readHeaderTimeout, err := durationFromEnv("HTTP_READ_HEADER_TIMEOUT", "5s")
	if err != nil {
		return Config{}, err
	}
	httpStru.ReadHeaderTimeout = readHeaderTimeout
	writeTimeout, err := durationFromEnv("HTTP_WRITE_TIMEOUT", "10s")
	if err != nil {
		return Config{}, err
	}
	httpStru.WriteTimeout = writeTimeout
	idleTimeout, err := durationFromEnv("HTTP_IDLE_TIMEOUT", "60s")
	if err != nil {
		return Config{}, err
	}
	httpStru.IdleTimeout = idleTimeout

	var postgresStru PostgresConfig
	postgresStru.DSN = os.Getenv("DATABASE_URL")
	mc := os.Getenv("PG_MAX_CONNS")
	if mc == "" {
		mc = "4"
	}
	maxConns, err := strconv.ParseInt(mc, 10, 32)
	if err != nil {
		return Config{}, fmt.Errorf("PG_MAX_CONNS 必须是 int32 范围内的整数: %w", err)
	}
	postgresStru.MaxConns = int32(maxConns)
	minConns, err := strconv.ParseInt(envOrDefault("PG_MIN_CONNS", "0"), 10, 32)
	if err != nil {
		return Config{}, fmt.Errorf("PG_MIN_CONNS 必须是 int32 范围内的整数: %w", err)
	}
	postgresStru.MinConns = int32(minConns)
	pgPingTimeout, err := durationFromEnv("PG_PING_TIMEOUT", "5s")
	if err != nil {
		return Config{}, err
	}
	postgresStru.PingTimeout = pgPingTimeout

	var redisStru RedisConfig
	redisStru.Addr = envOrDefault("REDIS_ADDR", "127.0.0.1:6379")
	redisStru.Username = envOrDefault("REDIS_USERNAME", "default")
	redisStru.Password = os.Getenv("REDIS_PASSWORD")
	dbstr := os.Getenv("REDIS_DB")
	if dbstr == "" {
		dbstr = "0"
	}
	db, err := strconv.Atoi(dbstr)
	if err != nil {
		return Config{}, fmt.Errorf("REDIS_DB 必须是整数: %w", err)
	}
	redisStru.DB = db
	redisPingTimeout, err := durationFromEnv("REDIS_PING_TIMEOUT", "3s")
	if err != nil {
		return Config{}, err
	}
	redisStru.PingTimeout = redisPingTimeout

	startupTimeout, err := durationFromEnv("STARTUP_TIMEOUT", "15s")
	if err != nil {
		return Config{}, err
	}
	var cfg = Config{
		Http:           httpStru,
		Postgres:       postgresStru,
		Redis:          redisStru,
		StartupTimeout: startupTimeout,
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// 空值使用默认配置；DATABASE_URL、REDIS_PASSWORD 等凭据仍直接读取。
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// 时间配置需要带单位，例如 500ms、5s、1m。
func durationFromEnv(key, fallback string) (time.Duration, error) {
	value, err := time.ParseDuration(envOrDefault(key, fallback))
	if err != nil {
		return 0, fmt.Errorf("%s 必须是带单位的时间: %w", key, err)
	}
	return value, nil
}

// func main() {
// 	ctx, cancel := context.WithCancel(
// 		context.Background(),
// 	)
// 	defer cancel()
// 	databaseURL := "postgres://app:dev_password_only_local@localhost:5432/cloudtask?sslmode=disable"
// 	config, err := pgxpool.ParseConfig(databaseURL)
// 	if err != nil {
// 		log.Fatalf("错误%v", err)
// 	}
// 	config.MaxConns = 3
// 	config.MinConns = 1

// 	pool, err := pgxpool.NewWithConfig(ctx, config)
// 	if err != nil {
// 		log.Fatalf("错误%v", err)
// 	}

// 	pingCtx, pingCancel := context.WithTimeout(
// 		ctx,   // 以ctx为父上下文
// 		3 * time.Second,
// 	)
// 	defer pingCancel()

// 	if err := pool.Ping(pingCtx); err != nil {
// 		pool.Close()
// 		log.Fatalf("数据库连接失败：%v", err)
// 	}
// 	defer pool.Close()
// 	var wg sync.WaitGroup

// 	for i := 0; i < 10; i++ {
// 		wg.Add(1)
// 		go func ()  {
// 			defer wg.Done()
// 			_, err := pool.Exec(ctx, "select pg_sleep(0.2)")
// 			if err != nil {
// 				log.Fatalf("select 执行失败：%v", err)
// 			}
// 		}()
// 	}
// 	wg.Wait()

// 	stats := pool.Stat()
// 	fmt.Println("总连接数：", stats.TotalConns())
// 	fmt.Println("已使用连接：", stats.AcquiredConns())
// 	fmt.Println("空闲连接：", stats.IdleConns())
// 	fmt.Println("累计获取次数：", stats.AcquireCount())
// 	fmt.Println("累计等待时间：", stats.AcquireDuration())
// }
