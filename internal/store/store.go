package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 常见错误。
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

const (
	stateFile    = "state.json"
	auditFile    = "audit.jsonl"
	auditRingLen = 1000
)

type diskData struct {
	Version        int       `json:"version"`
	Devices        []*Device `json:"devices"`
	Rules          []*Rule   `json:"rules"`
	Admins         []*Admin  `json:"admins"`
	Settings       Settings  `json:"settings"`
	ConfigRevision int64     `json:"config_revision"`
	// RevokedKeys 是已吊销的公钥（删除设备时写入），用于阻止已泄露私钥重新登记。
	RevokedKeys []string `json:"revoked_keys,omitempty"`
}

// Store 提供带互斥锁的配置存储。
type Store struct {
	mu    sync.RWMutex
	dir   string
	path  string
	audit string
	d     diskData
	ring  []AuditEntry
}

// Open 打开（或初始化）数据目录。
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("store: empty data dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}
	s := &Store{
		dir:   dir,
		path:  filepath.Join(dir, stateFile),
		audit: filepath.Join(dir, auditFile),
	}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, fmt.Errorf("store: parse %s: %w", s.path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("store: read %s: %w", s.path, err)
	}
	if s.d.Version == 0 {
		s.d.Version = 1
	}
	if s.d.Settings == (Settings{}) {
		s.d.Settings = DefaultSettings()
	}
	if s.d.Settings.AdminSessionTTLSec <= 0 {
		s.d.Settings.AdminSessionTTLSec = DefaultSettings().AdminSessionTTLSec
	}
	if s.d.Settings.MaxDevices <= 0 {
		s.d.Settings.MaxDevices = DefaultSettings().MaxDevices
	}
	if s.d.Settings.MaxRulesPerDevice <= 0 {
		s.d.Settings.MaxRulesPerDevice = DefaultSettings().MaxRulesPerDevice
	}
	if s.d.Settings.PortRange[0] == 0 {
		s.d.Settings.PortRange = DefaultSettings().PortRange
	}
	s.loadAuditTail()
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Dir 返回数据目录。
func (s *Store) Dir() string { return s.dir }

// Close 目前无资源需要释放。
func (s *Store) Close() error { return nil }

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(&s.d, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}
	b = append(b, '\n')
	return atomicWrite(s.path, b, 0o600)
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-state-*")
	if err != nil {
		return fmt.Errorf("store: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("store: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("store: chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("store: rename: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// NewID 生成带前缀的随机 ID。
func NewID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b)
}

func cloneDevice(d *Device) Device { return *d }
func cloneRule(r *Rule) Rule       { return *r }

// ---------------- 设备 ----------------

// ListDevices 返回全部设备副本（按创建时间倒序）。
func (s *Store) ListDevices() []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Device, 0, len(s.d.Devices))
	for i := len(s.d.Devices) - 1; i >= 0; i-- {
		out = append(out, cloneDevice(s.d.Devices[i]))
	}
	return out
}

// GetDevice 按 ID 查找设备。
func (s *Store) GetDevice(id string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Devices {
		if d.ID == id {
			return cloneDevice(d), true
		}
	}
	return Device{}, false
}

// GetDeviceByPublicKey 按公钥查找设备。
func (s *Store) GetDeviceByPublicKey(pub string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.d.Devices {
		if d.PublicKey == pub {
			return cloneDevice(d), true
		}
	}
	return Device{}, false
}

// CountDevices 返回设备总数。
func (s *Store) CountDevices() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.d.Devices)
}

// CountDevicesByStatus 统计某状态设备数。
func (s *Store) CountDevicesByStatus(status string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, d := range s.d.Devices {
		if d.Status == status {
			n++
		}
	}
	return n
}

