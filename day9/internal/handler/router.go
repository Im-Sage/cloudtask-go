package handler

import (
	"net/http"

	"example.com/cloudtask-go/day9/internal/service"
)

// 注册Http 路由

func NewRouter(taskService *service.TaskService) http.Handler {
	taskHandler := NewTaskHandler(taskService)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /tasks/{id}", taskHandler.GetTaskByID)

	mux.HandleFunc("GET /tasks", taskHandler.GetAllTasks)

	mux.HandleFunc("POST /tasks", taskHandler.CreateTask)

	mux.HandleFunc("DELETE /tasks/{id}", taskHandler.DeleteTask)

	mux.HandleFunc("PUT /tasks/{id}", taskHandler.UpdateTask)

	return mux
}