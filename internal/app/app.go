package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Im-Sage/cloudtask-go/internal/cache"
	"github.com/Im-Sage/cloudtask-go/internal/config"
	"github.com/Im-Sage/cloudtask-go/internal/database"
	"github.com/Im-Sage/cloudtask-go/internal/handler"
	"github.com/Im-Sage/cloudtask-go/internal/repository"
	"github.com/Im-Sage/cloudtask-go/internal/service"
	"github.com/redis/go-redis/v9"
)

// 初始化连接池、配置路由、启动HTTP服务
func Run(logger *slog.Logger) error {
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
	var taskBloom *cache.TaskBloom
	ids, err := taskRepository.GetIDs(ctx)
	if err != nil {
		// 任务读取失败，跳过布隆过滤器降级执行
		logger.Warn("读取任务id失败", "error", err)
	} else {
		taskBloom, err = cache.BuildTaskBloom(ctx, rdb, ids, logger)
		if err != nil {
			logger.Warn("构建 Bloom 过滤器失败，继续启动服务", "error", err)
		}
	}

	taskCache := cache.NewTaskCache(rdb)

	taskService := service.NewTaskService(taskRepository, taskCache, taskBloom)

	router := handler.NewRouter(taskService, pool)

	logger.Info("准备启动 HTTP 服务", "addr", cfg.HTTP.Addr)
	// if err := runServer(logger, cfg, router); err != nil{
	// 	return fmt.Errorf("http关闭失败：%w", err)
	// }
	//return nil
	return runServer(logger, cfg, router)
}
