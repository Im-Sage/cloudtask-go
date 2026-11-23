//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/config"
	"github.com/Im-Sage/cloudtask-go/day9/internal/database"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 数据库集成测试

// 辅助函数 数据库准备工作
func newRepository(t *testing.T) *pgxpool.Pool {
	t.Helper()
	// 读取配置，这里有误，不应该使用生产环境的配置，应新建一个测试环境的配置
	// cfg, err := config.Load()
	dsn := os.Getenv("TEST_DATABASE_URL")
	var pgCfg = config.PostgresConfig{
		DSN:         dsn,
		MaxConns:    4,
		MinConns:    0,
		PingTimeout: 5 * time.Second,
	}
	if pgCfg.DSN == "" {
		t.Fatalf("配置读取错误:未设置DSN")
	}

	const testDBName = "cloudtask_test"

	parsed, err := pgxpool.ParseConfig(pgCfg.DSN)

	if err != nil {
		t.Fatal("无法解析 TEST_DATABASE_URL，请检查连接串格式")
	}
	if dbName := parsed.ConnConfig.Database; dbName != testDBName {
		t.Fatalf("测试要求使用数据库 %q，当前配置指向 %q", testDBName, dbName)
	}
	// 设置超时和取消时间
	dbCtx, dbCancel := context.WithTimeout(
		context.Background(),
		6*time.Second,
	)
	defer dbCancel()
	pool, err := database.NewDBPool(dbCtx, pgCfg)
	if err != nil {
		t.Fatalf("数据库初始化错误%v", err)
	}
	return pool
}

func TestCreateAndGetTask(t *testing.T) {
	pool := newRepository(t)
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancel()
	repository := NewTaskRepository(pool)
	// 创建一个任务
	task, err := repository.CreateTask(ctx, "integration-test", "测试数据")
	if err != nil {
		t.Fatalf("任务创建失败：%v", err)
	}

	// 通过刚刚创建的taskid查询task
	taskGet, err := repository.GetTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM tasks WHERE id = $1", taskGet.ID); err != nil {
			t.Errorf("删除测试任务 %d 失败：%v", taskGet.ID, err)
		}
	})
	// 判断版本是否正确，插入时默认版本是1
	// 继续断言返回的ID、标题等于传入值，状态是否为pending
	if taskGet.Version != 1 {
		t.Fatalf("任务版本错误")
	}
	if taskGet.Title != task.Title {
		t.Fatalf("任务标题错误")
	}
	if taskGet.Status != "pending" {
		t.Fatalf("任务状态错误")
	}
	if taskGet.ID != task.ID {
		t.Fatalf("任务ID错误")
	}
}

func TestChangeStatusWritesEvent(t *testing.T) {
	pool := newRepository(t)
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancel()
	repository := NewTaskRepository(pool)
	// 先插入一条任务
	task, err := repository.CreateTask(ctx, "状态更新测试", "测试数据")
	if err != nil {
		t.Fatalf("任务创建失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM tasks WHERE id = $1", task.ID); err != nil {
			t.Errorf("删除测试任务 %d 失败：%v", task.ID, err)
		}
	})

	// 调用ChangeStatus
	if err := repository.ChangeStatus(ctx, task.ID, "in_progress"); err != nil {
		t.Fatalf("状态改变失败:%v", err)
	}
	// task_events表中只记录状态改变，不记录任务整个生命周期
	// 观察状态是否符合预期
	// 再次取得任务
	taskGet, err := repository.GetTaskByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("更改状态后查询失败:%v", err)
	}
	if taskGet.Status != "in_progress" {
		t.Fatalf("状态改变失败")
	}
	if taskGet.Version != task.Version+1 {
		t.Fatalf("任务版本没有更新")
	}
	var count int64

	err = pool.QueryRow(ctx, `
	SELECT COUNT(*)
		FROM task_events 
		WHERE task_id = $1 AND event_type = $2`,
		taskGet.ID, "status_changed").Scan(&count)
	if err != nil {
		t.Fatalf("查询状态变更事件失败：%v", err)
	}
	if count != 1 {
		t.Fatalf("任务 %d 应有 1 条 status_changed 事件，实际有 %d 条", task.ID, count)
	}
}

