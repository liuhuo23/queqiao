// Package web 提供管理台 HTTP 服务：JSON API + 内嵌单页前端。
package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"nrp/internal/store"
)

//go:embed static
var staticFiles embed.FS

// Err 是带 HTTP 状态码与业务错误码的接口错误。
type Err struct {
	Status  int
	Code    string
	Message string
}

func (e *Err) Error() string { return e.Message }

// NewErr 构造接口错误。
func NewErr(status int, code, format string, args ...any) *Err {
	return &Err{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// 常用错误。
var (
	ErrUnauthorized = NewErr(http.StatusUnauthorized, "unauthorized", "未登录或会话已过期")
	ErrNotFound     = NewErr(http.StatusNotFound, "not_found", "资源不存在")
	ErrConflict     = NewErr(http.StatusConflict, "conflict", "资源冲突")
)

// 视图类型（API 契约见 docs/API.md）。

// AdminView 是登录态管理员信息。
type AdminView struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	MustChangePassword bool   `json:"must_change_password"`
}

// DeviceView 是设备列表项（含运行时字段）。
type DeviceView struct {
	store.Device
	Online      bool       `json:"online"`
	OnlineSince *time.Time `json:"online_since,omitempty"`
	LastSeenSec int64      `json:"last_seen_sec"`
	RuleCount   int        `json:"rule_count"`
}

// RuleView 是规则列表项（含运行时状态与流量）。
type RuleView struct {
	store.Rule
	DeviceName   string `json:"device_name"`
	State        string `json:"state"`
	Error        string `json:"error"`
	PublicAddr   string `json:"public_addr"`
	BytesIn      int64  `json:"bytes_in"`
	BytesOut     int64  `json:"bytes_out"`
	ConnsCurrent int64  `json:"conns_current"`
	ConnsTotal   int64  `json:"conns_total"`
}

// ServerInfo 是服务端基本信息。
type ServerInfo struct {
	Version     string `json:"version"`
	UptimeSec   int64  `json:"uptime_sec"`
	ControlAddr string `json:"control_addr"`
	WebAddr     string `json:"web_addr"`
	PublicHost  string `json:"public_host"`
	PortRange   [2]int `json:"port_range"`
	HTTPAddr    string `json:"http_addr"`
}

// Counts 是聚合计数。
type Counts struct {
	DevicesTotal   int `json:"devices_total"`
	DevicesOnline  int `json:"devices_online"`
	DevicesPending int `json:"devices_pending"`
	RulesTotal     int `json:"rules_total"`
	RulesActive    int `json:"rules_active"`
}

// Traffic 是全局流量统计。
type Traffic struct {
	BytesIn      int64 `json:"bytes_in"`
	BytesOut     int64 `json:"bytes_out"`
	ConnsCurrent int64 `json:"conns_current"`
	ConnsTotal   int64 `json:"conns_total"`
}

// Overview 是概览接口响应。
type Overview struct {
	Server  ServerInfo `json:"server"`
	Counts  Counts     `json:"counts"`
	Traffic Traffic    `json:"traffic"`
}

// LogEntry 是日志条目（审计或 Agent 事件）。
type LogEntry struct {
	At     time.Time `json:"at"`
	Level  string    `json:"level"`
	Actor  string    `json:"actor,omitempty"`
	Action string    `json:"action,omitempty"`
	Target string    `json:"target,omitempty"`
	Detail string    `json:"detail,omitempty"`
	IP     string    `json:"ip,omitempty"`
	Msg    string    `json:"msg,omitempty"`
}

// RuleInput 是新建规则请求体。
type RuleInput struct {
	DeviceID   string `json:"device_id"`
	Name       string `json:"name"`
	Proto      string `json:"proto"`
	LocalAddr  string `json:"local_addr"`
	RemotePort int    `json:"remote_port"`
	Domain     string `json:"domain"`
	Enabled    *bool  `json:"enabled"`
}

// RulePatch 是修改规则请求体（nil 表示不改）。
type RulePatch struct {
	Name       *string `json:"name"`
	LocalAddr  *string `json:"local_addr"`
	RemotePort *int    `json:"remote_port"`
	Domain     *string `json:"domain"`
	Enabled    *bool   `json:"enabled"`
}

// SettingsPatch 是修改设置请求体（nil 表示不改）。
type SettingsPatch struct {
	PublicHost          *string `json:"public_host"`
	PortRange           *[2]int `json:"port_range"`
	HTTPAddr            *string `json:"http_addr"`
	DeviceAutoApprove   *bool   `json:"device_auto_approve"`
	MaxDevices          *int    `json:"max_devices"`
	MaxRulesPerDevice   *int    `json:"max_rules_per_device"`
	AdminSessionTTLSec  *int    `json:"admin_session_ttl_sec"`
	TrafficStatsEnabled *bool   `json:"traffic_stats_enabled"`
}

// Backend 是管理台依赖的服务端能力，由 internal/server.Server 实现。
type Backend interface {
	Overview() Overview
	Devices() []DeviceView
	ApproveDevice(id, name, actor, ip string) (*DeviceView, error)
	RejectDevice(id, actor, ip string) error
	SetDeviceBlocked(id string, blocked bool, actor, ip string) (*DeviceView, error)
	UpdateDevice(id string, name, note *string, actor, ip string) (*DeviceView, error)
	RebindDevice(id, actor, ip string) (*DeviceView, error)
	DeleteDevice(id, actor, ip string) error

	Rules(deviceID string) []RuleView
	CreateRule(in RuleInput, actor, ip string) (*RuleView, error)
	UpdateRule(id string, in RulePatch, actor, ip string) (*RuleView, error)
	DeleteRule(id, actor, ip string) error
	CheckPort(port int) (bool, string)

	GetSettings() store.Settings
	UpdateSettings(in SettingsPatch, actor, ip string) (store.Settings, error)
	Logs(kind string, limit int) []LogEntry
	Metrics() string

	VerifyLogin(username, password, ip string) (AdminView, bool)
	GetAdmin(id string) (AdminView, bool)
	ChangePassword(adminID, oldPassword, newPassword string) error
}

// Server 是管理台 HTTP 处理器。
type Server struct {
	be           Backend
	log          *slog.Logger
	sessions     *sessionStore
	loginLimiter *ipLimiter
	handler      http.Handler
}

// New 创建管理台。
func New(be Backend, log *slog.Logger) *Server {
	s := &Server{
		be:           be,
		log:          log,
		sessions:     newSessionStore(),
		loginLimiter: newIPLimiter(10, time.Minute),
	}
	s.handler = s.routes()
	return s
}

// Handler 返回 http.Handler。
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.auth(s.handleLogout))
	mux.HandleFunc("GET /api/v1/me", s.auth(s.handleMe))
	mux.HandleFunc("POST /api/v1/auth/password", s.auth(s.handleChangePassword))

	mux.HandleFunc("GET /api/v1/overview", s.auth(s.handleOverview))

	mux.HandleFunc("GET /api/v1/devices", s.auth(s.handleDevices))
	mux.HandleFunc("POST /api/v1/devices/{id}/approve", s.auth(s.handleApproveDevice))
	mux.HandleFunc("POST /api/v1/devices/{id}/reject", s.auth(s.handleRejectDevice))
	mux.HandleFunc("POST /api/v1/devices/{id}/block", s.auth(s.handleBlockDevice))
	mux.HandleFunc("POST /api/v1/devices/{id}/rebind", s.auth(s.handleRebindDevice))
	mux.HandleFunc("PATCH /api/v1/devices/{id}", s.auth(s.handleUpdateDevice))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.auth(s.handleDeleteDevice))

	mux.HandleFunc("GET /api/v1/rules", s.auth(s.handleRules))
	mux.HandleFunc("POST /api/v1/rules", s.auth(s.handleCreateRule))
	mux.HandleFunc("PATCH /api/v1/rules/{id}", s.auth(s.handleUpdateRule))
	mux.HandleFunc("DELETE /api/v1/rules/{id}", s.auth(s.handleDeleteRule))
	mux.HandleFunc("GET /api/v1/rules/check-port", s.auth(s.handleCheckPort))

	mux.HandleFunc("GET /api/v1/logs", s.auth(s.handleLogs))
	mux.HandleFunc("GET /api/v1/settings", s.auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/v1/settings", s.auth(s.handleUpdateSettings))

	mux.HandleFunc("GET /", s.serveStatic)
	return s.withCommonHeaders(mux)
}

