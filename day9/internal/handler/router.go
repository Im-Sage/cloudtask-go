package handler

import (
	"github.com/Im-Sage/cloudtask-go/day9/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 注册Http 路由

func NewRouter(taskService *service.TaskService, pool *pgxpool.Pool) *gin.Engine {
	router := gin.Default()
	taskHandler := NewTaskHandler(taskService)

	healthHandler := NewHealthHandler(pool)

	router.GET("/healthz", taskHandler.healthzSys)

	router.GET("/readySys", healthHandler.readySys)


	router.POST("/tasks", taskHandler.CreateTask)

	router.GET("/tasks/:id", taskHandler.GetTaskByID)

	// router.GET("/tasks?limit=3&offset=0",taskHandler.GetAllTasks)

	router.GET("/tasks", taskHandler.GetAllTasks)

	router.DELETE("/tasks/:id", taskHandler.DeleteTask)

	router.PUT("/tasks/:id", taskHandler.UpdateTask)

	router.PATCH("/tasks/:id", taskHandler.ChangeStatus)

	router.PATCH("tasks/opm/:id", taskHandler.ChangeStatusOptimistis)

	// router.GET("/tasks?limit=3&offset=0", taskHandler)

	return router
}
