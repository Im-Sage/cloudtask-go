package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

func TestSingleflightSameKey(t *testing.T) {

	var group singleflight.Group
	var count atomic.Int32

	// TODO 1: 创建 WaitGroup、start channel
	var wg sync.WaitGroup

	startChannel := make(chan struct{}) // 无缓冲

	// TODO 2: 启动 20 个 Goroutine

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startChannel // 管道中无数据且未关闭时阻塞在这
			value, err, shared := group.Do(
				"task:12",
				func() (any, error) {
					// 统计回调
					count.Add(1)
					// 模拟耗时查询，让其他请求有机会加入等待
					time.Sleep(100 * time.Millisecond)
					return "任务12的数据", nil
				})
			if err != nil {
				t.Errorf("查询失败：%v", err)
				return
			}

			if value != "任务12的数据" {
				t.Errorf("返回结果不符合预期：%v", value)
			}

			t.Logf("value=%v, shared=%v", value, shared)
		}()
	}
	// 本次循环并没有向管道中存放数据，此时20个Goroutine都被阻塞在<-startChannel
	// 一旦关闭管道，20个Goroutine同时被释放，模拟的很精妙 // 集中放行
	close(startChannel)
	wg.Wait()

	actual := count.Load()
	t.Logf("20 个请求，回调实际执行 %d 次", actual)

	if actual != 1 {
		t.Errorf("期望回调执行 1 次，实际执行 %d 次", actual)
	}
}

func TestSingleflightSameKeyDoChan(t *testing.T) {
	var group singleflight.Group
	var count atomic.Int32

	// 不关闭release，回调就不能结束
	release := make(chan struct{})

	work := func() (any, error) {
		count.Add(1) // 回调次数+1
		<-release
		return "任务12的数据", nil
	}

	const callers = 20
	// 创建一个长度为callers的切片，每一个元素用来保存一个只接收的结果Channel
	// singleflight.Result 管道传递的数据类型
	/**
		type Result struct {
		Val    interface{} // 返回的数据
		Err    error
		Shared bool
	}
	*/
	results := make([]<-chan singleflight.Result, callers)

	// DoChan注册调用后立即返回接受结果Channel，不必须等待工作完成。 Do返回 Result类型，等待任务完成
	// 此时回调被release阻塞，不会提前结束  -- 重点
	// 开始注册
	for i := range results {
		results[i] = group.DoChan("task:12", work)
		// 所有调用都注册完成，才允许回调返回
	}
	close(release)
	// --以上属于注册阶段
	// 给整个结果收集过程设置超时，避免无限等待
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()

	for i, ch := range results {
		select {
		case result := <-ch:
			if result.Err != nil {
				t.Errorf("调用 %d：预期无错误，实际为 %v", i, result.Err)
			}
			if result.Val != "任务12的数据" {
				t.Errorf("调用 %d：预期得到任务12的数据，实际为 %v",
					i, result.Val)
			}
			if !result.Shared {
				t.Errorf("调用 %d：预期结果被共享", i)
			}
		case <-timer.C:
			t.Fatalf("等待调用 %d 的结果超时", i)
		}
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("预期回调执行 1 次，实际执行 %d 次", got)
	}
}