// AddDevice 新建设备（ID/CreatedAt 为空时自动填充）。
func (s *Store) AddDevice(d *Device) (*Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.d.Devices {
		if x.PublicKey == d.PublicKey {
			return nil, ErrConflict
		}
	}
	cp := cloneDevice(d)
	if cp.ID == "" {
		cp.ID = NewID("dev_")
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	if cp.Status == "" {
		cp.Status = StatusPending
	}
	if cp.Name == "" {
		cp.Name = cp.Hostname
	}
	s.d.Devices = append(s.d.Devices, &cp)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	out := cp
	return &out, nil
}

// UpdateDevice 用 fn 修改设备并保存，返回更新后的副本。
func (s *Store) UpdateDevice(id string, fn func(*Device) error) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.d.Devices {
		if d.ID != id {
			continue
		}
		if err := fn(d); err != nil {
			return Device{}, err
		}
		if err := s.saveLocked(); err != nil {
			return Device{}, err
		}
		return cloneDevice(d), nil
	}
	return Device{}, ErrNotFound
}

// DeleteDevice 删除设备及其全部规则。
func (s *Store) DeleteDevice(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, d := range s.d.Devices {
		if d.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	// 删除即吊销：把公钥加入黑名单，避免同一私钥重新登记成新设备。
	revoked := s.d.Devices[idx].PublicKey
	already := false
	for _, k := range s.d.RevokedKeys {
		if k == revoked {
			already = true
			break
		}
	}
	if !already && revoked != "" {
		s.d.RevokedKeys = append(s.d.RevokedKeys, revoked)
	}
	s.d.Devices = append(s.d.Devices[:idx], s.d.Devices[idx+1:]...)
	rules := s.d.Rules[:0]
	for _, r := range s.d.Rules {
		if r.DeviceID != id {
			rules = append(rules, r)
		}
	}
	s.d.Rules = rules
	return s.saveLocked()
}

// ---------------- 规则 ----------------

// ListRules 返回规则副本（deviceID 为空表示全部）。
func (s *Store) ListRules(deviceID string) []Rule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Rule, 0, len(s.d.Rules))
	for _, r := range s.d.Rules {
		if deviceID != "" && r.DeviceID != deviceID {
			continue
		}
		out = append(out, cloneRule(r))
	}
	return out
}

// GetRule 按 ID 查找规则。
func (s *Store) GetRule(id string) (Rule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.d.Rules {
		if r.ID == id {
			return cloneRule(r), true
		}
	}
	return Rule{}, false
}

// AddRule 新建规则（在锁内再次校验端口/域名唯一，避免并发预检竞态）。
func (s *Store) AddRule(r *Rule) (*Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.d.Rules {
		if r.Proto == ProtoTCP && existing.Proto == ProtoTCP && existing.RemotePort == r.RemotePort && r.RemotePort != 0 {
			return nil, ErrConflict
		}
		if r.Proto == ProtoHTTP && existing.Proto == ProtoHTTP && equalFold(existing.Domain, r.Domain) && r.Domain != "" {
			return nil, ErrConflict
		}
	}
	cp := cloneRule(r)
	if cp.ID == "" {
		cp.ID = NewID("rule_")
	}
	now := time.Now().UTC()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	s.d.Rules = append(s.d.Rules, &cp)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	out := cp
	return &out, nil
}

// UpdateRule 修改规则并保存。
func (s *Store) UpdateRule(id string, fn func(*Rule) error) (Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.d.Rules {
		if r.ID != id {
			continue
		}
		if err := fn(r); err != nil {
			return Rule{}, err
		}
		r.UpdatedAt = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			return Rule{}, err
		}
		return cloneRule(r), nil
	}
	return Rule{}, ErrNotFound
}

// DeleteRule 删除规则。
func (s *Store) DeleteRule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, r := range s.d.Rules {
		if r.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	s.d.Rules = append(s.d.Rules[:idx], s.d.Rules[idx+1:]...)
	return s.saveLocked()
}

// PortInUse 判断公网端口是否已被其它 TCP 规则占用。
func (s *Store) PortInUse(port int, excludeRuleID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.d.Rules {
		if r.Proto == ProtoTCP && r.RemotePort == port && r.ID != excludeRuleID {
			return true
		}
	}
	return false
}

