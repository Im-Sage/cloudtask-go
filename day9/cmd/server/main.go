package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/cache"
	"github.com/Im-Sage/cloudtask-go/day9/internal/database"
	"github.com/Im-Sage/cloudtask-go/day9/internal/handler"
	"github.com/Im-Sage/cloudtask-go/day9/internal/repository"
	"github.com/Im-Sage/cloudtask-go/day9/internal/service"
	"github.com/joho/godotenv"
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

// 初始化连接池、配置路由、启动HTTP服务

func main() {
	if err := godotenv.Load("../../.env"); err != nil {
		fmt.Println("加载 .env 失败:", err)
		return
	}
	dsn := "postgres://app:dev_password_only_local@localhost:5432/cloudtask?sslmode=disable"
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second)
	defer cancel()

	pgCtx, pgCancel := context.WithTimeout(
		ctx,
		5*time.Second)
	defer pgCancel()
	pool, err := database.NewDBPool(pgCtx, dsn) // 先把连接池初始化好
	if err != nil {
		log.Fatalf("Failed to create connection pool: %v", err)
	}

	defer pool.Close() // 连接池成功创建，关闭应该由调用方负责

	// 初始化redis连接
	rdb := redis.NewClient(&redis.Options{
		Addr:     "127.0.0.1:6379",
		Username: "default",
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       0,
	})
	defer rdb.Close()

	redisCtx, redisCancel := context.WithTimeout(
		ctx,
		5*time.Second)
	defer redisCancel()

	err = rdb.Ping(redisCtx).Err()
	if err != nil {
		log.Printf("Redis 暂不可用，继续启动服务：%v", err)
	}

	taskRepository := repository.NewTaskRepository(pool)

	bloomKey := "cloudtask:task:bloom:build:" + uuid.NewString()
	taskBloom := cache.NewTaskBloom(rdb, bloomKey)
	ids, err := taskRepository.GetIds(ctx)
	if err == nil {
		err = taskBloom.Build(ctx, ids)
	}
	if err != nil {
		log.Printf("构建过滤器失败：禁用否定拦截：%v", err)
		taskBloom = nil

		cleanupCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			time.Second,
		)
		// 清理失败时的半成品
		cleanupErr := rdb.Unlink(cleanupCtx, bloomKey).Err()
		cancel()

		if cleanupErr != nil {
			log.Printf("清理构建失败的过滤器失败：key=%s err=%v",
				bloomKey, cleanupErr)
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
			log.Printf("清理旧过滤器失败：%v", cleanupErr)
		}
	}

	taskCache := cache.NewTaskCache(rdb)

	taskService := service.NewTaskService(taskRepository, taskCache, taskBloom)

	router := handler.NewRouter(taskService)

	// 启动HTTP服务
	addr := ":8080"
	fmt.Printf("Starting server on %s\n", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
