package proxy

import (
	"sync"
	"time"

	"apicluster/internal/config"
)

// modelLimiter 本地限流器：并发数 + 每分钟请求数（滑动窗口）。
// 按「厂商ID/模型ID」维度隔离，各模型互不影响。
type modelLimiter struct {
	mu       sync.Mutex
	inFlight map[string]int         // 当前在途请求数
	windows  map[string][]time.Time // 每分钟滑动窗口（已按时间升序）
}

func newModelLimiter() *modelLimiter {
	return &modelLimiter{
		inFlight: map[string]int{},
		windows:  map[string][]time.Time{},
	}
}

// tryAcquire 尝试占用某模型的额度。
// 成功返回 release 释放函数与空原因；失败返回 release=nil 与原因文案。
// lim 的并发/每分钟均为 0 时视为不限，直接放行。
func (l *modelLimiter) tryAcquire(key string, lim config.ModelLimit) (release func(), reason string) {
	if lim.Concurrency <= 0 && lim.RPM <= 0 {
		return func() {}, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if lim.Concurrency > 0 && l.inFlight[key] >= lim.Concurrency {
		return nil, "并发数达到上限"
	}

	if lim.RPM > 0 {
		now := time.Now()
		cutoff := now.Add(-time.Minute)
		ws := l.windows[key]
		// 丢弃一分钟前的过期记录
		i := 0
		for i < len(ws) && ws[i].Before(cutoff) {
			i++
		}
		ws = ws[i:]
		if len(ws) >= lim.RPM {
			l.windows[key] = ws
			return nil, "每分钟请求数达到上限"
		}
		l.windows[key] = append(ws, now)
	}

	l.inFlight[key]++
	return func() {
		l.mu.Lock()
		if l.inFlight[key] > 0 {
			l.inFlight[key]--
		}
		l.mu.Unlock()
	}, ""
}
