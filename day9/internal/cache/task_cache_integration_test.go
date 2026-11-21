//go:build integration

package cache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/model"
	"github.com/redis/go-redis/v9"
)

// 第一步仍然是创建辅助函数来初始化Redis
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

// 查询不存在的Key
func TestTaskCache_GetResult_MissingKey(t *testing.T) {
	rdb := newRedis(t)
	taskCache := NewTaskCache(rdb)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()
	const taskID int64 = 12345

	// 确保cache中没有这个id的任务
	if err := taskCache.DeleteTask(ctx, taskID); err != nil {
		t.Fatalf("清理测试缓存失败：%v", err)
	}
	got, err := taskCache.GetResult(ctx, taskID)
	if err != nil {
		t.Fatalf("查询缓存失败：%v", err)
	}
	if got.Hit {
		t.Fatalf("任务 %d 的缓存应为 Miss，实际 Hit=true", taskID)
	}
}

// 写入后能读回 Hit=='true' 'Exits==true', 任务字段符合预期
func TestTaskCache_SetEntry_CanReadBackTask(t *testing.T) {
	rdb := newRedis(t)
	task_cache := NewTaskCache(rdb)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()
	task := model.Task{
		ID:      123456,
		Title:   "缓存读回测试",
		Status:  "pending",
		Version: 1,
	}
	err := task_cache.SetEntry(ctx, task.ID, task)
	if err != nil {
		t.Fatalf("任务写入缓存失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := task_cache.DeleteTask(cleanupCtx, task.ID); err != nil {
			t.Errorf("清理测试缓存失败：%v", err)
		}
	})
	// 再从缓存中读取东西
	result, err := task_cache.GetResult(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取缓存失败：%v", err)
	}
	// 比较是否是原始数据
	if !result.Hit {
		t.Fatalf("预期命中，实际没有命中")
	}
	if result.Task.ID != task.ID {
		t.Fatalf("ID不匹配")
	}
	if result.Task.Title != task.Title {
		t.Fatalf("标题不匹配")
	}
	if result.Task.Status != task.Status {
		t.Fatalf("状态不匹配")
	}
	if result.Task.Version != task.Version {
		t.Fatalf("版本不匹配")
	}
}

// 空值缓存
// TestTaskCache_<方法>_<场景或预期>
func TestTaskCache_SetNotFound_CachesMissingTask(t *testing.T) {
	rdb := newRedis(t)
	task_cache := NewTaskCache(rdb)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()
	// 向缓存中写入空值
	id := 12345
	if err := task_cache.SetNotFound(ctx, int64(id)); err != nil {
		t.Fatalf("写入空值失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := task_cache.DeleteTask(cleanupCtx, int64(id)); err != nil {
			t.Errorf("清理测试缓存失败：%v", err)
		}
	})
	// 检验是否写入空值
	result, err := task_cache.GetResult(ctx, int64(id))
	if err != nil {
		t.Fatalf("读取缓存失败：%v", err)
	}
	if !result.Hit {
		t.Fatalf("预期命中，实际没有命中")
	}
	if result.Exists {
		t.Fatalf("预期空值，实际不是空值")
	}
}

// 缓存删除 先确认有缓存，删除后Hit = false
func TestTaskCache_DeleteTask_CausesCacheMiss(t *testing.T) {
	rdb := newRedis(t)
	task_cache := NewTaskCache(rdb)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()
	task := model.Task{
		ID:      123456,
		Title:   "缓存删除测试",
		Status:  "pending",
		Version: 1,
	}
	err := task_cache.SetEntry(ctx, task.ID, task)
	if err != nil {
		t.Fatalf("任务写入缓存失败：%v", err)
	}
	// 再从缓存中读取东西,确认有缓存
	result, err := task_cache.GetResult(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取缓存失败：%v", err)
	}
	if !result.Hit {
		t.Fatalf("预期命中，实际没有命中")
	}
	// 删除缓存
	if err := task_cache.DeleteTask(ctx, task.ID); err != nil {
		t.Fatalf("删除缓存失败:%v", err)
	}
	// 再次读取缓存
	result_del, err := task_cache.GetResult(ctx, task.ID)
	if err != nil {
		t.Fatalf("读取缓存失败：%v", err)
	}
	if result_del.Hit {
		t.Fatalf("预期不命中，结果命中")
	}
}

// 验证写入 Redis 的 TTL 落在预期范围内。
func TestTaskCache_SetEntry_TTLWithinRange(t *testing.T) {
	rdb := newRedis(t)
	taskCache := NewTaskCache(rdb)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const (
		taskID int64 = 123457
		minTTL       = 10 * time.Second
		maxTTL       = 70 * time.Second
	)
	task := model.Task{ID: taskID, Title: "缓存 TTL 测试", Status: "pending", Version: 1}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := taskCache.DeleteTask(cleanupCtx, taskID); err != nil {
			t.Errorf("清理测试缓存失败：%v", err)
		}
	})

	start := time.Now()
	if err := taskCache.SetEntry(ctx, taskID, task); err != nil {
		t.Fatalf("写入缓存失败：%v", err)
	}
	ttl, err := rdb.PTTL(ctx, taskKey2(int(taskID))).Result()
	if err != nil {
		t.Fatalf("读取缓存 TTL 失败：%v", err)
	}
	if ttl <= 0 {
		t.Fatalf("任务 %d 的缓存应存在且有过期时间，实际 PTTL=%v", taskID, ttl)
	}

	// PTTL 返回剩余毫秒数，扣除写入到读取的耗时和毫秒精度误差。
	lowerBound := minTTL - time.Since(start) - time.Millisecond
	if ttl < lowerBound || ttl >= maxTTL {
		t.Errorf("剩余 TTL 应在 [%v, %v) 内，实际为 %v", lowerBound, maxTTL, ttl)
	}
}
