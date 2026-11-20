package service

import (
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestTokenBu(t *testing.T) {
	now := time.Now()
	// 长期放行速率约为每秒 10 个，但最多积累 3 个令牌用于突发。
	limiter := rate.NewLimiter(rate.Limit(10), 3) // 每100ms补充一个令牌

	// 同一时间消耗全部初始令牌
	if !limiter.AllowN(now, 3) { // 判断在指定时刻能否一次消耗相应数量的令牌
		t.Fatal("初始应允许消耗3个令牌")
	}
	// 时间没有推进，不应再放行
	if limiter.AllowN(now, 1) {
		t.Fatal("令牌耗尽后应拒绝请求")
	}

	// 若为true(!表达式) 则说150ms后仍没有令牌
	if !limiter.AllowN(now.Add(150*time.Millisecond), 1) {
		t.Fatal("150ms 后应已补足一个令牌")
	}

}
