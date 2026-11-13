package service

import (
	"context"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/cache"
	"github.com/Im-Sage/cloudtask-go/day9/internal/config"
	"github.com/Im-Sage/cloudtask-go/day9/internal/database"
	"github.com/Im-Sage/cloudtask-go/day9/internal/repository"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestConcurrentCacheMiss(t *testing.T) {
	cfg, err := config.Load()
	if err != nil{
		log.Printf("配置文件加载失败：%v", err)
		return
	}
	// 准备测试Service
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()
	pool, err := database.NewDBPool(ctx, cfg.Postgres)
	if err != nil {
		log.Printf("数据库初始化失败：%v", err)
		return
	}
	defer pool.Close()
	taskRepository := repository.NewTaskRepository(pool)

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
		5*time.Second)
	defer redisCancel()

	err = rdb.Ping(redisCtx).Err()
	if err != nil {
		t.Fatalf("Redis 暂不可用，继续启动服务：%v", err)
	}

	bloomKey := "cloudtask:task:bloom:build:" + uuid.NewString()
	log.Printf("本次布隆过滤器 key：%s", bloomKey)
	taskBloom := cache.NewTaskBloom(rdb, bloomKey)
	ids, err := taskRepository.GetIds(ctx)
	if err == nil {
		err = taskBloom.Build(ctx, ids)
	}
	if err != nil {
		log.Printf("构建过滤器失败：禁用否定拦截：%v", err)
		taskBloom = nil
	}

	taskCache := cache.NewTaskCache(rdb)

	taskService := NewTaskService(taskRepository, taskCache, taskBloom)

	// TODO 2: 创建 WaitGroup
	var wg sync.WaitGroup

	// TODO 3: 创建 start channel
	startChannel := make(chan struct{})

	if err := taskCache.DeleteTask(ctx, 12); err != nil {
		t.Fatalf("删除缓存失败：%v", err)
	}

	// TODO 4: 启动 50 个 Goroutine
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 因为管道内是空结构体，所以我们只在意管道传没传进来信号，
			<-startChannel // 阻塞，直到收到开始信号
			task, err := taskService.GetTaskByID(ctx, 12)
			if err != nil {
				t.Errorf("查询失败：%v", err)
				return
			}
			if task.ID != 12 {
				t.Errorf("期望 ID=12，实际 ID=%d", task.ID)
			}
		}()
	}
	close(startChannel)
	wg.Wait()

	// TODO 5: 每个 Goroutine 等待 start 信号

	// TODO 6: 同时调用 GetTaskByID(ctx, 12)

	// TODO 7: 等待全部请求结束并检查结果
}

func TestRandomTTL(t *testing.T) {
	cfg, err := config.Load()
	if err != nil{
		log.Printf("配置文件加载失败：%v", err)
		return
	}
	// 准备测试Service
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()
	pool, err := database.NewDBPool(ctx, cfg.Postgres)
	if err != nil {
		log.Printf("数据库初始化失败：%v", err)
		return
	}
	defer pool.Close()
	taskRepository := repository.NewTaskRepository(pool)

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
		t.Fatalf("Redis 暂不可用，继续启动服务：%v", err)
	}

	bloomKey := "cloudtask:task:bloom:build:" + uuid.NewString()
	log.Printf("本次布隆过滤器 key：%s", bloomKey)
	taskBloom := cache.NewTaskBloom(rdb, bloomKey)
	ids, err := taskRepository.GetIds(ctx)
	if err == nil {
		err = taskBloom.Build(ctx, ids)
	}
	if err != nil {
		log.Printf("构建过滤器失败：禁用否定拦截：%v", err)
		taskBloom = nil
	}

	taskCache := cache.NewTaskCache(rdb)

	taskService := NewTaskService(taskRepository, taskCache, taskBloom)

	// TODO 2: 创建 WaitGroup
	var wg sync.WaitGroup

	// TODO 3: 创建 start channel
	startChannel := make(chan struct{})

	// if err := taskCache.DeleteTask(ctx, 12); err != nil {
	// 	t.Fatalf("删除缓存失败：%v", err)
	// }

	// TODO 4: 启动 50 个 Goroutine
	for i := 11; i < 62; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			// 因为管道内是空结构体，所以我们只在意管道传没传进来信号，
			<-startChannel // 阻塞，直到收到开始信号
			_, err := taskService.GetTaskByID(ctx, id)
			if err != nil {
				t.Errorf("查询失败：%v", err)
				return
			}
			// if task.ID != 12 {
			// 	t.Errorf("期望 ID=12，实际 ID=%d", task.ID)
			// }
		}(int64(i))
	}
	close(startChannel)
	wg.Wait()
}
