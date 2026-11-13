package service

import (
	"context"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/cache"
	"github.com/Im-Sage/cloudtask-go/day9/internal/database"
	"github.com/Im-Sage/cloudtask-go/day9/internal/repository"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/Im-Sage/cloudtask-go/day9/internal/config"
)

func TestRedisStop(t *testing.T) {
	cfg, err := config.Load()
	if err != nil{
		log.Printf("配置文件加载失败：%v", err)
		return
	}
	initCtx, initCancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer initCancel()
	pool, err := database.NewDBPool(initCtx, cfg.Postgres)
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
	redisCtx, redisCancel := context.WithTimeout(
		initCtx,
		500*time.Millisecond,
	)
	defer rdb.Close()

	defer redisCancel()

	err = rdb.Ping(redisCtx).Err()
	if err != nil {
		log.Printf("Redis 暂不可用，继续启动服务：%v", err)
	}

	bloomKey := "cloudtask:task:bloom:build:" + uuid.NewString()
	log.Printf("本次布隆过滤器 key：%s", bloomKey)
	taskBloom := cache.NewTaskBloom(rdb, bloomKey)
	ids, err := taskRepository.GetIds(initCtx)
	if err == nil {
		err = taskBloom.Build(initCtx, ids)
	}
	if err != nil {
		log.Printf("构建过滤器失败：禁用否定拦截：%v", err)
		taskBloom = nil
	}

	taskCache := cache.NewTaskCache(rdb)

	taskService := NewTaskService(taskRepository, taskCache, taskBloom)

	// 测试id=12
	// task, err := taskService.GetTaskByID(queryCtx, 12)
	// if err != nil {
	// 	t.Fatalf("查找任务12失败：%v", err)
	// }
	// fmt.Println(task)

	var wg sync.WaitGroup

	startChannel := make(chan struct{})

	// slots := make(chan struct{}, 10)		// 容量为10的Channel

	// 占用名额		slots <- struct{}{}
	// 释放名额		<- slots

	// if err := taskCache.DeleteTask(ctx, 12); err != nil {
	// 	t.Fatalf("删除缓存失败：%v", err)
	// }

	for i := 11; i < 62; i++ {
		wg.Add(1)
		go func(id int64) {
			// 为查询重新创建context
			queryCtx, queryCancel := context.WithTimeout(
				context.Background(),
				3*time.Second,
			)
			defer queryCancel()
			defer wg.Done()
			// 因为管道内是空结构体，所以我们只在意管道传没传进来信号，
			<-startChannel // 阻塞，直到收到开始信号
			_, err := taskService.GetTaskByID(queryCtx, id)
			if err != nil {
				t.Errorf("查询失败：%v", err)
				return
			}
		}(int64(i))
	}
	close(startChannel)
	wg.Wait()
}
