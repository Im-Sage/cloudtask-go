package database

// 创建连接池、检查连接
import (
	"context"
	"fmt"

	"github.com/Im-Sage/cloudtask-go/day9/internal/config"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewDBPool 创建一个新的数据库连接池,dsn是数据库连接字符串，格式为""postgres://app:dev_password_only_local@localhost:5432/cloudtask?sslmode=disable"
func NewDBPool(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	// pool, err := pgxpool.New(ctx, cfg.DSN)
	poolConfig, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("解析数据库配置失败: %w", err)
	}

	poolConfig.MaxConns = cfg.MaxConns
	// 使用配置文件创建连接池
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("创建数据库连接池失败: %w", err)
	}

	pingCtx, cancle := context.WithTimeout(
		ctx, cfg.PingTimeout,
	)
	defer cancle()

	if err := pool.Ping(pingCtx); err != nil {
		defer pool.Close()                                         // Ping失败时关闭连接池
		return nil, fmt.Errorf("Unable to ping database: %v", err) // 错误返回给调用方处理
	}

	fmt.Println("Connection pool created successfully")
	return pool, nil // 成功创建连接池之后，关闭应该由调用方负责
}
