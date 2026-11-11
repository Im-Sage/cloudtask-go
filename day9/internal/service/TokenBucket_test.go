package service

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestTokenBu(t *testing.T) {
	// 长期放行速率约为每秒 10 个，但最多积累 5 个令牌用于突发。
	limiter := rate.NewLimiter(rate.Limit(10), 3)  // 每100ms补充一个令牌

	passed := limiter.Allow()
	fmt.Println("passed为%w", passed)

	passed = limiter.Allow()
	fmt.Println("passed为%w", passed)

	passed = limiter.Allow()
	fmt.Println("passed为%w", passed)

	passed = limiter.Allow()
	fmt.Println("passed为%w", passed)

	time.Sleep(150 * time.Millisecond)

	passed = limiter.Allow()
	fmt.Println("passed为%w", passed)

}
