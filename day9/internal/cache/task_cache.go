package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/model"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var ErrCacheMiss = redis.Nil

type TaskCache struct {
	client *redis.Client
}

func NewTaskCache(client *redis.Client) *TaskCache {
	return &TaskCache{
		client: client,
	}
}

func taskKey(id int) string {
	return fmt.Sprintf("cloudtask:task:%d:v1", id)
}

// 从cache中读取task
func (c *TaskCache) GetTask(ctx context.Context, id int64) (model.Task, bool, error) {
	key := taskKey(int(id))
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, ErrCacheMiss) {
		return model.Task{}, false, nil
	}
	if err != nil {
		log.Printf("发生错误：%v", err)
		return model.Task{}, false, err
	}
	var task model.Task
	if err := json.Unmarshal(data, &task); err != nil {
		log.Printf("反序列化失败：%v", err)
		return model.Task{}, false, err
	}
	return task, true, nil
}

// 向cache中写入task
func (c *TaskCache) SetTask(ctx context.Context, id int64, task model.Task) error {
	// 首先将task序列化为json
	data, err := json.Marshal(task)
	if err != nil {
		fmt.Println("序列化json失败：%v", err)
		return err
	}
	key := taskKey(int(id))
	err = c.client.Set(ctx, key, data, 5*time.Minute).Err()
	if err != nil {
		return err
	}
	return nil
}

// 从cache中删除数据
func (c *TaskCache) DeleteTask(ctx context.Context, id int64) error {
	// 先判断目标记录是否在cache中
	key := taskKey(int(id))
	// 删除cache中数据
	if err := c.client.Del(ctx,key).Err(); err != nil{
		log.Printf("删除缓存失败：%v",err)
		return err
	}
	return nil
}

func fun1() {
	// 当前从 day9/internal/cache 目录运行，向上两级找到 .env
	if err := godotenv.Load("../../.env"); err != nil {
		fmt.Println("加载 .env 失败:", err)
		return
	}

	// 初始化Redis Client
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)

	defer cancel()

	client := redis.NewClient(&redis.Options{
		Addr:     "127.0.0.1:6379",
		Username: "default",
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       0,
	})
	defer client.Close()

	id := 12

	task := model.Task{
		ID:          int64(id),
		Title:       "学习 Redis",
		Description: "学习 Redis 的基本使用",
		Status:      "pending",
		Version:     1,
	}

	key := taskKey(id)
	fmt.Print(key)

	data, err := json.Marshal(task)
	if err != nil {
		fmt.Println("序列化任务失败:", err)
		return
	}
	fmt.Println("序列化后的任务数据:", string(data))

	start := time.Now()

	// 把JSON存在Redis中
	err = client.Set(ctx, key, data, 5*time.Minute).Err()

	fmt.Printf(
		"Set 耗时：%v，操作错误：%v，ctx 状态：%v\n",
		time.Since(start),
		err,
		ctx.Err(),
	)

	if err != nil {
		return
	}

	// 从Redis中读取并反序列化为Task
	data, err = client.Get(ctx, key).Bytes()
	if err != nil {
		fmt.Errorf("发生错误：%v", err)
	}

	var task1 model.Task

	if err := json.Unmarshal(data, &task1); err != nil {
		fmt.Errorf("发生错误：%v", err)
	}
	fmt.Printf("key为：%s,值为：%v", key, task1)

	// var decoded model.Task

	// err = json.Unmarshal(data, &decoded)
	// if err != nil {
	// 	fmt.Println("反序列化任务失败:", err)
	// 	return
	// }
	// fmt.Println("反序列化后的任务数据:", decoded)
}
