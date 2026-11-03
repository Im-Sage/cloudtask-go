package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()
	databaseURL := "postgres://app:dev_password_only_local@localhost:5432/cloudtask?sslmode=disable"
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatalf("错误%v", err)
	}
	config.MaxConns = 3
	config.MinConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		log.Fatalf("错误%v", err)
	}

	pingCtx, pingCancel := context.WithTimeout(
		ctx,   // 以ctx为父上下文
		3 * time.Second,
	)
	defer pingCancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		log.Fatalf("数据库连接失败：%v", err)
	}
	defer pool.Close()
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func ()  {
			defer wg.Done()
			_, err := pool.Exec(ctx, "select pg_sleep(0.2)")
			if err != nil {
				log.Fatalf("select 执行失败：%v", err)
			}
		}()
	}
	wg.Wait()


	stats := pool.Stat()
	fmt.Println("总连接数：", stats.TotalConns())
	fmt.Println("已使用连接：", stats.AcquiredConns())
	fmt.Println("空闲连接：", stats.IdleConns())
	fmt.Println("累计获取次数：", stats.AcquireCount())
	fmt.Println("累计等待时间：", stats.AcquireDuration())
}