func (s *Server) withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// ---------------- 认证中间件 ----------------

type ctxKey int

const ctxSession ctxKey = 1

// auth 包装需要登录的处理器，并做 CSRF 校验。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.currentSession(r)
		if !ok {
			writeErr(w, ErrUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-NRP-CSRF") == "" || r.Header.Get("X-NRP-CSRF") != sess.csrf {
				writeErr(w, NewErr(http.StatusForbidden, "csrf_invalid", "CSRF 校验失败，请刷新页面"))
				return
			}
		}
		next(w, r.WithContext(withSession(r.Context(), sess)))
	}
}

func (s *Server) currentSession(r *http.Request) (*adminSession, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	ttl := time.Duration(s.be.GetSettings().AdminSessionTTLSec) * time.Second
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return s.sessions.get(c.Value, ttl)
}

// ---------------- 会话存储 ----------------

const cookieName = "nrp_admin"

type adminSession struct {
	adminID  string
	username string
	csrf     string
	expires  time.Time
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]*adminSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: map[string]*adminSession{}}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *sessionStore) create(adminID, username string, ttl time.Duration) (token string, sess *adminSession) {
	token = randomHex(32)
	sess = &adminSession{adminID: adminID, username: username, csrf: randomHex(32), expires: time.Now().Add(ttl)}
	s.mu.Lock()
	s.m[token] = sess
	s.mu.Unlock()
	return token, sess
}

