package service

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/Im-Sage/cloudtask-go/day9/internal/cache"
	"github.com/Im-Sage/cloudtask-go/day9/internal/model"
	"github.com/Im-Sage/cloudtask-go/day9/internal/repository"
)

// 业务规则、事务用例

type TaskService struct {
	taskRepository *repository.TaskRepository
	taskCache      *cache.TaskCache
}

func NewTaskService(
	taskRepository *repository.TaskRepository,
	taskCache *cache.TaskCache,
) *TaskService {
	return &TaskService{
		taskRepository: taskRepository,
		taskCache:      taskCache,
	}
}

var ErrInvalidTitle = errors.New("invalid title")
var ErrInvalidDescription = errors.New("invalid description")
var ErrTaskNotFound = errors.New("TaskNotFound")
var ErrInvalidStatus = errors.New("invalid status (should be 'pending' or 'in_progress' or 'completed')")

func (s *TaskService) CreateTask(
	ctx context.Context,
	title string,
	description string,
) (model.Task, error) {
	if strings.TrimSpace(title) == "" {
		return model.Task{}, ErrInvalidTitle
	}
	return s.taskRepository.CreateTask(ctx, title, description)
}

func (s *TaskService) GetTaskByID(
	ctx context.Context,
	id int64,
) (model.Task, error) {
	// 如果启用了缓存，调用TaskCache.Get(ctx, id)，首先得明白什么叫启用缓存
	if s.taskCache != nil {
		log.Printf("启用了缓存")
		task, hit, err := s.taskCache.GetTask(ctx, id)
		if err == nil && hit {
			log.Printf("任务缓存命中：id=%d", id)
			// 命中则直接返回
			return task, nil
		}
		if !hit {
			log.Printf("读取缓存失败，准备回源：id=%d", id)
		}
		if err != nil {
			return model.Task{}, err
		}
	}
	// 未命中则准备开始回源
	task, err := s.taskRepository.GetTaskByID(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	// 数据库查询成功后回填
	if s.taskCache != nil {
		// 将task写进Redis, Redis写入失败不应该影响数据库查询结果
		if err := s.taskCache.SetTask(ctx, id, task); err != nil {
			log.Printf("写入redis失败：%v", err)
		}
	}
	return task, nil
}

func (s *TaskService) GetAllTasks(
	ctx context.Context,
	limit int,
	offset int,
	status string) ([]model.Task, error) {
	return s.taskRepository.GetAllTasks(ctx, limit, offset, status)
}

func (s *TaskService) DeleteTask(ctx context.Context, id int64) error {
	return s.taskRepository.DeleteTask(ctx, id)
}

func (s *TaskService) UpdateTask(
	ctx context.Context,
	id int64,
	title string,
	status string) (model.Task, error) {
	if status != "pending" && status != "in_progress" && status != "completed" {
		return model.Task{}, ErrInvalidStatus
	}
	if strings.TrimSpace(title) == "" {
		return model.Task{}, ErrInvalidTitle
	}
	return s.taskRepository.UpdateTask(ctx, id, title, status)
}

func (s *TaskService) ChangeStatus(ctx context.Context, id int64, newStatus string) error {
	if newStatus != "pending" && newStatus != "in_progress" && newStatus != "completed" {
		return ErrInvalidStatus
	}
	return s.taskRepository.ChangeStatus(ctx, id, newStatus)
}

func (s *TaskService) ChangeStatusOptimistis(ctx context.Context, id int64, newStatus string, expectedVersion int64) (model.Task, error) {
	if newStatus != "pending" && newStatus != "in_progress" && newStatus != "completed" {
		return model.Task{}, ErrInvalidStatus
	}
	return s.taskRepository.ChangeStatusOptimistis(ctx, id, newStatus, expectedVersion)
}
