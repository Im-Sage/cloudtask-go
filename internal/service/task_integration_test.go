//go:build integration

package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Im-Sage/cloudtask-go/internal/cache"
	"github.com/Im-Sage/cloudtask-go/internal/config"
	"github.com/Im-Sage/cloudtask-go/internal/database"
	"github.com/Im-Sage/cloudtask-go/internal/model"
	"github.com/Im-Sage/cloudtask-go/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func newRedis(t *testing.T) *redis.Client {
	t.Helper()
	// 初始化redis连接
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatalf("未设置 TEST_REDIS_ADDR")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Username: os.Getenv("TEST_REDIS_USERNAME"),
		Password: os.Getenv("TEST_REDIS_PASSWORD"),
		DB:       0,
	})
	t.Cleanup(func() {
		if err := rdb.Close(); err != nil {
			t.Errorf("关闭测试 Redis 客户端失败：%v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("连接测试 Redis 失败：%v", err)
	}

	return rdb
}

// 辅助函数 数据库准备工作
func newRepository(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// 读取配置，这里有误，不应该使用生产环境的配置，应新建一个测试环境的配置
	// cfg, err := config.Load()
	dsn := os.Getenv("TEST_DATABASE_URL")
	var pgCfg = config.PostgresConfig{
		DSN:         dsn,
		MaxConns:    4,
		MinConns:    0,
		PingTimeout: 5 * time.Second,
	}
	if pgCfg.DSN == "" {
		t.Fatalf("配置读取错误:未设置DSN")
	}
	// 设置超时和取消时间
	dbCtx, dbCancel := context.WithTimeout(
		context.Background(),
		6*time.Second,
	)
	defer dbCancel()
	pool, err := database.NewDBPool(dbCtx, pgCfg)
	if err != nil {
		t.Fatalf("数据库初始化错误%v", err)
	}
	return pool
}

// 验证 Service 缓存未命中、命中、更新失效及重新回填。
func TestTaskService_CacheAside(t *testing.T) {
	pool := newRepository(t)
	t.Cleanup(pool.Close)
	rdb := newRedis(t)
	repo := repository.NewTaskRepository(pool)
	taskCache := cache.NewTaskCache(rdb)
	taskService := NewTaskService(repo, taskCache, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	original, err := repo.CreateTask(ctx, "缓存测试任务", "测试数据")
	if err != nil {
		t.Fatalf("创建测试任务失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := taskCache.DeleteTask(cleanupCtx, original.ID); err != nil {
			t.Errorf("清理测试缓存失败：%v", err)
		}
		if err := repo.DeleteTask(cleanupCtx, original.ID); err != nil {
			t.Errorf("清理测试任务失败：%v", err)
		}
	})

	assertTask := func(stage string, got, want model.Task) {
		t.Helper()
		if got.ID != want.ID || got.Title != want.Title || got.Description != want.Description ||
			got.Status != want.Status || got.Version != want.Version {
			t.Fatalf("%s：任务数据不一致，实际 %+v，预期 %+v", stage, got, want)
		}
	}
	assertCache := func(stage string, want model.Task, wantHit bool) {
		t.Helper()
		got, err := taskCache.GetResult(ctx, original.ID)
		if err != nil {
			t.Fatalf("%s：读取 Redis 失败：%v", stage, err)
		}
		if got.Hit != wantHit {
			t.Fatalf("%s：Hit=%t，预期 %t", stage, got.Hit, wantHit)
		}
		if wantHit {
			if !got.Exists {
				t.Fatalf("%s：命中了空值缓存", stage)
			}
			assertTask(stage, got.Task, want)
		}
	}

	if err := taskCache.DeleteTask(ctx, original.ID); err != nil {
		t.Fatalf("准备缓存 Miss 失败：%v", err)
	}
	assertCache("首次读取前", original, false)
	first, err := taskService.GetTaskByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("首次通过 Service 读取任务失败：%v", err)
	}
	assertTask("首次读取", first, original)
	assertCache("首次读取后回填", original, true)

	// 只修改数据库里的标题，给第二次读取准备两个可区分的数据来源。
	if _, err := pool.Exec(ctx, "UPDATE tasks SET title = $1 WHERE id = $2", "仅数据库中的标题", original.ID); err != nil {
		t.Fatalf("准备缓存命中场景失败：%v", err)
	}
	second, err := taskService.GetTaskByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("第二次通过 Service 读取任务失败：%v", err)
	}
	assertTask("第二次读取应返回缓存数据", second, original)

	wantUpdated := original
	wantUpdated.Title = "缓存更新后的标题"
	wantUpdated.Status = "in_progress"
	wantUpdated.Version++
	updated, err := taskService.UpdateTask(ctx, original.ID, wantUpdated.Title, wantUpdated.Status)
	if err != nil {
		t.Fatalf("通过 Service 更新任务失败：%v", err)
	}
	assertTask("更新返回值", updated, wantUpdated)
	assertCache("更新后旧缓存已删除", original, false)

	reloaded, err := taskService.GetTaskByID(ctx, original.ID)
	if err != nil {
		t.Fatalf("更新后通过 Service 读取任务失败：%v", err)
	}
	assertTask("更新后读取", reloaded, wantUpdated)
	assertCache("更新后回填新任务", wantUpdated, true)
}