func (s *sessionStore) get(token string, ttl time.Duration) (*adminSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.expires) {
		delete(s.m, token)
		return nil, false
	}
	// 滑动续期。
	sess.expires = time.Now().Add(ttl)
	return sess, true
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// cleanup 清理已过期的会话（登录时顺手调用，避免长期运行会话表无限增长）。
func (s *sessionStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.m {
		if now.After(v.expires) {
			delete(s.m, k)
		}
	}
}

func (s *sessionStore) deleteByAdmin(adminID string) {
	s.mu.Lock()
	for k, v := range s.m {
		if v.adminID == adminID {
			delete(s.m, k)
		}
	}
	s.mu.Unlock()
}

// ---------------- 静态资源 ----------------

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		http.Error(w, "static unavailable", http.StatusInternalServerError)
		return
	}
	clean := strings.TrimPrefix(r.URL.Path, "/")
	if clean == "" {
		clean = "index.html"
	}
	if strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}
	// API 的未知路径必须返回 JSON 404，不能回落成 SPA 的 index.html。
	if strings.HasPrefix(clean, "api/") {
		writeErr(w, ErrNotFound)
		return
	}
	if f, err := sub.Open(clean); err == nil {
		_ = f.Close()
		// 管理台前端只有内嵌的三个小文件；不缓存，保证升级后刷新即可拿到新版本。
		w.Header().Set("Cache-Control", "no-cache")
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
		return
	}
	// 单页应用：未知路径回落到 index.html（仅无扩展名时）。
	if !strings.Contains(clean, ".") {
		w.Header().Set("Cache-Control", "no-cache")
		data, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	http.NotFound(w, r)
}

// ---------------- 通用辅助 ----------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var e *Err
	if errors.As(err, &e) {
		writeJSON(w, e.Status, map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]string{"code": "internal", "message": "服务端内部错误"}})
}

// readJSONOptional 解析可选 JSON 请求体（空 body 合法）。
func readJSONOptional(r *http.Request, v any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return NewErr(http.StatusBadRequest, "invalid_request", "读取请求体失败")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return NewErr(http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON：%v", err)
	}
	return nil
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return NewErr(http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON：%v", err)
	}
	return nil
}

// clientIP 返回真实来源 IP。若请求来自本机（通常意味着经过 nginx/SSH 隧道反代），
// 信任反向代理注入的 X-Real-IP / X-Forwarded-For，否则直接用 RemoteAddr。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isLoopbackHost(host) {
		return host
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		parts := strings.Split(v, ",")
		if first := strings.TrimSpace(parts[0]); first != "" {
			return first
		}
	}
	return host
}

// isHTTPS 判断浏览器侧是否为 HTTPS（直接 TLS，或来自本机反代的 X-Forwarded-Proto）。
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if isLoopbackHost(host) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ipLimiter 是简单的按 IP 令牌桶（用于登录/认证限流）。
type ipLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, buckets: map[string]*bucket{}}
}

func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok || now.After(b.reset) {
		l.buckets[ip] = &bucket{count: 1, reset: now.Add(l.window)}
		if len(l.buckets) > 10000 {
			for k, v := range l.buckets {
				if now.After(v.reset) {
					delete(l.buckets, k)
				}
			}
		}
		return true
	}
	b.count++
	return b.count <= l.limit
}
