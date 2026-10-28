package database

// 创建连接池、检查连接
import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewDBPool 创建一个新的数据库连接池,dsn是数据库连接字符串，格式为""postgres://app:dev_password_only_local@localhost:5432/cloudtask?sslmode=disable"
func NewDBPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("Unable to create connection pool: %v", err)
	}

	if err := pool.Ping(ctx); err != nil {
		defer pool.Close()                                         // Ping失败时关闭连接池
		return nil, fmt.Errorf("Unable to ping database: %v", err) // 错误返回给调用方处理
	}

	fmt.Println("Connection pool created successfully")
	return pool, nil // 成功创建连接池之后，关闭应该由调用方负责
}
