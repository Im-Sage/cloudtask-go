package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

func main() {
	// 创建一个WaitGroup，用于等待所有worker完成工作。
	var wg sync.WaitGroup

	// 创建一个上下文，用于在需要时取消所有worker的工作。
	ctx, cancel := context.WithTimeout(
		context.Background(),
		800*time.Millisecond,
	)
	defer cancel() // 确保在main函数退出时取消上下文

	// 创建一个通道，用于传递工作任务。 无缓冲
	jobs := make(chan Job)
	// 创建一个通道，用于接收工作结果。 无缓冲
	results := make(chan Result)

	// 启动多个worker，每个worker在一个独立的goroutine中运行。
	numWorkers := 3
	for i := 1; i <= numWorkers; i++ {
		wg.Add(1) // 增加WaitGroup计数器
		go worker(i, jobs, results, &wg, ctx)
	}

	// 提交10个Job到jobs通道中。
	wg.Add(1) // 增加WaitGroup计数器，确保在提交完所有工作后调用Done
	go func() {
		defer wg.Done()   // 确保在提交完所有工作后调用Done，通知WaitGroup该goroutine已完成工作
		defer close(jobs) // 确保在提交完所有工作后关闭jobs通道
		for j := 1; j <= 10; j++ {
			// if j == 6 {
			// 	cancel() // 取消上下文，通知所有worker停止工作
			// }
			select {
			case <-ctx.Done():
				fmt.Println("提交者收到取消信号，停止提交工作")
				return
			case jobs <- Job{ID: j, Value: fmt.Sprintf("GoJob%d", j)}:
			}
		}
	}()

	/**
	不开一个新协程的结果
	wg.Wait() main 等待所有worker完成工作，会在这里阻塞，
	到达不了下面的for循环，导致死锁

	*/

	go func() {
		wg.Wait()      // 等待所有worker完成工作
		close(results) // 关闭results通道，通知主goroutine所有结果已处理完毕
	}()

	//  收集并打印所有结果。range results 的每一次循环代表完成一个结果的接收
	for result := range results {
		if result.Error != nil {
			fmt.Printf("Job %d由Worker %d处理时发生错误: %v\n", result.JobID, result.WorkerID, result.Error)
		} else {
			fmt.Printf("Job %d由Worker %d处理完成，结果: %s\n", result.JobID, result.WorkerID, result.Value)
		}
	}

}
