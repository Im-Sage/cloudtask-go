package main

import (
	"fmt"
	"sync"
)

// 生产者
func producer(ch chan<- *Task) {
	defer close(ch) // 关闭通道，通知消费者没有更多数据了
	for i := 1; i <= 10; i++ {
		ch <- &Task{
			Id:   i,
			Name: fmt.Sprintf("Task %d", i),
			Done: false,
		} // 发送数据到通道
		// ch <- i // 发送数据到通道
		//fmt.Printf("生产者生产了数据: %d\n", i)
	}
}

// 消费者
func consumer(ch <-chan *Task) {
	for task := range ch {
		fmt.Printf("消费者消费了数据: %s\n", task.Name)
	}
	// 不使用 range 循环，而是使用 for 循环和 select 语句来处理通道的关闭情况
	// for {
	// 	select {
	// 	case num, ok := <-ch:
	// 		if !ok {
	// 			// 通道已关闭，退出循环
	// 			fmt.Printf("关闭通道后的num: %d, ok: %v\n", num, ok)
	// 			return
	// 		}
	// 		fmt.Printf("消费者消费了数据: %d\n", num)
	// 	}
	// }
	fmt.Println("消费者没有更多数据可消费，退出")
}

func main() {
	ch := make(chan *Task, 10) // 创建一个无缓冲通道

	var wg sync.WaitGroup
	wg.Add(2) // 等待两个 goroutine 完成

	go func() {
		defer wg.Done()
		producer(ch)
	}()

	go func() {
		defer wg.Done()
		consumer(ch)
	}()

	wg.Wait()

	fmt.Println("所有生产者和消费者都已完成")
}
