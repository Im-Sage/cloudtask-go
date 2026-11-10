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
			<-startChannel
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
	close(startChannel)
	wg.Wait()

	actual := count.Load()
	t.Logf("20 个请求，回调实际执行 %d 次", actual)

	if actual != 1 {
		t.Errorf("期望回调执行 1 次，实际执行 %d 次", actual)
	}

	// TODO 3: 每个 Goroutine 调用 group.Do()

	// TODO 4: fn 内增加 count，并模拟耗时操作

	// TODO 5: 等待全部 Goroutine 返回

	// TODO 6: 检查 count 和各个请求的返回结果
}
