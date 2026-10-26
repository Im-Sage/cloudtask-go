package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Job struct {
	ID    int
	Value string
}

// Result 代表一个工作结果，包含工作ID、执行该工作的worker ID、结果值以及可能的错误信息。
type Result struct {
	JobID    int
	WorkerID int
	Value    string
	Error    error
}

func worker(
	workerID int,
	jobs <-chan Job,
	results chan<- Result,
	wg *sync.WaitGroup,
	ctx context.Context) {
	// 确保在函数退出时调用Done，通知WaitGroup该worker已完成工作。
	defer wg.Done() // 计数器减一
	// 从 jobs通道中接收工作，直到通道关闭。
	for {
		select {
		case <-ctx.Done():
			fmt.Printf("worker %d 收到取消信号，退出\n", workerID)
			return
		case job, ok := <-jobs:
			if !ok {
				fmt.Printf("worker %d 检测到jobs通道已关闭，退出\n", workerID)
				return
			}

			// 模拟工作处理，这里可以替换为实际的工作逻辑。
			fmt.Printf("job%d正在处理中\n", job.ID)
			if job.ID == 1 {
				// 模拟一个错误
				fmt.Printf("job%d处理出错\n", job.ID)
				result := Result{
					JobID:    job.ID,
					WorkerID: workerID,
					Value:    fmt.Sprintf("GoJob%d", job.ID),
					Error:    fmt.Errorf("job%d处理出错", job.ID),
				}
				select {
				case <-ctx.Done():
					fmt.Printf("worker %d 在发送结果时接收到取消信号，退出\n", workerID)
					return
				case results <- result:
				}
				continue
			}
			select {
			case <-ctx.Done():
				fmt.Printf("worker %d 在处理job%d时接收到取消信号，退出\n", workerID, job.ID)
				return
			case <-time.After(500 * time.Millisecond): // 模拟处理时间
				// 处理完成，发送结果
				result := Result{
					JobID:    job.ID,
					WorkerID: workerID,
					Value:    fmt.Sprintf("GoJob%d", job.ID),
					Error:    nil, // 假设没有错误发生
				}
				// 将结果发送到 results通道。
				select {
				case <-ctx.Done():
					fmt.Printf("worker %d 在发送结果时接收到取消信号，退出\n", workerID)
					return
				case results <- result: // 若results通道未被接收，可能会阻塞，导致死锁
					fmt.Printf("worker %d 完成了job%d的处理\n", workerID, job.ID)
				}
			}
		}
	}
}
