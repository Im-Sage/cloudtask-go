package service

import (
	"context"
	"errors"
	"strings"

	"example.com/cloudtask-go/day9/internal/model"
	"example.com/cloudtask-go/day9/internal/repository"
)

// 业务规则、事务用例

type TaskService struct {
	taskRepository *repository.TaskRepository
}

func NewTaskService(taskRepository *repository.TaskRepository) *TaskService {
	return &TaskService{taskRepository: taskRepository}
}

var ErrInvalidTitle = errors.New("invalid title")
var ErrInvalidDescription = errors.New("invalid description")

func (s *TaskService) CreateTask(
	ctx context.Context,
	title string,
	description string,
) (model.Task, error) {
	if strings.TrimSpace(title) == "" {
		return model.Task{}, ErrInvalidTitle
	}
	if strings.TrimSpace(description) == "" {
		return model.Task{}, ErrInvalidDescription
	}
	return s.taskRepository.CreateTask(ctx, title, description)
}

func (s *TaskService) GetTaskByID(
	ctx context.Context,
	id int64,
) (model.Task, error) {
	return s.taskRepository.GetTaskByID(ctx, id)
}

func (s *TaskService) GetAllTasks(ctx context.Context) ([]model.Task, error) {
	return s.taskRepository.GetAllTasks(ctx)
}

func (s *TaskService) DeleteTask(ctx context.Context, id int64) error {
	return s.taskRepository.DeleteTask(ctx, id)
}

func (s *TaskService) UpdateTask(
	ctx context.Context,
	id int64,
	title string,
	status string) (model.Task, error) {
	return s.taskRepository.UpdateTask(ctx, id, title, status)
}
