package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 使用pgxpool执行SQL语句，返回结果集

type TaskRepository struct {
	// 连接池
	pool *pgxpool.Pool
}

func NewTaskRepository(pool *pgxpool.Pool) *TaskRepository {
	return &TaskRepository{pool: pool}
}

var ErrTaskNotFound = errors.New("task not found")
var ErrVersionConflict = errors.New("task version conflct")

// 创建任务
func (r *TaskRepository) CreateTask(
	ctx context.Context,
	title string,
	description string,
) (model.Task, error) {
	// 执行SQL语句
	var task model.Task

	err := r.pool.QueryRow(
		ctx,
		`
		INSERT INTO tasks (title, description)
        VALUES ($1, $2)
        RETURNING id, title, description, status, created_at, updated_at, version
		`,
		title,
		description,
	).Scan(
		&task.ID,
		&task.Title,
		&task.Description,
		&task.Status,
		&task.CreatedAt,
		&task.UpdatedAt,
		&task.Version,
	)
	if err != nil {
		return model.Task{}, err
	}
	return task, nil
}

// 获取一条记录
func (r *TaskRepository) GetTaskByID(
	ctx context.Context,
	id int64,
) (model.Task, error) {
	// 执行SQL语句
	var task model.Task // 定义一个task变量来存储查询结果

	log.Printf("[DB回源] GetTaskByID id=%d", id)

	err := r.pool.QueryRow(
		ctx,
		`select 
		id, title, description, status, created_at, updated_at, version
		from tasks where id = $1`,
		id,
	).Scan(
		&task.ID,
		&task.Title,
		&task.Description,
		&task.Status,
		&task.CreatedAt,
		&task.UpdatedAt,
		&task.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, fmt.Errorf("任务 %d: %w", id, ErrTaskNotFound)
	}
	if err != nil {
		return model.Task{}, err
	}
	return task, nil
}

