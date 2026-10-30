package model

import (
	"time"
	"encoding/json"
)

// task数据结构
type Task struct {
	ID          int64     `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type TaskEvent struct {
	ID        int64           `json:"id"`
	TaskID    int64           `json:"task_id"`
	EventType string          `json:"event_type"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}
