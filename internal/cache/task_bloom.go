package cache

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const taskBloomKeyPrefix = "cloudtask:task:bloom:build:"

type TaskBloom struct {
	client  *redis.Client
	key     string
	trusted atomic.Bool // 初始值是false，默认不可信
}

// type BloomResult struct {
// 	Usable bool	// 本次结果是否可以用于拦截
// 	MayExist bool	// Usable=true shi
// }

func NewTaskBloom(client *redis.Client, key string) *TaskBloom {
	return &TaskBloom{
		client: client,
		key:    key, // key为整个布隆过滤器在redis中的名字
	}
}

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

// 向过滤器加入单个任务id
func (b *TaskBloom) Add(ctx context.Context, id int64) error {
	var itf any = id
	if err := b.client.BFAdd(ctx, b.key, itf).Err(); err != nil {
		b.trusted.Store(false)
		return fmt.Errorf("向过滤器中加入单个任务失败：%w", err)
	}
	return nil
}

// 布隆过滤器是否被弃用
func (b *TaskBloom) Trusted() bool {
	return b.trusted.Load()
}

// 判断该任务id是否存在于过滤器中
func (b *TaskBloom) MayContain(ctx context.Context, id int64) (bool, error) {
	// true, nil id可能存在，继续查询缓存或者数据库
	// false, nil 过滤器有效且确认未加入，可按策略拦截，即说不存在就一定不存在
	exits, err := b.client.BFExists(ctx, b.key, id).Result()

	return exits, err
}

// 外部查询ID，再交给组件构建
func (b *TaskBloom) Build(ctx context.Context, ids []int64) error {
	// 创建过滤器：目标误判率0.1%， 初始容量100000
	if err := b.client.BFReserve(ctx, b.key, 0.001, 100000).Err(); err != nil {
		return fmt.Errorf("创建布隆过滤器失败：%w", err)
	}

	// 每批加入1000个ID
	const batchSize = 1000

	for start := 0; start < len(ids); start = start + batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}

		items := make([]interface{}, 0, end-start)
		for _, id := range ids[start:end] {
			items = append(items, id)
		}
		if err := b.client.BFMAdd(ctx, b.key, items...).Err(); err != nil {
			return fmt.Errorf("批量加入任务ID失败，起始位置%d:%w", start, err)
		}
	}

	b.trusted.Store(true)
	return nil
}

func BuildTaskBloom(
	ctx context.Context,
	rdb *redis.Client,
	ids []int64,
	logger *slog.Logger,
) (*TaskBloom, error) {
	// 布隆过滤器的名字
	bloomKey := "cloudtask:task:bloom:build:" + uuid.NewString()
	taskBloom := NewTaskBloom(rdb, bloomKey)
	if err := taskBloom.Build(ctx, ids); err != nil {
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
				"error", cleanupErr)
		}
		return taskBloom, err
	}
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

	return taskBloom, nil
}
