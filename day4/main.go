package main

import (
	"fmt"
	"sync"
	"time"
)

func main() {
	var wg sync.WaitGroup

	tm := NewTaskManager()
	tm.Add("chen")
	tm.Add("Wu")
	tm.Add("Liu")
	tm.Add("Zhang")
	tm.Add("Li")
	fmt.Printf("tasks: %v\n", tm.tasks)

	for i := 1; i <= 5; i++ {
		wg.Add(1) // 登记一个待完成的任务，计数器 + 1

		go func(id int) {
			defer wg.Done() // 一个任务完成 计数器 - 1
			// defer 代表在函数退出时执行，确保即使发生错误也会调用 Done()，避免死锁
			fmt.Printf("任务 %d 开始\n", id)
			// 模拟任务执行时间
			// time.Sleep(time.Second * time.Duration(rand.Intn(3)+1))
			fmt.Printf("任务 %d 执行中...\n", tm.tasks[id].Id)

			time.Sleep(time.Second * 1) // 模拟任务执行时间
			fmt.Printf("任务 %d 完成\n", id)
		}(i) // 把当前i的值传递给匿名函数，避免闭包问题
	}
	wg.Wait() // 阻塞当前 Goroutine（main的协程），等待计数器变为 0
	fmt.Printf("all tasks finished\n")
}
