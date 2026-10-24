package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// 生产者
func producer(ctx context.Context, jobs chan<- int) {
	defer close(jobs) // 关闭通道，通知消费者没有更多数据了
	for i := 1; i <= 10; i++ {
		select {
		case <-ctx.Done():
			fmt.Println("生产者收到取消信号，退出")
			return

		case jobs <- i:
			fmt.Printf("生产者生产了数据: %d\n", i)
		}
	}
}

// 消费者
func consumer(ctx context.Context, jobs <-chan int) {
	for {
		select {
		case <-ctx.Done():
			fmt.Println("消费者收到取消信号，退出，原因:", ctx.Err())
			return

		case num, ok := <-jobs:
			if !ok {
				fmt.Println("通道已关闭，消费者退出")
				return
			}
			fmt.Printf("消费者消费了数据: %d\n", num)

			// 模拟处理时间
			select {
			case <-time.After(500 * time.Millisecond):
				fmt.Println("消费者处理数据完成:", num)

			case <-ctx.Done():
				fmt.Println("消费者在处理数据时收到取消信号，退出，原因:", ctx.Err())
				return
			}
		default:
			fmt.Printf("当前无任务\\n")
		}
	}
}

func main() {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		1000*time.Millisecond,
	)
	defer cancel() // 确保在 main 函数退出时取消上下文

	jobs := make(chan int)

	var wg sync.WaitGroup
	wg.Add(2) // 等待两个 goroutine 完成

	go func() {
		defer wg.Done()
		producer(ctx, jobs)
	}()

	go func() {
		defer wg.Done()
		consumer(ctx, jobs)
	}()

	// 模拟用户在 1.2 秒后取消操作
	// time.Sleep(1200 * time.Millisecond)
	// fmt.Println("主函数发送取消信号")
	// cancel()

	wg.Wait()
	fmt.Println("主程序：两个Goroutine均已退出")
}
