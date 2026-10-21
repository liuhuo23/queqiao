package server

import (
	"sync"
	"time"

	"github.com/liuhuo23/queqiao/internal/web"
)

// eventRing 是内存事件环形缓冲（Agent 事件 + 连接事件），供管理台展示。
type eventRing struct {
	mu   sync.Mutex
	buf  []web.LogEntry
	size int
}

func newEventRing(size int) *eventRing {
	if size <= 0 {
		size = 1000
	}
	return &eventRing{size: size}
}

func (e *eventRing) add(entry web.LogEntry) {
	if entry.At.IsZero() {
		entry.At = time.Now().UTC()
	}
	e.mu.Lock()
	e.buf = append(e.buf, entry)
	if len(e.buf) > e.size {
		e.buf = e.buf[len(e.buf)-e.size:]
	}
	e.mu.Unlock()
}

func (e *eventRing) recent(n int) []web.LogEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n <= 0 || n > len(e.buf) {
		n = len(e.buf)
	}
	out := make([]web.LogEntry, 0, n)
	for i := len(e.buf) - 1; i >= len(e.buf)-n; i-- {
		out = append(out, e.buf[i])
	}
	return out
}
