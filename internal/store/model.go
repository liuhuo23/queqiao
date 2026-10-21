// Package store 提供服务端配置的持久化（单文件 JSON + 审计 JSONL）。
//
// 设计取舍见 docs/DESIGN.md：单实例、规模 < 10^4，用 JSON 原子写换取零依赖与部署简单；
// 抽象出方法边界，后续可替换为 SQLite 而不影响调用方。
package store

import "time"

// 设备状态。
const (
	StatusPending = "pending"
	StatusActive  = "active"
	StatusBlocked = "blocked"
)

// 映射协议。
const (
	ProtoTCP  = "tcp"
	ProtoHTTP = "http"
)

// Device 是一台被管理的客户端主机。
type Device struct {
	ID                  string     `json:"id"`
	KeyCode             string     `json:"key_code"`
	PublicKey           string     `json:"public_key"`
	Name                string     `json:"name"`
	Status              string     `json:"status"`
	MachineID           string     `json:"machine_id"`
	Hostname            string     `json:"hostname"`
	OS                  string     `json:"os"`
	Arch                string     `json:"arch"`
	AgentVersion        string     `json:"agent_version"`
	LastIP              string     `json:"last_ip"`
	Note                string     `json:"note"`
	LastError           string     `json:"last_error,omitempty"`
	FingerprintConflict bool       `json:"fingerprint_conflict"`
	CreatedAt           time.Time  `json:"created_at"`
	ApprovedAt          *time.Time `json:"approved_at,omitempty"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
}

// Rule 是一条端口/域名映射规则。
type Rule struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id"`
	Name       string    `json:"name"`
	Proto      string    `json:"proto"`
	LocalAddr  string    `json:"local_addr"`
	RemotePort int       `json:"remote_port,omitempty"`
	Domain     string    `json:"domain,omitempty"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Admin 是管理台账号。
type Admin struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	PasswordHash       string     `json:"password_hash"`
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	LastLoginAt        *time.Time `json:"last_login_at,omitempty"`
}

// Settings 是可由管理台修改的全局设置。
type Settings struct {
	PublicHost          string `json:"public_host"`
	PortRange           [2]int `json:"port_range"`
	HTTPAddr            string `json:"http_addr"`
	DeviceAutoApprove   bool   `json:"device_auto_approve"`
	MaxDevices          int    `json:"max_devices"`
	MaxRulesPerDevice   int    `json:"max_rules_per_device"`
	AdminSessionTTLSec  int    `json:"admin_session_ttl_sec"`
	TrafficStatsEnabled bool   `json:"traffic_stats_enabled"`
}

// AuditEntry 是一条审计记录。
type AuditEntry struct {
	At     time.Time `json:"at"`
	Level  string    `json:"level"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}

// DefaultSettings 返回默认设置。
func DefaultSettings() Settings {
	return Settings{
		PublicHost:          "",
		PortRange:           [2]int{39000, 40000},
		HTTPAddr:            "",
		DeviceAutoApprove:   false,
		MaxDevices:          200,
		MaxRulesPerDevice:   50,
		AdminSessionTTLSec:  12 * 3600,
		TrafficStatsEnabled: true,
	}
}
