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
		if err != nil{
			// redis连接对象存在，但本次缓存操作失败
			log.Printf("连接对象存在，但本次操作失败,原因：%v, 准备回源",err)
		}else if hit {
			// 命中直接返回
			return task,nil
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
	// 先写数据库，成功后删除缓存，让下一次读取重新从数据库加载。
	task, err := s.taskRepository.UpdateTask(ctx, id, title, status)
	// 数据库更新失败直接返回错误，不删除缓存
	if err != nil {
		return model.Task{}, err
	}
	// 数据库更新成功则删除当前缓存，首先要判断是否开启了缓存
	if s.taskCache != nil {
		if err := s.taskCache.DeleteTask(ctx, id); err != nil {
			// 若数据库删除失败不影响数据库更新操作
			log.Printf("删除缓存失败：%v", err)
		}
	}
	return task, nil
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
