package handler

import (
	// "encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"

	"example.com/cloudtask-go/day9/internal/repository"
	"example.com/cloudtask-go/day9/internal/requestbody"
	"example.com/cloudtask-go/day9/internal/service"
	"github.com/gin-gonic/gin"
	// "golang.org/x/tools/go/analysis/passes/nilfunc"
)

// 解析请求、调用业务、返回响应

type TaskHandler struct {
	taskService *service.TaskService
}

func NewTaskHandler(taskService *service.TaskService) *TaskHandler {
	return &TaskHandler{taskService: taskService}
}

func (h *TaskHandler) GetTaskByID(
	c *gin.Context,
) {
	idStr := c.Param("id")
	idInt, err := strconv.ParseInt(idStr, 10, 64)
	if idInt <= 0 || err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_ID", "任务 ID 必须是正整数")
		// c.JSON(http.StatusBadRequest, gin.H{"error":"Invalid ID"})
		return
	}

	task, err := h.taskService.GetTaskByID(c.Request.Context(), idInt)

	if errors.Is(err, repository.ErrTaskNotFound) {
		writeError(c, http.StatusNotFound, "TASK_NOT_FOUND", "任务不存在")
		// c.JSON(http.StatusNotFound, gin.H{"error":"任务不存在"})
		return
	}

	if err != nil {
		log.Printf("查询任务失败：id=%d，错误=%v", idInt, err)
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
		// c.JSON(http.StatusInternalServerError, gin.H{"error":"错误"})
		return
	}

	c.JSON(http.StatusOK, task)

	// w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// w.WriteHeader(http.StatusOK)

	// if err := json.NewEncoder(w).Encode(task); err != nil {
	// 	log.Printf("编码任务响应失败: %v", err)
	// }
}

func (h *TaskHandler) GetAllTasks(
	c *gin.Context,
) {
	// offset = (页码 - 1) * limit
	limitStr := c.DefaultQuery("limit", "10")
	offsetStr := c.DefaultQuery("offset", "0")
	status := c.DefaultQuery("status", "pending")

	statuses := []string{"pending", "in_progress", "completed"}

	limit, err := strconv.Atoi(limitStr)
	if limit <= 0 || limit > 100 || err != nil {
		log.Printf("limit 不符合规定：%v", err)
		writeError(c, http.StatusBadRequest, "INVALID_LIMIT", "limit必须是正整数")
		return
	}

	offset, err := strconv.Atoi(offsetStr)
	if offset < 0 || err != nil {
		log.Printf("offset 不符合规定：%v", err)
		writeError(c, http.StatusBadRequest, "INVALID_OFFSET", "offset必须是正整数")
		return
	}

	if !slices.Contains(statuses, status) {
		writeError(c, http.StatusBadRequest, "INVALID_STATUS", "status必须为('pending','in_progress','comleted')")
		return
	}

	tasks, err := h.taskService.GetAllTasks(c.Request.Context(), limit, offset, status)
	if err != nil {
		log.Printf("查询任务列表失败：%v", err)
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
		// http.Error(w, "错误", http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, tasks)
	// w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// w.WriteHeader(http.StatusOK)

	// if err := json.NewEncoder(w).Encode(tasks); err != nil {
	// 	log.Printf("编码任务响应失败: %v", err)
	// }
}

func (h *TaskHandler) CreateTask(
	c *gin.Context,
) {
	var req requestbody.CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "创建内容无效")
		return
	}

	// if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
	// 	http.Error(w, "请求 JSON 无效", http.StatusBadRequest)
	// 	return
	// }
	task, err := h.taskService.CreateTask(
		c.Request.Context(),
		req.Title,
		req.Description,
	)
	if errors.Is(err, service.ErrInvalidTitle) {
		writeError(c, http.StatusBadRequest, "INVALID_TITLE", "无效的标题")
		return
	}
	if errors.Is(err, service.ErrInvalidDescription) {
		writeError(c, http.StatusBadRequest, "INVALID_DESCRIPTION", "无效的描述")
		return
	}
	if err != nil {
		log.Printf("添加任务失败：%v", err)
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
		// http.Error(w, "错误", http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusCreated, task)

	// w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// w.WriteHeader(http.StatusCreated)

	// if err := json.NewEncoder(w).Encode(task); err != nil {
	// 	log.Printf("编码新任务响应失败: %v", err)
	// }
}

func (h *TaskHandler) DeleteTask(
	c *gin.Context,
) {
	idStr := c.Param("id")
	idInt, err := strconv.ParseInt(idStr, 10, 64)
	if idInt <= 0 {
		writeError(c, http.StatusBadRequest, "INVALID_ID", "任务 ID 必须是正整数")
		// http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_ID", "任务 ID 必须是正整数")
		// http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	err = h.taskService.DeleteTask(c.Request.Context(), idInt)
	if errors.Is(err, repository.ErrTaskNotFound) {
		writeError(c, http.StatusNotFound, "TASK_NOT_FOUND", "任务不存在")
		// http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("删除任务失败：id=%d，错误=%v", idInt, err)
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
		// http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
	// w.WriteHeader(http.StatusNoContent)
}

func (h *TaskHandler) UpdateTask(
	c *gin.Context,
) {
	// 要更新的内容在c中，把要更新的内容放在req中
	var req requestbody.UpdateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_REQUEST", "更新内容无效")
		return
	}

	// if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
	// 	http.Error(w, "请求JSON无效",  http.StatusBadRequest)
	// 	return
	// }

	idStr := c.Param("id")
	idInt, err := strconv.ParseInt(idStr, 10, 64)
	if idInt <= 0 {
		writeError(c, http.StatusBadRequest, "INVALID_ID", "任务 ID 必须是正整数")
		// http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_ID", "任务 ID 必须是正整数")
		// http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	task, err := h.taskService.UpdateTask(c.Request.Context(), idInt, req.Title, req.Status)
	if errors.Is(err, repository.ErrTaskNotFound) {
		writeError(c, http.StatusNotFound, "TASK_NOT_FOUND", "任务不存在")
		// http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	if errors.Is(err, service.ErrInvalidStatus) {
		writeError(c, http.StatusBadRequest, "INVALID_STATUS", "无效的状态值")
		// http.Error(w, "无效的状态值", http.StatusBadRequest)
		return
	}
	if errors.Is(err, service.ErrInvalidTitle) {
		writeError(c, http.StatusBadRequest, "INVALID_TITLE", "无效的标题")
		// http.Error(w, "无效的标题", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("更新任务失败：id=%d，错误=%v", idInt, err)
		writeError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
		// http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return
	}

	c.JSON(http.StatusOK, task)
	// w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// w.WriteHeader(http.StatusOK)

	// if err := json.NewEncoder(w).Encode(task); err != nil {
	// 	log.Printf("编码新任务响应失败: %v", err)
	// }
}
