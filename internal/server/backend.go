package server

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/liuhuo23/queqiao/internal/passwd"
	"github.com/liuhuo23/queqiao/internal/protocol"
	"github.com/liuhuo23/queqiao/internal/store"
	"github.com/liuhuo23/queqiao/internal/version"
	"github.com/liuhuo23/queqiao/internal/web"
)

// 编译期断言：Server 实现管理台所需的后端能力。
var _ web.Backend = (*Server)(nil)

// ---------------- 概览 ----------------

// Overview 汇总服务端状态。
func (s *Server) Overview() web.Overview {
	settings := s.store.Settings()
	devices := s.store.ListDevices()
	rules := s.store.ListRules("")
	online := s.sessions.onlineIDs()

	pending := 0
	for _, d := range devices {
		if d.Status == store.StatusPending {
			pending++
		}
	}
	active := 0
	for _, r := range rules {
		if r.Enabled && online[r.DeviceID] {
			active++
		}
	}
	return web.Overview{
		Server: web.ServerInfo{
			Version:     version.String(),
			UptimeSec:   int64(s.Uptime().Seconds()),
			ControlAddr: s.cfg.ControlAddr,
			WebAddr:     s.cfg.WebAddr,
			PublicHost:  settings.PublicHost,
			PortRange:   settings.PortRange,
			HTTPAddr:    settings.HTTPAddr,
		},
		Counts: web.Counts{
			DevicesTotal:   len(devices),
			DevicesOnline:  len(online),
			DevicesPending: pending,
			RulesTotal:     len(rules),
			RulesActive:    active,
		},
		Traffic: web.Traffic{
			BytesIn:      s.stats.bytesIn.Load(),
			BytesOut:     s.stats.bytesOut.Load(),
			ConnsCurrent: s.stats.connsCurrent.Load(),
			ConnsTotal:   s.stats.connsTotal.Load(),
		},
	}
}

// ---------------- 设备 ----------------