// 验证事件写入失败时，任务更新也会回滚，这个测试一定要保证测试会回滚
func TestChangeStatusFailRollback(t *testing.T) {
	pool := newRepository(t)
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewTaskRepository(pool)

	before, err := repo.CreateTask(ctx, "状态回滚测试", "测试数据")
	if err != nil {
		t.Fatalf("创建测试任务失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM tasks WHERE id = $1", before.ID); err != nil {
			t.Errorf("删除测试任务 %d 失败：%v", before.ID, err)
		}
	})

	// 只在独立测试库中拦截这条任务的状态变更事件。
	constraintName := fmt.Sprintf("test_block_status_changed_%d", before.ID)
	addConstraint := fmt.Sprintf(`
		ALTER TABLE task_events
		ADD CONSTRAINT %s
		CHECK (task_id <> %d OR event_type <> 'status_changed')
	`, constraintName, before.ID)
	if _, err := pool.Exec(ctx, addConstraint); err != nil {
		t.Fatalf("添加测试约束失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "ALTER TABLE task_events DROP CONSTRAINT "+constraintName); err != nil {
			t.Errorf("删除测试约束失败：%v", err)
		}
	})

	err = repo.ChangeStatus(ctx, before.ID, "in_progress")
	if err == nil {
		t.Fatal("预期事件写入失败，实际状态更新成功")
	}
	// 事件写入失败，符合预期
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("预期 PostgreSQL 返回约束错误，实际为：%v", err)
	}
	if pgErr.Code != "23514" || pgErr.ConstraintName != constraintName {
		t.Fatalf("预期违反约束 %q，实际 SQLSTATE=%s，约束=%q",
			constraintName, pgErr.Code, pgErr.ConstraintName)
	}

	after, err := repo.GetTaskByID(ctx, before.ID)
	if err != nil {
		t.Fatalf("读取回滚后的任务失败：%v", err)
	}
	if after.Status != before.Status {
		t.Errorf("回滚后状态应为 %q，实际为 %q", before.Status, after.Status)
	}
	if after.Version != before.Version {
		t.Errorf("回滚后版本应为 %d，实际为 %d", before.Version, after.Version)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("回滚后更新时间应保持为 %v，实际为 %v",
			before.UpdatedAt, after.UpdatedAt)
	}

	var count int64
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM task_events WHERE task_id = $1",
		before.ID).Scan(&count); err != nil {
		t.Fatalf("查询回滚后的事件失败：%v", err)
	}
	if count != 0 {
		t.Errorf("回滚后任务 %d 应有 0 条事件，实际有 %d 条", before.ID, count)
	}
}

// 验证乐观锁冲突
func TestChangeStatusOptimisticConflict(t *testing.T) {
	pool := newRepository(t)
	t.Cleanup(pool.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := NewTaskRepository(pool)

	// 创建新任务
	before, err := repo.CreateTask(ctx, "状态乐观锁冲突测试", "测试数据")
	if err != nil {
		t.Fatalf("创建测试任务失败：%v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM tasks WHERE id = $1", before.ID); err != nil {
			t.Errorf("删除测试任务 %d 失败：%v", before.ID, err)
		}
	})
	// 此时before中的version字段为1
	// 先调用ChangeStatusOptimistis成功更新一次，version+1 = 2
	task1, err := repo.ChangeStatusOptimistis(ctx, before.ID, "in_progress", before.Version)
	if err != nil {
		t.Fatalf("改变任务状态失败：%v", err)
	}
	// 再调用一次，version正确因为2
	after, err := repo.ChangeStatusOptimistis(ctx, before.ID, "completed", before.Version) // before.Version仍为1

	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("预期返回版本冲突错误，实际为：%v", err)
	}

	// 再次取任务
	after, err = repo.GetTaskByID(ctx, task1.ID)
	if err != nil {
		t.Fatalf("读取回滚后的任务失败：%v", err)
	}
	if after.Status != task1.Status {
		t.Errorf("回滚后状态应为 %q，实际为 %q", task1.Status, after.Status)
	}
	if after.Version != task1.Version {
		t.Errorf("回滚后版本应为 %d，实际为 %d", task1.Version, after.Version)
	}
	if !after.UpdatedAt.Equal(task1.UpdatedAt) {
		t.Errorf("回滚后更新时间应保持为 %v，实际为 %v",
			task1.UpdatedAt, after.UpdatedAt)
	}

	var count int64
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM task_events WHERE task_id = $1",
		task1.ID).Scan(&count); err != nil {
		t.Fatalf("查询回滚后的事件失败：%v", err)
	}
	if count != 1 {
		t.Errorf("回滚后任务 %d 应有 0 条事件，实际有 %d 条", task1.ID, count)
	}
}
