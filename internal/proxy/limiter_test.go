package proxy

import (
	"testing"
	"time"

	"apicluster/internal/config"
)

// 验证并发限制：concurrency=1 时第二个并发的请求应被拒
func TestLimiter_Concurrency(t *testing.T) {
	l := newModelLimiter()
	lim := config.ModelLimit{Concurrency: 1, RPM: 0}

	release, reason := l.tryAcquire("p/m", lim)
	if release == nil {
		t.Fatalf("第一次占用应成功，却被拒: %s", reason)
	}

	// 未释放时，第二次占用应被拒
	_, reason2 := l.tryAcquire("p/m", lim)
	if reason2 == "" {
		t.Fatal("并发达到上限后应被拒，但返回空原因（意味着放行）")
	}
	if reason2 != "并发数达到上限" {
		t.Fatalf("原因应为「并发数达到上限」，实际: %s", reason2)
	}

	// 释放后应可再次占用
	release()
	release3, reason3 := l.tryAcquire("p/m", lim)
	if release3 == nil {
		t.Fatalf("释放后应可占用，却被拒: %s", reason3)
	}
	release3()
}

// 验证 RPM 限制：rpm=2 时第三次的请求应被拒
func TestLimiter_RPM(t *testing.T) {
	l := newModelLimiter()
	lim := config.ModelLimit{Concurrency: 0, RPM: 2}

	r1, reason1 := l.tryAcquire("p/m", lim)
	if r1 == nil {
		t.Fatalf("第1次应成功: %s", reason1)
	}
	r2, reason2 := l.tryAcquire("p/m", lim)
	if r2 == nil {
		t.Fatalf("第2次应成功: %s", reason2)
	}

	// 第三次应被 RPM 拒绝
	_, reason3 := l.tryAcquire("p/m", lim)
	if reason3 != "每分钟请求数达到上限" {
		t.Fatalf("第3次应被 RPM 拒绝，实际: %q", reason3)
	}

	// 释放一个后仍被限（窗口内还是 2 次）
	r1()
	_, reason4 := l.tryAcquire("p/m", lim)
	if reason4 != "每分钟请求数达到上限" {
		t.Fatalf("释放一个后窗口内仍应限流，实际: %q", reason4)
	}
	r2()
}

// 验证滑动窗口：超过一分钟后旧请求不计入
func TestLimiter_WindowSlide(t *testing.T) {
	l := newModelLimiter()
	lim := config.ModelLimit{Concurrency: 0, RPM: 1}

	r, _ := l.tryAcquire("p/m", lim)
	// 手动把时间回溯到一分钟前，模拟过期
	l.mu.Lock()
	l.windows["p/m"][0] = time.Now().Add(-61 * time.Second)
	l.mu.Unlock()

	// 应可再次占用（旧记录已过期）
	r2, reason := l.tryAcquire("p/m", lim)
	if r2 == nil {
		t.Fatalf("过期后应放行，却被拒: %s", reason)
	}
	r()
	r2()
}

// 验证不限流（0,0）时永远放行
func TestLimiter_Unlimited(t *testing.T) {
	l := newModelLimiter()
	lim := config.ModelLimit{Concurrency: 0, RPM: 0}
	for i := 0; i < 100; i++ {
		r, reason := l.tryAcquire("p/m", lim)
		if r == nil {
			t.Fatalf("不限流时应始终放行，第%d次被拒: %s", i, reason)
		}
	}
}