// Devices 返回设备列表（合并在线状态）。
func (s *Server) Devices() []web.DeviceView {
	devices := s.store.ListDevices()
	sessions := s.sessions.snapshot()
	ruleCount := map[string]int{}
	for _, r := range s.store.ListRules("") {
		ruleCount[r.DeviceID]++
	}
	out := make([]web.DeviceView, 0, len(devices))
	for _, d := range devices {
		v := web.DeviceView{Device: d, RuleCount: ruleCount[d.ID]}
		if sess, ok := sessions[d.ID]; ok {
			v.Online = true
			since := sess.createdAt
			v.OnlineSince = &since
			v.LastSeenSec = int64(time.Since(time.Unix(sess.lastSeen.Load(), 0)).Seconds())
		} else if d.LastSeenAt != nil {
			v.LastSeenSec = int64(time.Since(*d.LastSeenAt).Seconds())
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) deviceView(d store.Device) web.DeviceView {
	ruleCount := len(s.store.ListRules(d.ID))
	v := web.DeviceView{Device: d, RuleCount: ruleCount}
	if sess := s.sessions.get(d.ID); sess != nil {
		v.Online = true
		since := sess.createdAt
		v.OnlineSince = &since
		v.LastSeenSec = int64(time.Since(time.Unix(sess.lastSeen.Load(), 0)).Seconds())
	} else if d.LastSeenAt != nil {
		v.LastSeenSec = int64(time.Since(*d.LastSeenAt).Seconds())
	}
	return v
}

// ApproveDevice 批准待审批设备。
func (s *Server) ApproveDevice(id, name, actor, ip string) (*web.DeviceView, error) {
	dev, ok := s.store.GetDevice(id)
	if !ok {
		return nil, web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
	}
	if dev.Status != store.StatusPending {
		return nil, web.NewErr(http.StatusConflict, "invalid_state", "只有待审批设备可以批准")
	}
	now := time.Now().UTC()
	updated, err := s.store.UpdateDevice(id, func(d *store.Device) error {
		d.Status = store.StatusActive
		d.ApprovedAt = &now
		d.LastError = ""
		d.FingerprintConflict = false
		if strings.TrimSpace(name) != "" {
			d.Name = strings.TrimSpace(name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.store.BumpConfigRevision()
	s.pushConfig(id)
	s.tunnels.reconcileAll()
	s.audit(actor, "device.approve", id, updated.Name, ip, "info")
	v := s.deviceView(updated)
	return &v, nil
}

// RejectDevice 拒绝并删除待审批设备。
func (s *Server) RejectDevice(id, actor, ip string) error {
	dev, ok := s.store.GetDevice(id)
	if !ok {
		return web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
	}
	if dev.Status != store.StatusPending {
		return web.NewErr(http.StatusConflict, "invalid_state", "只有待审批设备可以拒绝")
	}
	if err := s.store.DeleteDevice(id); err != nil {
		return err
	}
	s.tunnels.reconcileAll()
	s.audit(actor, "device.reject", id, dev.Name, ip, "warn")
	return nil
}

// SetDeviceBlocked 封禁/解封设备。
func (s *Server) SetDeviceBlocked(id string, blocked bool, actor, ip string) (*web.DeviceView, error) {
	dev, ok := s.store.GetDevice(id)
	if !ok {
		return nil, web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
	}
	// 解封只能用于已封禁设备，不能借“解封”把 pending 设备直接变成 active。
	if !blocked && dev.Status != store.StatusBlocked {
		return nil, web.NewErr(http.StatusConflict, "invalid_state", "只有已封禁设备可以解封")
	}
	updated, err := s.store.UpdateDevice(id, func(d *store.Device) error {
		if blocked {
			d.Status = store.StatusBlocked
		} else {
			d.Status = store.StatusActive
		}
		return nil
	})
	if err != nil {
		if err == store.ErrNotFound {
			return nil, web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
		}
		return nil, err
	}
	if blocked {
		s.sessions.kickDevice(id, "管理员封禁设备")
	}
	s.tunnels.reconcileAll()
	action := "device.unblock"
	if blocked {
		action = "device.block"
	}
	s.audit(actor, action, id, updated.Name, ip, "warn")
	v := s.deviceView(updated)
	return &v, nil
}

// UpdateDevice 修改设备显示名/备注。
func (s *Server) UpdateDevice(id string, name, note *string, actor, ip string) (*web.DeviceView, error) {
	updated, err := s.store.UpdateDevice(id, func(d *store.Device) error {
		if name != nil {
			n := strings.TrimSpace(*name)
			if n == "" {
				return fmt.Errorf("名称不能为空")
			}
			d.Name = truncate(n, 64)
		}
		if note != nil {
			d.Note = truncate(strings.TrimSpace(*note), 512)
		}
		return nil
	})
	if err != nil {
		if err == store.ErrNotFound {
			return nil, web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
		}
		return nil, web.NewErr(http.StatusBadRequest, "invalid_request", "%v", err)
	}
	s.audit(actor, "device.update", id, updated.Name, ip, "info")
	v := s.deviceView(updated)
	return &v, nil
}

// RebindDevice 清空机器指纹，要求下次连接重新登记。
func (s *Server) RebindDevice(id, actor, ip string) (*web.DeviceView, error) {
	updated, err := s.store.UpdateDevice(id, func(d *store.Device) error {
		d.MachineID = ""
		d.FingerprintConflict = false
		d.LastError = ""
		d.Status = store.StatusPending
		return nil
	})
	if err != nil {
		if err == store.ErrNotFound {
			return nil, web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
		}
		return nil, err
	}
	s.sessions.kickDevice(id, "管理员重绑定机器")
	s.tunnels.reconcileAll()
	s.audit(actor, "device.rebind", id, updated.Name, ip, "warn")
	v := s.deviceView(updated)
	return &v, nil
}

// DeleteDevice 删除设备及其规则。
func (s *Server) DeleteDevice(id, actor, ip string) error {
	dev, ok := s.store.GetDevice(id)
	if !ok {
		return web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
	}
	s.sessions.kickDevice(id, "设备已被删除")
	if err := s.store.DeleteDevice(id); err != nil {
		return err
	}
	s.store.BumpConfigRevision()
	s.tunnels.reconcileAll()
	s.audit(actor, "device.delete", id, dev.Name, ip, "warn")
	return nil
}

// ---------------- 规则 ----------------

// Rules 返回规则列表。
func (s *Server) Rules(deviceID string) []web.RuleView {
	rules := s.store.ListRules(deviceID)
	names := map[string]string{}
	for _, d := range s.store.ListDevices() {
		names[d.ID] = d.Name
	}
	out := make([]web.RuleView, 0, len(rules))
	for _, r := range rules {
		out = append(out, s.ruleView(r, names))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeviceName == out[j].DeviceName {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].DeviceName < out[j].DeviceName
	})
	return out
}

func (s *Server) ruleView(r store.Rule, names map[string]string) web.RuleView {
	settings := s.store.Settings()
	state := "active"
	errMsg := s.tunnels.ruleError(r.ID)
	if errMsg == "" && r.Proto == store.ProtoHTTP && r.Enabled {
		errMsg = s.tunnels.HTTPError()
	}
	switch {
	case !r.Enabled:
		state = "disabled"
	case !s.sessions.online(r.DeviceID):
		state = "device_offline"
	case errMsg != "":
		state = "error"
	}
	publicAddr := ""
	host := settings.PublicHost
	if host == "" {
		host = s.cfg.PublicHost
	}
	switch r.Proto {
	case store.ProtoTCP:
		publicAddr = net.JoinHostPort(host, strconv.Itoa(r.RemotePort))
	case store.ProtoHTTP:
		publicAddr = r.Domain
	}
	in, out, cur, total := s.tunnels.ruleStats(r.ID)
	return web.RuleView{
		Rule:         r,
		DeviceName:   names[r.DeviceID],
		State:        state,
		Error:        errMsg,
		PublicAddr:   publicAddr,
		BytesIn:      in,
		BytesOut:     out,
		ConnsCurrent: cur,
		ConnsTotal:   total,
	}
}

// CreateRule 新建映射。
func (s *Server) CreateRule(in web.RuleInput, actor, ip string) (*web.RuleView, error) {
	dev, ok := s.store.GetDevice(in.DeviceID)
	if !ok {
		return nil, web.NewErr(http.StatusNotFound, "device_not_found", "设备不存在")
	}
	settings := s.store.Settings()
	if len(s.store.ListRules(in.DeviceID)) >= settings.MaxRulesPerDevice {
		return nil, web.NewErr(http.StatusConflict, "rule_limit", "该设备映射数量已达上限 %d", settings.MaxRulesPerDevice)
	}
	proto := strings.ToLower(strings.TrimSpace(in.Proto))
	if proto == "" {
		proto = store.ProtoTCP
	}
	localAddr := strings.TrimSpace(in.LocalAddr)
	if err := validateLocalAddr(localAddr); err != nil {
		return nil, web.NewErr(http.StatusBadRequest, "invalid_local_addr", "%v", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = localAddr
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	r := store.Rule{
		DeviceID:  dev.ID,
		Name:      truncate(name, 64),
		Proto:     proto,
		LocalAddr: localAddr,
		Enabled:   enabled,
	}
	switch proto {
	case store.ProtoTCP:
		if in.RemotePort < settings.PortRange[0] || in.RemotePort > settings.PortRange[1] {
			return nil, web.NewErr(http.StatusBadRequest, "port_out_of_range",
				"公网端口必须在 %d-%d 之间", settings.PortRange[0], settings.PortRange[1])
		}
		if ok, reason := s.CheckPort(in.RemotePort); !ok {
			return nil, web.NewErr(http.StatusConflict, "port_in_use", "%s", reason)
		}
		r.RemotePort = in.RemotePort
	case store.ProtoHTTP:
		domain := normalizeDomain(in.Domain)
		if domain == "" {
			return nil, web.NewErr(http.StatusBadRequest, "invalid_domain", "HTTP 映射需要填写域名")
		}
		if s.store.DomainInUse(domain, "") {
			return nil, web.NewErr(http.StatusConflict, "domain_taken", "域名已被其他映射占用")
		}
		r.Domain = domain
	default:
		return nil, web.NewErr(http.StatusBadRequest, "invalid_proto", "协议只支持 tcp 或 http")
	}

	created, err := s.store.AddRule(&r)
	if err != nil {
		return nil, err
	}
	s.store.BumpConfigRevision()
	s.pushConfig(dev.ID)
	s.tunnels.reconcileAll()
	s.audit(actor, "rule.create", created.ID, fmt.Sprintf("%s %s", created.Proto, created.LocalAddr), ip, "info")
	names := map[string]string{dev.ID: dev.Name}
	v := s.ruleView(*created, names)
	return &v, nil
}

// UpdateRule 修改映射。
func (s *Server) UpdateRule(id string, in web.RulePatch, actor, ip string) (*web.RuleView, error) {
	rule, ok := s.store.GetRule(id)
	if !ok {
		return nil, web.NewErr(http.StatusNotFound, "rule_not_found", "映射不存在")
	}
	settings := s.store.Settings()
	candidate := rule
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" {
			return nil, web.NewErr(http.StatusBadRequest, "invalid_request", "名称不能为空")
		}
		candidate.Name = truncate(n, 64)
	}
	if in.LocalAddr != nil {
		la := strings.TrimSpace(*in.LocalAddr)
		if err := validateLocalAddr(la); err != nil {
			return nil, web.NewErr(http.StatusBadRequest, "invalid_local_addr", "%v", err)
		}
		candidate.LocalAddr = la
	}
	if in.Enabled != nil {
		candidate.Enabled = *in.Enabled
	}
	if in.RemotePort != nil {
		if candidate.Proto != store.ProtoTCP {
			return nil, web.NewErr(http.StatusBadRequest, "invalid_request", "HTTP 映射不支持公网端口")
		}
		p := *in.RemotePort
		if p != rule.RemotePort {
			if p < settings.PortRange[0] || p > settings.PortRange[1] {
				return nil, web.NewErr(http.StatusBadRequest, "port_out_of_range",
					"公网端口必须在 %d-%d 之间", settings.PortRange[0], settings.PortRange[1])
			}
			if ok, reason := s.CheckPort(p); !ok {
				return nil, web.NewErr(http.StatusConflict, "port_in_use", "%s", reason)
			}
		}
		candidate.RemotePort = p
	}
	if in.Domain != nil {
		if candidate.Proto != store.ProtoHTTP {
			return nil, web.NewErr(http.StatusBadRequest, "invalid_request", "TCP 映射不支持域名")
		}
		domain := normalizeDomain(*in.Domain)
		if domain == "" {
			return nil, web.NewErr(http.StatusBadRequest, "invalid_domain", "域名不能为空")
		}
		if !strings.EqualFold(domain, rule.Domain) && s.store.DomainInUse(domain, id) {
			return nil, web.NewErr(http.StatusConflict, "domain_taken", "域名已被其他映射占用")
		}
		candidate.Domain = domain
	}

	updated, err := s.store.UpdateRule(id, func(r *store.Rule) error {
		r.Name = candidate.Name
		r.LocalAddr = candidate.LocalAddr
		r.Enabled = candidate.Enabled
		r.RemotePort = candidate.RemotePort
		r.Domain = candidate.Domain
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.store.BumpConfigRevision()
	s.pushConfig(updated.DeviceID)
	s.tunnels.reconcileAll()
	s.audit(actor, "rule.update", id, fmt.Sprintf("%s %s", updated.Proto, updated.LocalAddr), ip, "info")
	names := map[string]string{}
	if dev, ok := s.store.GetDevice(updated.DeviceID); ok {
		names[dev.ID] = dev.Name
	}
	v := s.ruleView(updated, names)
	return &v, nil
}

// DeleteRule 删除映射。
func (s *Server) DeleteRule(id, actor, ip string) error {
	rule, ok := s.store.GetRule(id)
	if !ok {
		return web.NewErr(http.StatusNotFound, "rule_not_found", "映射不存在")
	}
	if err := s.store.DeleteRule(id); err != nil {
		return err
	}
	s.store.BumpConfigRevision()
	s.pushConfig(rule.DeviceID)
	s.tunnels.reconcileAll()
	s.audit(actor, "rule.delete", id, fmt.Sprintf("%s %s", rule.Proto, rule.LocalAddr), ip, "info")
	return nil
}

// CheckPort 检查公网端口是否可用。
func (s *Server) CheckPort(port int) (bool, string) {
	if port < 1024 || port > 65535 {
		return false, "端口需在 1024-65535 之间"
	}
	settings := s.store.Settings()
	if port < settings.PortRange[0] || port > settings.PortRange[1] {
		return false, fmt.Sprintf("端口不在允许范围 %d-%d", settings.PortRange[0], settings.PortRange[1])
	}
	if port == portOf(s.cfg.ControlAddr) || port == portOf(s.cfg.WebAddr) || port == portOf(settings.HTTPAddr) {
		return false, "端口被 鹊桥 自身占用"
	}
	if s.store.PortInUse(port, "") {
		return false, "端口已被其他映射占用"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(s.cfg.PublicBindAddr, strconv.Itoa(port)))
	if err != nil {
		return false, "端口在本机不可用（可能被系统进程占用）"
	}
	_ = ln.Close()
	return true, ""
}

// ---------------- 配置下发 ----------------

// rulesForDevice 返回下发给某设备的规则快照。
func (s *Server) rulesForDevice(deviceID string) []protocol.RuleSpec {
	rules := s.store.ListRules(deviceID)
	out := make([]protocol.RuleSpec, 0, len(rules))
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		out = append(out, protocol.RuleSpec{
			ID:         r.ID,
			Name:       r.Name,
			Proto:      r.Proto,
			LocalAddr:  r.LocalAddr,
			RemotePort: r.RemotePort,
			Domain:     r.Domain,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Server) pushConfig(deviceID string) {
	sess := s.sessions.get(deviceID)
	if sess == nil {
		return
	}
	rev := s.store.ConfigRevision()
	rules := s.rulesForDevice(deviceID)
	sess.setRules(rules)
	if err := sess.send(&protocol.Config{Type: protocol.TypeConfig, Revision: rev, Rules: rules}); err != nil {
		s.log.Warn("推送配置失败", "device_id", deviceID, "err", err)
	}
}

// ---------------- 设置 ----------------

// GetSettings 返回全局设置。
func (s *Server) GetSettings() store.Settings { return s.store.Settings() }

// UpdateSettings 修改全局设置。
func (s *Server) UpdateSettings(in web.SettingsPatch, actor, ip string) (store.Settings, error) {
	// http_addr 变更前先验证能绑定，避免“保存成功但监听失败”。
	if in.HTTPAddr != nil {
		addr := strings.TrimSpace(*in.HTTPAddr)
		if addr != "" {
			if err := validateListenAddr(addr); err != nil {
				return store.Settings{}, web.NewErr(http.StatusBadRequest, "invalid_settings", "%v", err)
			}
			if addr != s.store.Settings().HTTPAddr {
				ln, err := net.Listen("tcp", addr)
				if err != nil {
					return store.Settings{}, web.NewErr(http.StatusConflict, "http_addr_in_use",
						"HTTP 监听地址不可用：%v", err)
				}
				_ = ln.Close()
			}
		}
	}
	updated, err := s.store.UpdateSettings(func(x *store.Settings) error {
		if in.PublicHost != nil {
			x.PublicHost = strings.TrimSpace(*in.PublicHost)
		}
		if in.PortRange != nil {
			lo, hi := in.PortRange[0], in.PortRange[1]
			if lo < 1024 || hi > 65535 || lo > hi {
				return fmt.Errorf("端口段非法")
			}
			x.PortRange = [2]int{lo, hi}
		}
		if in.HTTPAddr != nil {
			addr := strings.TrimSpace(*in.HTTPAddr)
			if addr != "" {
				if err := validateListenAddr(addr); err != nil {
					return err
				}
			}
			x.HTTPAddr = addr
		}
		if in.DeviceAutoApprove != nil {
			x.DeviceAutoApprove = *in.DeviceAutoApprove
		}
		if in.MaxDevices != nil {
			if *in.MaxDevices < 1 || *in.MaxDevices > 10000 {
				return fmt.Errorf("最大设备数需在 1-10000 之间")
			}
			x.MaxDevices = *in.MaxDevices
		}
		if in.MaxRulesPerDevice != nil {
			if *in.MaxRulesPerDevice < 1 || *in.MaxRulesPerDevice > 1000 {
				return fmt.Errorf("每设备映射上限需在 1-1000 之间")
			}
			x.MaxRulesPerDevice = *in.MaxRulesPerDevice
		}
		if in.AdminSessionTTLSec != nil {
			if *in.AdminSessionTTLSec < 300 || *in.AdminSessionTTLSec > 7*24*3600 {
				return fmt.Errorf("会话有效期需在 300-604800 秒之间")
			}
			x.AdminSessionTTLSec = *in.AdminSessionTTLSec
		}
		if in.TrafficStatsEnabled != nil {
			x.TrafficStatsEnabled = *in.TrafficStatsEnabled
		}
		return nil
	})
	if err != nil {
		return store.Settings{}, web.NewErr(http.StatusBadRequest, "invalid_settings", "%v", err)
	}
	s.tunnels.reconcileAll()
	s.audit(actor, "settings.update", "settings", "", ip, "info")
	return updated, nil
}

// ---------------- 日志 / 指标 ----------------

// Logs 返回审计或 Agent 事件日志。
func (s *Server) Logs(kind string, limit int) []web.LogEntry {
	if kind == "agent" {
		return s.events.recent(limit)
	}
	entries := s.store.RecentAudit(limit)
	out := make([]web.LogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, web.LogEntry{
			At: e.At, Level: e.Level, Actor: e.Actor,
			Action: e.Action, Target: e.Target, Detail: e.Detail, IP: e.IP,
		})
	}
	return out
}

// Metrics 返回 Prometheus 文本格式指标。
func (s *Server) Metrics() string {
	devices := s.store.ListDevices()
	rules := s.store.ListRules("")
	pending := 0
	for _, d := range devices {
		if d.Status == store.StatusPending {
			pending++
		}
	}
	active := 0
	online := s.sessions.onlineIDs()
	for _, r := range rules {
		if r.Enabled && online[r.DeviceID] {
			active++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP queqiao_devices_online 在线设备数\n# TYPE queqiao_devices_online gauge\nqueqiao_devices_online %d\n", len(online))
	fmt.Fprintf(&b, "# HELP queqiao_devices_pending 待审批设备数\n# TYPE queqiao_devices_pending gauge\nqueqiao_devices_pending %d\n", pending)
	fmt.Fprintf(&b, "# HELP queqiao_rules_active 生效中的映射数\n# TYPE queqiao_rules_active gauge\nqueqiao_rules_active %d\n", active)
	fmt.Fprintf(&b, "# HELP queqiao_conns_current 当前隧道连接数\n# TYPE queqiao_conns_current gauge\nqueqiao_conns_current %d\n", s.stats.connsCurrent.Load())
	fmt.Fprintf(&b, "# HELP queqiao_conns_total 累计隧道连接数\n# TYPE queqiao_conns_total counter\nqueqiao_conns_total %d\n", s.stats.connsTotal.Load())
	fmt.Fprintf(&b, "# HELP queqiao_bytes_in_total 公网到本地累计字节\n# TYPE queqiao_bytes_in_total counter\nqueqiao_bytes_in_total %d\n", s.stats.bytesIn.Load())
	fmt.Fprintf(&b, "# HELP queqiao_bytes_out_total 本地到公网累计字节\n# TYPE queqiao_bytes_out_total counter\nqueqiao_bytes_out_total %d\n", s.stats.bytesOut.Load())
	fmt.Fprintf(&b, "# HELP queqiao_auth_failures_total 认证失败次数\n# TYPE queqiao_auth_failures_total counter\nqueqiao_auth_failures_total %d\n", s.stats.authFailures.Load())
	return b.String()
}

// ---------------- 管理员 ----------------

// VerifyLogin 校验管理员登录。
func (s *Server) VerifyLogin(username, password, ip string) (web.AdminView, bool) {
	admin, ok := s.store.GetAdminByUsername(username)
	if !ok {
		s.audit(username, "auth.login_failed", "admin", "用户不存在", ip, "warn")
		return web.AdminView{}, false
	}
	if !passwd.Verify(admin.PasswordHash, password) {
		s.audit(username, "auth.login_failed", admin.ID, "密码错误", ip, "warn")
		return web.AdminView{}, false
	}
	now := time.Now().UTC()
	updated, err := s.store.UpdateAdmin(admin.ID, func(a *store.Admin) error {
		a.LastLoginAt = &now
		return nil
	})
	if err != nil {
		return web.AdminView{}, false
	}
	s.audit(username, "auth.login", admin.ID, "", "", "info")
	return web.AdminView{ID: updated.ID, Username: updated.Username, MustChangePassword: updated.MustChangePassword}, true
}

// GetAdmin 按 ID 返回管理员信息。
func (s *Server) GetAdmin(id string) (web.AdminView, bool) {
	a, ok := s.store.GetAdmin(id)
	if !ok {
		return web.AdminView{}, false
	}
	return web.AdminView{ID: a.ID, Username: a.Username, MustChangePassword: a.MustChangePassword}, true
}

// ChangePassword 修改管理员密码。
func (s *Server) ChangePassword(adminID, oldPassword, newPassword string) error {
	admin, ok := s.store.GetAdmin(adminID)
	if !ok {
		return web.NewErr(http.StatusNotFound, "admin_not_found", "管理员不存在")
	}
	if !passwd.Verify(admin.PasswordHash, oldPassword) {
		return web.NewErr(http.StatusBadRequest, "invalid_old_password", "旧密码错误")
	}
	hash, err := passwd.Hash(newPassword)
	if err != nil {
		return web.NewErr(http.StatusBadRequest, "invalid_password", "%v", err)
	}
	if _, err := s.store.UpdateAdmin(adminID, func(a *store.Admin) error {
		a.PasswordHash = hash
		a.MustChangePassword = false
		return nil
	}); err != nil {
		return err
	}
	s.audit(admin.Username, "auth.password_change", adminID, "", "", "info")
	// 初始密码文件已失效，删除以免误导/扩大泄露面。
	_ = os.Remove(filepath.Join(s.cfg.DataDir, "initial-admin.txt"))
	return nil
}

// ---------------- 校验辅助 ----------------

func validateLocalAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("本地地址必须形如 host:port")
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("本地地址缺少主机")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("本地端口非法")
	}
	return nil
}

func validateListenAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("监听地址必须形如 host:port")
	}
	if host != "" && net.ParseIP(host) == nil && host != "localhost" {
		return fmt.Errorf("监听地址主机部分必须是 IP 或 localhost")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("监听端口非法")
	}
	return nil
}

func normalizeDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return ""
	}
	if strings.ContainsAny(d, "/\\: \t@") || len(d) > 253 {
		return ""
	}
	return d
}
