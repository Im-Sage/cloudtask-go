package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/cache"
	"github.com/Im-Sage/cloudtask-go/day9/internal/config"
	"github.com/Im-Sage/cloudtask-go/day9/internal/database"
	"github.com/Im-Sage/cloudtask-go/day9/internal/handler"
	"github.com/Im-Sage/cloudtask-go/day9/internal/repository"
	"github.com/Im-Sage/cloudtask-go/day9/internal/service"
	"github.com/redis/go-redis/v9"

	"github.com/google/uuid"
)

func cleanupOldBloomKeys(
	ctx context.Context,
	rdb *redis.Client,
	currentKey string,
) error {
	const pattern = "cloudtask:task:bloom:build:*"

	var cursor uint64
	for {
		keys, next, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("扫描旧过滤器失败：%w", err)
		}

		var oldKeys []string
		for _, key := range keys {
			if key != currentKey {
				oldKeys = append(oldKeys, key)
			}
		}

		if len(oldKeys) > 0 {
			if err := rdb.Unlink(ctx, oldKeys...).Err(); err != nil {
				return fmt.Errorf("清理旧过滤器失败：%w", err)
			}
		}

		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout,
		&slog.HandlerOptions{
			Level: slog.LevelInfo,
		},
	))
	if err := run(logger); err != nil {
		logger.Error("服务退出", "error", err)
		os.Exit(1)
	}
}

// 初始化连接池、配置路由、启动HTTP服务
func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		// log.Printf("配置文件加载失败：%v", err)
		return fmt.Errorf("加载配置失败：%w", err)
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		cfg.StartupTimeout)
	defer cancel()
	pool, err := database.NewDBPool(ctx, cfg.Postgres) // 先把连接池初始化好
	if err != nil {
		// log.Fatalf("Failed to create connection pool: %v", err)
		return fmt.Errorf("数据库加载或连接失败：%w", err)
	}

	defer pool.Close() // 连接池成功创建，关闭应该由调用方负责

	// 初始化redis连接
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Username: cfg.Redis.Username,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer rdb.Close()

	redisCtx, redisCancel := context.WithTimeout(
		ctx,
		cfg.Redis.PingTimeout)
	defer redisCancel()

	err = rdb.Ping(redisCtx).Err()
	if err != nil {
		// log.Printf("Redis 暂不可用，继续启动服务：%v", err)
		logger.Warn("Redis 服务不可用，继续使用数据库",
			"error", err)
	}

	taskRepository := repository.NewTaskRepository(pool)

	bloomKey := "cloudtask:task:bloom:build:" + uuid.NewString()
	taskBloom := cache.NewTaskBloom(rdb, bloomKey)
	ids, err := taskRepository.GetIds(ctx)
	if err == nil {
		err = taskBloom.Build(ctx, ids)
	}
	if err != nil {
		logger.Warn("构建过滤器失败：禁用否定拦截",
			"error", err)
		taskBloom = nil

		cleanupCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			time.Second,
		)
		// 清理失败时的半成品
		cleanupErr := rdb.Unlink(cleanupCtx, bloomKey).Err()
		cancel()

		if cleanupErr != nil {
			logger.Warn("清理构建失败的过滤器失败：",
				"key", bloomKey,
				"error", err)
		}
	} else {
		log.Printf("布隆过滤器构建成功：key=%s", bloomKey)
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			3*time.Second,
		)
		cleanupErr := cleanupOldBloomKeys(cleanupCtx, rdb, bloomKey)
		cleanupCancel()

		if cleanupErr != nil {
			logger.Warn("清理旧过滤器失败",
				"error", cleanupErr)
		}
	}

	taskCache := cache.NewTaskCache(rdb)

	taskService := service.NewTaskService(taskRepository, taskCache, taskBloom)

	router := handler.NewRouter(taskService, pool)

	logger.Info("准备启动 HTTP 服务", "addr", cfg.Http.Addr)
	// 创建http.Server
	server := &http.Server{
		Addr: cfg.Http.Addr,
		// 处理请求的路由
		Handler:           router,
		ReadHeaderTimeout: cfg.Http.ReadHeaderTimeout,
		WriteTimeout:      cfg.Http.WriteTimeout,
		// 	保持连接时，等待下一次请求的最长空闲时间
		IdleTimeout: cfg.Http.IdleTimeout,
	}

	sinalCtx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	serverErr := make(chan error, 1)

	go func() {
		serverErr <- server.ListenAndServe()
	}()

	select {
	// 从错误通道里取错误
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			// 发生意外崩溃或底层网络错误而退出
			return fmt.Errorf("HTTP 服务失败: %w", err)
		}
		return nil

	case <-sinalCtx.Done():
		// HTTP 服务器是通过显式调用 Server.Shutdown()
		// 或 Server.Close() 被正常关闭的，
		// 而不是因为发生意外崩溃或底层网络错误而退出的。
		logger.Info("收到退出信号，开始关闭服务")
		stop()

		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("优雅关闭失败，强制关闭连接", "error", err)

			if closeErr := server.Close(); closeErr != nil {
				logger.Error("强制关闭失败", "error", closeErr)
			}
			return fmt.Errorf("HTTP 服务优雅关闭失败: %w", err)
		}
		logger.Info("HTTP 服务已关闭")
		return nil
	}
}
