package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"example.com/cloudtask-go/day9/internal/repository"
	"example.com/cloudtask-go/day9/internal/requestbody"
	"example.com/cloudtask-go/day9/internal/service"
)

// 解析请求、调用业务、返回响应

type TaskHandler struct {
	taskService *service.TaskService
}

func NewTaskHandler(taskService *service.TaskService) *TaskHandler {
	return &TaskHandler{taskService: taskService}
}

func (h *TaskHandler) GetTaskByID(
	w http.ResponseWriter,
	r *http.Request,
) {
	idStr := r.PathValue("id")
	idInt, err := strconv.ParseInt(idStr, 10, 64)
	if idInt <= 0 {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	task, err := h.taskService.GetTaskByID(r.Context(), idInt)

	if errors.Is(err, repository.ErrTaskNotFound) {
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}

	if err != nil {
		log.Printf("查询任务失败：id=%d，错误=%v", idInt, err)
		http.Error(w, "错误", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(task); err != nil {
		log.Printf("编码任务响应失败: %v", err)
	}
}

func (h *TaskHandler) GetAllTasks(
	w http.ResponseWriter,
	r *http.Request,
) {
	tasks, err := h.taskService.GetAllTasks(r.Context())
	if err != nil {
		log.Printf("查询任务列表失败：%v", err)
		http.Error(w, "错误", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(tasks); err != nil {
		log.Printf("编码任务响应失败: %v", err)
	}
}

func (h *TaskHandler) CreateTask(
	w http.ResponseWriter,
	r *http.Request,
) {
	var req requestbody.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求 JSON 无效", http.StatusBadRequest)
		return
	}
	task, err := h.taskService.CreateTask(
		r.Context(),
		req.Title,
		req.Description,
	)
	if err != nil {
		log.Printf("添加任务失败：%v", err)
		http.Error(w, "错误", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(task); err != nil {
		log.Printf("编码新任务响应失败: %v", err)
	}
}


func (h *TaskHandler) DeleteTask(
	w http.ResponseWriter,
	r *http.Request,
) {
	idStr := r.PathValue("id")
	idInt, err := strconv.ParseInt(idStr, 10, 64)
	if idInt <= 0 {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	err = h.taskService.DeleteTask(r.Context(), idInt)
	if errors.Is(err, repository.ErrTaskNotFound){
		http.Error(w, "任务不存在", http.StatusNotFound)
		return 
	}
	if err != nil {
		log.Printf("删除任务失败：id=%d，错误=%v", idInt, err)
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return 
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *TaskHandler) UpdateTask(
	w http.ResponseWriter,
	r *http.Request,
){
	// 要更新的内容在r中
	var req requestbody.UpdateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求JSON无效",  http.StatusBadRequest)
		return
	}

	idStr := r.PathValue("id")
	idInt, err := strconv.ParseInt(idStr, 10, 64)
	if idInt <= 0 {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	task, err := h.taskService.UpdateTask(r.Context(), idInt ,req.Title, req.Status)
	if errors.Is(err, repository.ErrTaskNotFound){
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("更新任务失败：id=%d，错误=%v", idInt, err)
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return 
	}
	
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(task); err != nil {
		log.Printf("编码新任务响应失败: %v", err)
	}
}