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
)

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
		5 * time.Second)	
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
		5 * time.Second)	
	defer redisCancel()

	err = rdb.Ping(redisCtx).Err()
	if err != nil {
		fmt.Println("Redis 连接失败", err)
		return 
	}

	taskRepository := repository.NewTaskRepository(pool)
	
	taskCache := cache.NewTaskCache(rdb)

	taskService := service.NewTaskService(taskRepository, taskCache)

	router := handler.NewRouter(taskService)

	// 启动HTTP服务
	addr := ":8080"
	fmt.Printf("Starting server on %s\n", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	// task, err := taskRepository.CreateTask(ctx,"outlook","outlook is a email client")
	// if err != nil {
	// 	log.Fatalf("Failed to create task: %v", err)
	// }
	// log.Printf("Created task: %v", task)

	// 获取刚刚获取的任务
	// task, err := taskRepository.GetTaskByID(ctx, task.ID)
	// if err != nil {
	// 	log.Fatalf("Failed to get task: %v", err)
	// }
	// log.Printf("Retrieved task: %v", task)

	// 获取所有的任务
	// tasks, err := taskRepository.GetAllTasks(ctx)
	// if err != nil {
	// 	log.Fatalf("Failed to create task: %v", err)
	// }
	// for _, task := range tasks {
	// 	log.Printf("task%d为:%v", task.ID, task)
	// }

	// 更新id为8的任务
	// task, err := taskRepository.UpdateTask(ctx, 11, "现阶段目标：找到一个真正快乐的工作", "in_progress")
	// if err != nil {
	// 	log.Fatalf("Failed to update task:%v", err)
	// }
	// log.Printf("Retrieved task: %v", task)

	// 删除id为11的任务
	// err = taskRepository.DeleteTask(ctx,5)
	// if err!=nil {
	// 	log.Fatalf("Failed to delete task: %v", err)
	// }
	// fmt.Printf("delete success")

}
