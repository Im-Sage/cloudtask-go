package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// 一个正常任务
func TestWorkerSucess(t *testing.T) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		2*time.Second)
	defer cancel() // 确保在 main 函数退出时取消上下文

	jobs := make(chan Job, 1)       // 缓冲为1，确保至少有一个工作可以被提交
	results := make(chan Result, 1) // 缓冲为1，确保至少有一个结果可以被接收

	var wg sync.WaitGroup
	wg.Add(1)
	go worker(1, jobs, results, &wg, ctx)

	jobs <- Job{2, "GoJob2"}
	close(jobs) // 关闭jobs通道，通知worker没有更多的工作

	select {
	case got := <-results:
		if got.JobID != 2 || got.Value != "GoJob2" || got.Error != nil {
			t.Errorf("worker处理结果不正确: got %+v", got)
		}
	case <-ctx.Done():
		t.Errorf("worker处理超时")
	}
	wg.Wait() // 等待worker完成工作
}

// 一个任务处理出错
func TestWorkerError(t *testing.T) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		2*time.Second)
	defer cancel() // 确保在 main 函数退出时取消上下文

	jobs := make(chan Job, 1)       // 缓冲为1，确保至少有一个工作可以被提交
	results := make(chan Result, 1) // 缓冲为1，确保至少有一个结果可以被接收

	var wg sync.WaitGroup
	wg.Add(1)
	go worker(1, jobs, results, &wg, ctx)

	jobs <- Job{1, "GoJob1"} // 这个任务会模拟出错
	close(jobs)              // 关闭jobs通道，通知worker没有更多的工作

	select {
	case got := <-results:
		if got.JobID != 1 || got.Value != "GoJob1" || got.Error == nil {
			t.Errorf("worker处理结果不正确: got %+v", got)
		}
	case <-ctx.Done():
		t.Errorf("worker处理超时")
	}
	wg.Wait() // 等待worker完成工作
}

// 测试10个任务的处理，确保所有任务都能被正确处理
func TestWorkerPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		3*time.Second)
	defer cancel() // 确保在 main 函数退出时取消上下文

	jobs := make(chan Job, 10)       // 缓冲为10，确保所有工作可以被提交
	results := make(chan Result, 10) // 缓冲为10，确保所有结果可以被接收

	var wg sync.WaitGroup
	numWorkers := 3

	for i := 1; i <= numWorkers; i++ {
		wg.Add(1)
		go worker(i, jobs, results, &wg, ctx)
	}

	for j := 1; j <= 10; j++ {
		jobs <- Job{j, fmt.Sprintf("GoJob%d", j)}
	}

	close(jobs) // 关闭jobs通道，通知worker没有更多的工作

	receivedResults := make(map[int]Result)

	for i := 0; i < 10; i++ {
		select {
		case got := <-results:
			receivedResults[got.JobID] = got
		case <-ctx.Done():
			t.Errorf("worker处理超时")
			return
		}
	}

	// 检查所有任务是否都被正确处理
	for j := 1; j <= 10; j++ {
		got, exists := receivedResults[j]
		if !exists {
			t.Errorf("任务 %d 没有被处理", j)
			continue
		}
		if got.JobID != j || got.Value != fmt.Sprintf("GoJob%d", j) || (j != 1 && got.Error != nil) {
			t.Errorf("任务 %d 的处理结果不正确: got %+v", j, got)
		}
	}
	wg.Wait() // 等待所有worker完成工作
}