// 查询多条记录
func (r *TaskRepository) GetAllTasks(
	ctx context.Context,
	limit int,
	offset int,
	status string,
) ([]model.Task, error) {
	// 返回的是一个装有任务的数组
	tasks := []model.Task{}

	rows, err := r.pool.Query(
		ctx,
		`select id, title,description,status,created_at,updated_at,version
		from tasks where status = $1
		order by id desc
		limit $2 OFFSET $3;
		`,
		status,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() // 关闭结果集，释放连接
	for rows.Next() {
		var task model.Task
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&task.Status,
			&task.CreatedAt,
			&task.UpdatedAt,
			&task.Version,
		); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

// 修改任务的标题与状态
func (r *TaskRepository) UpdateTask(
	ctx context.Context,
	id int64,
	title string,
	status string,
) (model.Task, error) {
	// 先获取旧状态，如果新状态不同旧状态，就更新title、status、updated_at,同时向task_events中写入事件。
	// 若新旧状态相同时，则只更新title、updated_at，同时写入事件。

	// 引进事务
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	// 回滚兜底
	defer func() {
		rollbackCtx, cancle := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancle()
		_ = tx.Rollback(rollbackCtx)
	}()

	// 先不获取之前的状态

	// 设置一个Task放改好的任务
	var task model.Task

	err = tx.QueryRow(ctx,
		`
		UPDATE tasks
	SET title = $1,
		status = $2,
		version = version + 1,
		updated_at = CURRENT_TIMESTAMP
	WHERE id = $3
	RETURNING id, title, description, status,
			created_at, updated_at, version;
	`,
		title,
		status,
		id,
	).Scan(
		&task.ID,
		&task.Title,
		&task.Description,
		&task.Status,
		&task.CreatedAt,
		&task.UpdatedAt,
		&task.Version,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, fmt.Errorf("任务 %d: %w", id, ErrTaskNotFound)
	}

	if err != nil {
		return model.Task{}, err
	}

	// 同时向task_events表内插入数据，事件类型为task_updated
	event_type := "task_updated"
	tag, err := tx.Exec(ctx,
		`Insert into task_events (task_id, event_type) values($1, $2)`,
		id, event_type)
	if err != nil {
		return model.Task{}, err
	}
	if tag.RowsAffected() == 0 {
		return model.Task{}, fmt.Errorf("插入事件任务%d失败", id)
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Task{}, fmt.Errorf("提交任务更新事务失败%w", err)
	}
	return task, nil
}

// 删除任务
func (r *TaskRepository) DeleteTask(
	ctx context.Context,
	id int64,
) error {
	tag, err := r.pool.Exec(ctx,
		`
	delete from tasks where id = $1`,
		id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("任务 %d: %w", id, ErrTaskNotFound)
	}
	return nil
}

// 数据量不大的情况下，获取所有的id，为布隆过滤器做准备
func (r *TaskRepository) GetIds(ctx context.Context) ([]int64, error){
	var ids []int64

	rows, err := r.pool.Query(
		ctx,
		`select id from tasks order by id;`,
	)
	if err != nil {
		return ids, err
	}
	defer rows.Close() // 关闭结果集，释放连接

	for rows.Next() {
		var id int64
		if err := rows.Scan(
			&id,
		); err != nil{
			return ids, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}


// 读取原状态 -> 更新任务状态 -> 插入时间 -> 提交事务
func (r *TaskRepository) ChangeStatus(
	ctx context.Context,
	id int64,
	newStatus string,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	// defer tx.Rollback(ctx) // 兜底回滚
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	// 1、读取旧状态
	var oldStatus string

	err = tx.QueryRow(ctx,
		`select status from tasks where id = $1 for update`,
		id).Scan(&oldStatus)
	// 检查任务是否存在
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("任务 %d: %w", id, ErrTaskNotFound)
	}
	if oldStatus == newStatus {
		log.Printf("任务状态未变化：id=%d,status=%s", id, oldStatus)
		return tx.Commit(ctx)
	}
	if err != nil {
		return fmt.Errorf("读取任务旧状态失败: %w", err)
	}
	// 使用tx.Exec
	tag, err := tx.Exec(
		ctx,
		`update tasks 
		set status = $1, 
		version = version + 1,
		updated_at = CURRENT_TIMESTAMP 
		where id = $2`,
		newStatus, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaskNotFound
	}
	// 使用tx.Exec 插入 task_event;
	// var task_event model.TaskEvent
	// task_event.TaskID = id
	event_type := "status_changed"
	details, err := json.Marshal(map[string]string{
		"old_status": oldStatus,
		"new_status": newStatus,
	})
	if err != nil {
		return err
	}

	tag, err = tx.Exec(
		ctx,
		`insert into task_events (task_id, event_type, details) values ($1,$2,$3)`,
		id, event_type, details,
	)
	if err != nil {
		return fmt.Errorf("插入事件失败：%w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("插入事件任务%d失败", id)
	}
	return tx.Commit(ctx)
}

func (r *TaskRepository) ChangeStatusOptimistis(
	ctx context.Context,
	id int64,
	newStatus string,
	expectedVersion int64,
) (model.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return model.Task{}, err
	}
	// 回滚兜底
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(
			context.Background(),
			3*time.Second,
		)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	// 读取当前任务和版本号
	var old_status string
	var old_version int64
	var task model.Task

	err = tx.QueryRow(
		ctx,
		`SELECT status, version from tasks where id = $1`,
		id,
	).Scan(
		&old_status,
		&old_version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Task{}, fmt.Errorf("任务 %d: %w", id, ErrTaskNotFound)
	}
	if err != nil {
		return model.Task{}, err
	}

	// 判断期待的版本号是否和oldversion一样
	if old_version != expectedVersion {
		return model.Task{}, fmt.Errorf("任务%d更新状态失败：%w", id, ErrVersionConflict)
	} else {
		// 一致则更新表中数据
		err := tx.QueryRow(
			ctx,
			`UPDATE tasks
			SET status = $1,
				version = version + 1,
				updated_at = CURRENT_TIMESTAMP
			where id = $2 AND version = $3
			RETURNING id, title, description, status, created_at, updated_at, version
				`,
			newStatus,
			id,
			expectedVersion,
		).Scan(
			&task.ID,
			&task.Title,
			&task.Description,
			&task.Status,
			&task.CreatedAt,
			&task.UpdatedAt,
			&task.Version,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return model.Task{}, fmt.Errorf("任务 %d: %w", id, ErrVersionConflict)
		}
		if err != nil {
			return model.Task{}, err
		}
		// 同时向事件表中插入数据
		event_type := "status_changed"
		details, err := json.Marshal(map[string]string{
			"old_status": old_status,
			"new_status": newStatus,
		})
		if err != nil {
			return model.Task{}, err
		}
		tag, err := tx.Exec(
			ctx,
			`insert into task_events (task_id, event_type, details) values ($1,$2,$3)`,
			id, event_type, details,
		)
		if err != nil {
			return model.Task{}, fmt.Errorf("插入事件失败：%w", err)
		}
		if tag.RowsAffected() != 1 {
			return model.Task{}, fmt.Errorf("插入事件任务%d失败", id)
		}

		if err := tx.Commit(ctx); err != nil {
			return model.Task{}, err
		}

		return task, nil
	}
}