// DomainInUse 判断域名是否已被其它 HTTP 规则占用（大小写不敏感）。
func (s *Store) DomainInUse(domain, excludeRuleID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.d.Rules {
		if r.Proto == ProtoHTTP && equalFold(r.Domain, domain) && r.ID != excludeRuleID {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// IsKeyRevoked 判断公钥是否已被吊销。
func (s *Store) IsKeyRevoked(pub string) bool {
	if pub == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.d.RevokedKeys {
		if k == pub {
			return true
		}
	}
	return false
}

// ---------------- 管理员 ----------------

// AdminCount 返回管理员数量。
func (s *Store) AdminCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.d.Admins)
}

// AddAdmin 新建管理员。
func (s *Store) AddAdmin(username, passwordHash string) (*Admin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.d.Admins {
		if a.Username == username {
			return nil, ErrConflict
		}
	}
	a := &Admin{
		ID:                 NewID("adm_"),
		Username:           username,
		PasswordHash:       passwordHash,
		MustChangePassword: true,
		CreatedAt:          time.Now().UTC(),
	}
	s.d.Admins = append(s.d.Admins, a)
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	out := *a
	return &out, nil
}

// GetAdminByUsername 按用户名查找。
func (s *Store) GetAdminByUsername(username string) (Admin, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.d.Admins {
		if a.Username == username {
			return *a, true
		}
	}
	return Admin{}, false
}

// GetAdmin 按 ID 查找。
func (s *Store) GetAdmin(id string) (Admin, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.d.Admins {
		if a.ID == id {
			return *a, true
		}
	}
	return Admin{}, false
}

// UpdateAdmin 用 fn 修改管理员并保存。
func (s *Store) UpdateAdmin(id string, fn func(*Admin) error) (Admin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.d.Admins {
		if a.ID != id {
			continue
		}
		if err := fn(a); err != nil {
			return Admin{}, err
		}
		if err := s.saveLocked(); err != nil {
			return Admin{}, err
		}
		return *a, nil
	}
	return Admin{}, ErrNotFound
}

// ---------------- 设置 / 版本 ----------------

// Settings 返回设置副本。
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d.Settings
}

// UpdateSettings 用 fn 修改设置并保存。
func (s *Store) UpdateSettings(fn func(*Settings) error) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.d.Settings); err != nil {
		return Settings{}, err
	}
	if err := s.saveLocked(); err != nil {
		return Settings{}, err
	}
	return s.d.Settings, nil
}

// ConfigRevision 返回当前配置版本号。
func (s *Store) ConfigRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.d.ConfigRevision
}

// BumpConfigRevision 自增配置版本号并持久化。
func (s *Store) BumpConfigRevision() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d.ConfigRevision++
	if err := s.saveLocked(); err != nil {
		// 版本号只影响 Agent 缓存刷新判断，失败不阻塞主流程。
		_ = err
	}
	return s.d.ConfigRevision
}

// ---------------- 审计 ----------------

// AppendAudit 写入审计（内存环形缓冲 + JSONL 文件）。
func (s *Store) AppendAudit(e AuditEntry) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	s.mu.Lock()
	s.ring = append(s.ring, e)
	if len(s.ring) > auditRingLen {
		s.ring = s.ring[len(s.ring)-auditRingLen:]
	}
	s.mu.Unlock()

	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.audit, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// RecentAudit 返回最近 n 条审计（新的在前）。
func (s *Store) RecentAudit(n int) []AuditEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n <= 0 || n > len(s.ring) {
		n = len(s.ring)
	}
	out := make([]AuditEntry, 0, n)
	for i := len(s.ring) - 1; i >= len(s.ring)-n; i-- {
		out = append(out, s.ring[i])
	}
	return out
}

func (s *Store) loadAuditTail() {
	f, err := os.Open(s.audit)
	if err != nil {
		return
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var all []AuditEntry
	for {
		var e AuditEntry
		if err := dec.Decode(&e); err != nil {
			break
		}
		all = append(all, e)
	}
	if len(all) > auditRingLen {
		all = all[len(all)-auditRingLen:]
	}
	s.ring = all
}
