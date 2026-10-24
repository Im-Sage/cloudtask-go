package main

import "fmt"

type TaskManager struct {
	tasks  map[int]*Task
	nextID int
}

// 普通函数当作构造函数使用，返回一个指针类型的结构体
func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks:  make(map[int]*Task),
		nextID: 1,
	}
}

func (tm *TaskManager) Add(title string) (Task, error) {
	task := &Task{
		Id:   tm.nextID,
		Name: title,
		Done: false,
	}
	tm.tasks[task.Id] = task
	tm.nextID++
	return *task, nil
}

func (tm *TaskManager) Get(id int) (Task, error) {
	task, exits := tm.tasks[id]
	if !exits {
		return Task{}, fmt.Errorf("task with id %d not found", id)
	}
	return *task, nil
}

func (tm *TaskManager) Complete(id int) error {
	task, exits := tm.tasks[id]
	if !exits {
		return fmt.Errorf("task with id %d not found", id)
	}
	task.Done = true
	return nil
}

func (tm *TaskManager) Delete(id int) error {
	task, exits := tm.tasks[id]
	if !exits {
		return fmt.Errorf("task with id %d not found", id)
	}
	delete(tm.tasks, task.Id)
	return nil
}
