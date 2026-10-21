package server

import (
	"sync"
	"sync/atomic"
	"time"
)

// counters 是单条规则的流量与连接计数。
type counters struct {
	bytesIn      atomic.Int64
	bytesOut     atomic.Int64
	connsCurrent atomic.Int64
	connsTotal   atomic.Int64
}

func (c *counters) addIn(n int64)  { c.bytesIn.Add(n) }
func (c *counters) addOut(n int64) { c.bytesOut.Add(n) }

func (c *counters) snapshot() (in, out, cur, total int64) {
	return c.bytesIn.Load(), c.bytesOut.Load(), c.connsCurrent.Load(), c.connsTotal.Load()
}

// globalStats 是服务端全局统计。
type globalStats struct {
	devicesTotal   atomic.Int64
	bytesIn        atomic.Int64
	bytesOut       atomic.Int64
	connsCurrent   atomic.Int64
	connsTotal     atomic.Int64
	authFailures   atomic.Int64
	sessionsActive atomic.Int64
}

// rateLimiter 是简单的按 IP 令牌桶。
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func newIPLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, buckets: map[string]*bucket{}}
}

// reset 在认证成功后清空该 IP 的失败计数（限流只针对失败尝试）。
func (l *rateLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.buckets, ip)
	l.mu.Unlock()
}

func (l *rateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok || now.After(b.reset) {
		if len(l.buckets) > 10000 {
			for k, v := range l.buckets {
				if now.After(v.reset) {
					delete(l.buckets, k)
				}
			}
		}
		l.buckets[ip] = &bucket{count: 1, reset: now.Add(l.window)}
		return true
	}
	b.count++
	return b.count <= l.limit
}
