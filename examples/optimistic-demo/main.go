package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Result struct {
	Name       string
	StatusCode int
	Body       string
	Err        error
}

func main() {

	const taskID int64 = 20015 // 替换成测试任务 ID

	url := fmt.Sprintf(
		"http://localhost:8080/tasks/opm/%d",
		taskID,
	)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	var wg sync.WaitGroup

	// 关闭 start 后，两个 goroutine 都可以开始请求。
	start := make(chan struct{})

	// 两个任务各发送一个结果。
	results := make(chan Result, 2)

	update := func(name, status string) {
		defer wg.Done()

		body, err := json.Marshal(struct {
			Status  string `json:"status"`
			Version int64  `json:"version"`
		}{
			Status:  status,
			Version: 1,
		})
		if err != nil {
			results <- Result{Name: name, Err: err}
			return
		}

		req, err := http.NewRequest(
			http.MethodPatch,
			url,
			bytes.NewReader(body),
		)
		if err != nil {
			results <- Result{Name: name, Err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")

		// 等待统一的启动信号。
		<-start

		resp, err := client.Do(req)
		if err != nil {
			results <- Result{Name: name, Err: err}
			return
		}
		defer resp.Body.Close()

		responseBody, err := io.ReadAll(resp.Body)

		results <- Result{
			Name:       name,
			StatusCode: resp.StatusCode,
			Body:       string(responseBody),
			Err:        err,
		}
	}

	wg.Add(2)
	go update("A", "completed")
	go update("B", "in_progress")

	close(start) // 广播启动信号

	wg.Wait() // 必须放在两个任务启动之后
	close(results)

	for result := range results {
		if result.Err != nil {
			fmt.Printf("%s：请求失败：%v\n", result.Name, result.Err)
			continue
		}

		fmt.Printf(
			"%s：HTTP %d，响应：%s\n",
			result.Name,
			result.StatusCode,
			result.Body,
		)
	}
}
