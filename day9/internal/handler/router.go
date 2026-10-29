package handler

import (
	"example.com/cloudtask-go/day9/internal/service"
	"github.com/gin-gonic/gin"
)

// 注册Http 路由

func NewRouter(taskService *service.TaskService) *gin.Engine {
	router := gin.Default()
	taskHandler := NewTaskHandler(taskService)

	router.POST("/tasks",taskHandler.CreateTask)

	router.GET("/tasks/:id",taskHandler.GetTaskByID)

	// router.GET("/tasks?limit=3&offset=0",taskHandler.GetAllTasks)

	router.GET("/tasks",taskHandler.GetAllTasks)

	router.DELETE("/tasks/:id", taskHandler.DeleteTask)

	router.PUT("/tasks/:id",taskHandler.UpdateTask)


	// router.GET("/tasks?limit=3&offset=0", taskHandler)

	return router
}