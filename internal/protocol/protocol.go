// Package protocol 定义 NRP 服务端与 Agent 之间的线路协议（v1）。
//
// 传输层：一条 TLS 连接。
// 握手：4 字节大端长度 + JSON 帧，顺序 hello -> challenge -> proof -> ok/pending/reject。
// 认证成功后：同一连接升级为 yamux 会话，流首字节标识类型。
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// Version 是协议主版本号；不匹配时服务端直接拒绝。
	Version = 1

	// MaxFrame 是单条 JSON 帧上限（1 MiB）。
	MaxFrame = 1 << 20

	// 流类型（yamux 流的第一个字节）。
	StreamControl byte = 0x01
	StreamData    byte = 0x02

	// 数据流 dial 状态字节。
	DataStatusOK   byte = 0x00
	DataStatusFail byte = 0x01
)

// 消息类型常量。
const (
	TypeHello     = "hello"
	TypeChallenge = "challenge"
	TypeProof     = "proof"
	TypeOK        = "ok"
	TypePending   = "pending"
	TypeReject    = "reject"

	TypeConfig    = "config"
	TypeConfigAck = "config_ack"
	TypePing      = "ping"
	TypePong      = "pong"
	TypeStats     = "stats"
	TypeEvent     = "event"
	TypeKick      = "kick"
	TypeShutdown  = "shutdown"
)

// 握手错误码。
const (
	CodeUnsupportedVersion = "unsupported_version"
	CodeBadRequest         = "bad_request"
	CodeBadProof           = "bad_proof"
	CodeBadNonce           = "bad_nonce"
	CodeFingerprint        = "fingerprint_mismatch"
	CodeBlocked            = "blocked"
	CodeDeviceLimit        = "device_limit"
	CodeRateLimited        = "rate_limited"
	CodeServerBusy         = "server_busy"
)

// ErrFrameTooLarge 表示对端发来的帧超过 MaxFrame。
var ErrFrameTooLarge = errors.New("protocol: frame too large")

// Hello 是客户端的第一帧。
type Hello struct {
	Type         string `json:"type"`
	Version      int    `json:"version"`
	PublicKey    string `json:"public_key"`
	MachineID    string `json:"machine_id"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
	KeyCode      string `json:"key_code,omitempty"`
}

// Challenge 携带一次性 nonce。
type Challenge struct {
	Type     string `json:"type"`
	Nonce    string `json:"nonce"`
	DeviceID string `json:"device_id"`
	KeyCode  string `json:"key_code"`
	Status   string `json:"status"`
}

// Proof 是对 nonce 的 Ed25519 签名。
type Proof struct {
	Type      string `json:"type"`
	Signature string `json:"signature"`
}

// OK 表示认证成功，随后连接升级为 yamux。
type OK struct {
	Type          string `json:"type"`
	DeviceID      string `json:"device_id"`
	KeyCode       string `json:"key_code"`
	ConfigVersion int64  `json:"config_version"`
	HeartbeatSec  int    `json:"heartbeat_sec"`
	ServerTime    int64  `json:"server_time"`
}

// Pending 表示设备尚未审批。
type Pending struct {
	Type          string `json:"type"`
	DeviceID      string `json:"device_id"`
	KeyCode       string `json:"key_code"`
	Message       string `json:"message"`
	RetryAfterSec int    `json:"retry_after_sec"`
}

// Reject 表示认证被拒绝。
type Reject struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RuleSpec 是服务端下发给 Agent 的规则快照。
type RuleSpec struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Proto      string `json:"proto"`
	LocalAddr  string `json:"local_addr"`
	RemotePort int    `json:"remote_port,omitempty"`
	Domain     string `json:"domain,omitempty"`
}

// Config 是全量配置快照。
type Config struct {
	Type     string     `json:"type"`
	Revision int64      `json:"revision"`
	Rules    []RuleSpec `json:"rules"`
}

// ConfigAck 是 Agent 应用配置后的确认。
type ConfigAck struct {
	Type     string `json:"type"`
	Revision int64  `json:"revision"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// Ping 是控制流心跳。
type Ping struct {
	Type string `json:"type"`
	TS   int64  `json:"ts"`
}

// Pong 是心跳响应。
type Pong struct {
	Type string `json:"type"`
	TS   int64  `json:"ts"`
}

// RuleStats 是单条规则的 Agent 侧统计。
type RuleStats struct {
	RuleID     string `json:"rule_id"`
	BytesIn    int64  `json:"bytes_in"`
	BytesOut   int64  `json:"bytes_out"`
	ConnsTotal int64  `json:"conns_total"`
}

// Stats 是 Agent 定期上报的统计。
type Stats struct {
	Type  string      `json:"type"`
	At    int64       `json:"at"`
	Rules []RuleStats `json:"rules"`
}

// Event 是 Agent 上送的事件（日志）。
type Event struct {
	Type   string `json:"type"`
	Level  string `json:"level"`
	Msg    string `json:"msg"`
	RuleID string `json:"rule_id,omitempty"`
}

// Kick 要求 Agent 主动断开。
type Kick struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// Shutdown 通知 Agent 服务端即将停机。
type Shutdown struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// DataHeader 是数据流的 JSON 头（服务端 -> Agent）。
type DataHeader struct {
	RuleID     string `json:"rule_id"`
	Proto      string `json:"proto"`
	RemoteAddr string `json:"remote_addr"`
}

// WriteMessage 写入一个长度前缀 JSON 帧。
func WriteMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("protocol: marshal: %w", err)
	}
	if len(b) > MaxFrame {
		return ErrFrameTooLarge
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return nil
}

// ReadMessage 读取一个长度前缀 JSON 帧并解码到 v。
func ReadMessage(r io.Reader, v any) error {
	b, err := ReadRaw(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("protocol: unmarshal: %w", err)
	}
	return nil
}

// ReadRaw 读取一个长度前缀帧的原始 JSON 字节。
func ReadRaw(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// PeekType 返回帧的类型字段（用于按类型分派）。
func PeekType(raw []byte) (string, error) {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", err
	}
	return env.Type, nil
}
