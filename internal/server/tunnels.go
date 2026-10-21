package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"nrp/internal/store"
)

const maxHTTPHead = 32 * 1024

type tcpTunnel struct {
	rule store.Rule
	ln   net.Listener
}

const (
	// maxConnsPerRule 单条映射的并发连接上限。
	maxConnsPerRule = 256
	// maxConnsPerIP 单个来源 IP 的并发连接上限。
	maxConnsPerIP = 64
)

// connLimiter 按来源 IP 限制并发连接数。
type connLimiter struct {
	mu    sync.Mutex
	perIP map[string]int
}

func newConnLimiter() *connLimiter { return &connLimiter{perIP: map[string]int{}} }

func (l *connLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP[ip] >= maxConnsPerIP {
		return false
	}
	l.perIP[ip]++
	return true
}

func (l *connLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.perIP[ip] - 1
	if n <= 0 {
		delete(l.perIP, ip)
		return
	}
	l.perIP[ip] = n
}

// tunnelManager 负责公网监听的生命周期：规则 + 设备在线状态 -> 监听器。
type tunnelManager struct {
	srv      *Server
	mu       sync.Mutex
	tcp      map[string]*tcpTunnel
	httpLn   net.Listener
	httpAddr string
	closed   bool

	counters sync.Map // ruleID -> *counters
	errs     sync.Map // ruleID -> string

	limiter *connLimiter
	httpErr string
}

func newTunnelManager(s *Server) *tunnelManager {
	return &tunnelManager{srv: s, tcp: map[string]*tcpTunnel{}, limiter: newConnLimiter()}
}

// HTTPError 返回 HTTP 域名映射监听器的最近错误（供管理台展示）。
func (t *tunnelManager) HTTPError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.httpErr
}

func (t *tunnelManager) counterFor(ruleID string) *counters {
	if v, ok := t.counters.Load(ruleID); ok {
		return v.(*counters)
	}
	c := &counters{}
	actual, _ := t.counters.LoadOrStore(ruleID, c)
	return actual.(*counters)
}

func (t *tunnelManager) ruleStats(ruleID string) (in, out, cur, total int64) {
	if v, ok := t.counters.Load(ruleID); ok {
		return v.(*counters).snapshot()
	}
	return 0, 0, 0, 0
}

func (t *tunnelManager) setRuleError(ruleID, msg string) {
	if msg == "" {
		t.errs.Delete(ruleID)
		return
	}
	t.errs.Store(ruleID, msg)
}

func (t *tunnelManager) ruleError(ruleID string) string {
	if v, ok := t.errs.Load(ruleID); ok {
		return v.(string)
	}
	return ""
}

// reconcileAll 按当前规则与在线状态重建监听（幂等）。
func (t *tunnelManager) reconcileAll() {
	settings := t.srv.store.Settings()
	rules := t.srv.store.ListRules("")
	online := t.srv.sessions.onlineIDs()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}

	desired := make(map[string]store.Rule)
	existing := make(map[string]bool)
	for _, r := range rules {
		existing[r.ID] = true
		if r.Proto == store.ProtoTCP && r.Enabled && online[r.DeviceID] {
			desired[r.ID] = r
		}
	}

	// 关闭不再需要的监听。
	for id, tun := range t.tcp {
		r, keep := desired[id]
		if !keep || tun.rule.RemotePort != r.RemotePort {
			_ = tun.ln.Close()
			delete(t.tcp, id)
			t.srv.log.Info("公网端口已关闭", "rule_id", id, "port", tun.rule.RemotePort)
		}
	}

	// 创建缺失的监听。
	for id, r := range desired {
		if _, ok := t.tcp[id]; ok {
			continue
		}
		addr := net.JoinHostPort(t.srv.cfg.PublicBindAddr, strconv.Itoa(r.RemotePort))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.setRuleError(id, fmt.Sprintf("公网端口 %d 监听失败：%v", r.RemotePort, err))
			t.srv.log.Warn("公网端口监听失败", "rule_id", id, "port", r.RemotePort, "err", err)
			continue
		}
		t.setRuleError(id, "")
		tun := &tcpTunnel{rule: r, ln: ln}
		t.tcp[id] = tun
		go t.acceptTCP(tun)
		t.srv.log.Info("公网端口已监听", "rule_id", id, "port", r.RemotePort, "device_id", r.DeviceID)
	}

	// 清理已删除规则的历史错误。
	t.errs.Range(func(k, _ any) bool {
		if !existing[k.(string)] {
			t.errs.Delete(k)
		}
		return true
	})

	t.reconcileHTTPLocked(settings)
}

// reconcileDevice 在设备上线/下线时重建监听。
func (t *tunnelManager) reconcileDevice(deviceID string) {
	t.reconcileAll()
}

func (t *tunnelManager) acceptTCP(tun *tcpTunnel) {
	for {
		c, err := tun.ln.Accept()
		if err != nil {
			return
		}
		go t.handlePublicConn(tun.rule.ID, c, c, nil)
	}
}

