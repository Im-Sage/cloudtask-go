package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"example.com/cloudtask-go/day9/internal/database"
	"example.com/cloudtask-go/day9/internal/handler"
	"example.com/cloudtask-go/day9/internal/repository"
	"example.com/cloudtask-go/day9/internal/service"
)

// 初始化连接池、配置路由、启动HTTP服务

func main() {
	dsn := "postgres://app:dev_password_only_local@localhost:5432/cloudtask?sslmode=disable"
	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second)
	defer cancel()
	pool, err := database.NewDBPool(ctx, dsn) // 先把连接池初始化好
	if err != nil {
		log.Fatalf("Failed to create connection pool: %v", err)
	}

	defer pool.Close() // 连接池成功创建，关闭应该由调用方负责

	taskRepository := repository.NewTaskRepository(pool)

	taskService := service.NewTaskService(taskRepository)

	

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