// handlePublicConn 处理一条公网连接：查规则/会话 -> 交给 session 打通数据流。
func (t *tunnelManager) handlePublicConn(ruleID string, pub net.Conn, inbound io.Reader, preface []byte) {
	rule, ok := t.srv.store.GetRule(ruleID)
	if !ok || !rule.Enabled {
		_ = pub.Close()
		return
	}
	sess := t.srv.sessions.get(rule.DeviceID)
	if sess == nil {
		_ = pub.Close()
		return
	}
	cnt := t.counterFor(ruleID)
	if cnt.connsCurrent.Load() >= maxConnsPerRule {
		_ = pub.Close()
		t.srv.log.Warn("单条映射并发连接数达到上限，拒绝新连接", "rule_id", ruleID, "limit", maxConnsPerRule)
		return
	}
	clientIP := remoteHost(pub)
	if !t.limiter.acquire(clientIP) {
		_ = pub.Close()
		t.srv.log.Warn("单 IP 并发连接数达到上限，拒绝新连接", "ip", clientIP, "limit", maxConnsPerIP)
		return
	}
	defer t.limiter.release(clientIP)

	cnt.connsCurrent.Add(1)
	cnt.connsTotal.Add(1)
	t.srv.stats.connsCurrent.Add(1)
	t.srv.stats.connsTotal.Add(1)
	defer func() {
		cnt.connsCurrent.Add(-1)
		t.srv.stats.connsCurrent.Add(-1)
	}()

	traffic := t.srv.store.Settings().TrafficStatsEnabled
	sess.serveData(rule, pub, inbound, preface, cnt, traffic)
}

// ---------------- HTTP 域名映射 ----------------

func (t *tunnelManager) reconcileHTTPLocked(settings store.Settings) {
	if settings.HTTPAddr == "" {
		if t.httpLn != nil {
			_ = t.httpLn.Close()
			t.httpLn = nil
			t.httpAddr = ""
			t.httpErr = ""
			t.srv.log.Info("HTTP 域名映射监听已关闭")
		}
		return
	}
	if t.httpLn != nil && t.httpAddr == settings.HTTPAddr {
		return
	}
	if t.httpLn != nil {
		_ = t.httpLn.Close()
		t.httpLn = nil
	}
	ln, err := net.Listen("tcp", settings.HTTPAddr)
	if err != nil {
		t.httpErr = fmt.Sprintf("HTTP 监听 %s 失败：%v", settings.HTTPAddr, err)
		t.srv.log.Warn("HTTP 域名映射监听失败", "addr", settings.HTTPAddr, "err", err)
		return
	}
	t.httpErr = ""
	t.httpLn = ln
	t.httpAddr = settings.HTTPAddr
	go t.acceptHTTP(ln)
	t.srv.log.Info("HTTP 域名映射已监听", "addr", settings.HTTPAddr)
}

func (t *tunnelManager) acceptHTTP(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go t.handleHTTPConn(c)
	}
}

func (t *tunnelManager) handleHTTPConn(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReaderSize(c, 4096)
	head, host, err := readHTTPHead(br, maxHTTPHead)
	if err != nil {
		writeHTTPError(c, 400, "bad request")
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	rule, ok := t.findHTTPRule(host)
	if !ok {
		writeHTTPError(c, 404, "no matching tunnel for host "+host)
		_ = c.Close()
		return
	}
	t.handlePublicConn(rule.ID, c, br, head)
}

func (t *tunnelManager) findHTTPRule(host string) (store.Rule, bool) {
	if host == "" {
		return store.Rule{}, false
	}
	for _, r := range t.srv.store.ListRules("") {
		if r.Proto == store.ProtoHTTP && r.Enabled &&
			strings.EqualFold(r.Domain, host) && t.srv.sessions.online(r.DeviceID) {
			return r, true
		}
	}
	return store.Rule{}, false
}

// readHTTPHead 读取 HTTP 请求头原始字节（用于原样转发），并解析 Host。
func readHTTPHead(br *bufio.Reader, max int) ([]byte, string, error) {
	var head []byte
	for len(head) < max {
		b, err := br.ReadByte()
		if err != nil {
			return nil, "", err
		}
		head = append(head, b)
		if len(head) >= 4 && head[len(head)-4] == '\r' && head[len(head)-3] == '\n' &&
			head[len(head)-2] == '\r' && head[len(head)-1] == '\n' {
			return head, parseHTTPHost(head), nil
		}
	}
	return nil, "", fmt.Errorf("请求头过大")
}

func parseHTTPHost(head []byte) string {
	lines := strings.Split(string(head), "\r\n")
	for _, line := range lines[1:] {
		if len(line) < 5 {
			continue
		}
		if strings.EqualFold(line[:5], "host:") {
			h := strings.TrimSpace(line[5:])
			if strings.HasPrefix(h, "[") {
				if i := strings.IndexByte(h, ']'); i >= 0 {
					h = h[1:i]
				}
			} else if i := strings.IndexByte(h, ':'); i >= 0 {
				h = h[:i]
			}
			return strings.ToLower(h)
		}
	}
	return ""
}

func writeHTTPError(c net.Conn, status int, msg string) {
	body := msg + "\n"
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, httpStatusText(status), len(body), body)
}

func httpStatusText(status int) string {
	switch status {
	case 400:
		return "Bad Request"
	case 404:
		return "Not Found"
	case 502:
		return "Bad Gateway"
	default:
		return "Error"
	}
}

func (t *tunnelManager) closeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for id, tun := range t.tcp {
		_ = tun.ln.Close()
		delete(t.tcp, id)
	}
	if t.httpLn != nil {
		_ = t.httpLn.Close()
		t.httpLn = nil
	}
	t.httpErr = ""
}
