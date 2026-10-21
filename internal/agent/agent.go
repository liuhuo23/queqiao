// Package agent 实现客户端：零配置连接服务端、认证、同步配置、转发数据流。
package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"nrp/internal/agentstate"
	"nrp/internal/auth"
	"nrp/internal/machineid"
	"nrp/internal/protocol"
	"nrp/internal/tlsutil"
	"nrp/internal/version"
)

const (
	dialTimeout     = 10 * time.Second
	handshakeWait   = 15 * time.Second
	controlDeadline = 45 * time.Second
	statsEvery      = 10 * time.Second
)

// Options 是 Agent 的运行参数（通常只需一次性给出服务端地址）。
type Options struct {
	ServerAddr string
	StateDir   string
	AllowLAN   bool
	CAFile     string
	Insecure   bool
	Log        *slog.Logger
}

// Agent 是客户端主体。
type Agent struct {
	opts  Options
	log   *slog.Logger
	state *agentstate.State
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey

	mu    sync.RWMutex
	rules map[string]protocol.RuleSpec

	statsMu sync.Mutex
	stats   map[string]*ruleCounters

	// lastRev 记录已应用的最新配置版本，用于丢弃乱序到达的旧配置。
	lastRev atomic.Int64

	lastPending string
}

type ruleCounters struct {
	bytesIn    atomic.Int64
	bytesOut   atomic.Int64
	connsTotal atomic.Int64
}

// retryError 表示应退避重试（或致命）的连接错误。
type retryError struct {
	err   error
	delay time.Duration
	fatal bool
}

func (e *retryError) Error() string { return e.err.Error() }
func (e *retryError) Unwrap() error { return e.err }

// New 加载状态与身份密钥。
func New(opts Options) (*Agent, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.StateDir == "" {
		dir, err := agentstate.DefaultDir()
		if err != nil {
			return nil, err
		}
		opts.StateDir = dir
	}
	state, err := agentstate.Load(opts.StateDir)
	if err != nil {
		return nil, err
	}
	pub, priv, _, err := agentstate.LoadOrCreateIdentity(opts.StateDir)
	if err != nil {
		return nil, err
	}
	if opts.ServerAddr != "" {
		state.ServerAddr = normalizeServerAddr(opts.ServerAddr)
	}
	a := &Agent{
		opts:  opts,
		log:   opts.Log,
		state: state,
		pub:   pub,
		priv:  priv,
		rules: map[string]protocol.RuleSpec{},
		stats: map[string]*ruleCounters{},
	}
	if err := a.state.Save(a.opts.StateDir); err != nil {
		return nil, err
	}
	return a, nil
}

// KeyCode 返回本机密钥码。
func (a *Agent) KeyCode() string { return auth.KeyCode(a.pub) }

// PublicKey 返回 base64 公钥。
func (a *Agent) PublicKey() string { return auth.EncodePublic(a.pub) }

// StateDir 返回状态目录。
func (a *Agent) StateDir() string { return a.opts.StateDir }

// ServerAddr 返回服务端地址。
func (a *Agent) ServerAddr() string { return a.state.ServerAddr }

// IdentityPath 返回身份文件路径。
func (a *Agent) IdentityPath() string { return agentstate.IdentityPath(a.opts.StateDir) }

// Run 持续连接服务端，直到 ctx 结束（网络错误自动重连）。
func (a *Agent) Run(ctx context.Context) error {
	if a.state.ServerAddr == "" {
		return errors.New("尚未配置服务端地址，请首次运行时指定：nrp-agent -s <服务端IP或域名>:7600")
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		connected, err := a.connectOnce(ctx)
		if connected {
			backoff = time.Second
		}
		if ctx.Err() != nil {
			return nil
		}
		var delay time.Duration
		var re *retryError
		if errors.As(err, &re) {
			if re.fatal {
				return re.err
			}
			delay = re.delay
			if delay <= 0 {
				delay = backoff
			}
		} else if err != nil {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			delay = jitter(backoff)
		}
		if err != nil {
			a.log.Warn("连接失败，稍后重试", "err", err, "retry_in", delay.Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

func jitter(d time.Duration) time.Duration {
	factor := 0.8 + 0.4*float64(time.Now().UnixNano()%1000)/1000.0
	return time.Duration(float64(d) * factor)
}

// connectOnce 建立一次连接并运行会话；connected 表示认证成功过。
func (a *Agent) connectOnce(ctx context.Context) (bool, error) {
	tlsCfg, err := a.tlsConfig()
	if err != nil {
		return false, &retryError{err: err, fatal: true}
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", a.state.ServerAddr, tlsCfg)
	if err != nil {
		return false, fmt.Errorf("连接服务端 %s 失败: %w", a.state.ServerAddr, err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(handshakeWait))
	mi := machineid.Get()
	hello := protocol.Hello{
		Type:         protocol.TypeHello,
		Version:      protocol.Version,
		PublicKey:    auth.EncodePublic(a.pub),
		MachineID:    mi.Hash,
		Hostname:     mi.Hostname,
		OS:           mi.OS,
		Arch:         mi.Arch,
		AgentVersion: version.String(),
		KeyCode:      a.KeyCode(),
	}
	if err := protocol.WriteMessage(conn, &hello); err != nil {
		return false, fmt.Errorf("发送 hello 失败: %w", err)
	}

	raw, err := protocol.ReadRaw(conn)
	if err != nil {
		return false, fmt.Errorf("读取服务端响应失败: %w", err)
	}
	typ, _ := protocol.PeekType(raw)
	switch typ {
	case protocol.TypeReject:
		return false, a.rejectError(raw)
	case protocol.TypeChallenge:
		var ch protocol.Challenge
		if err := json.Unmarshal(raw, &ch); err != nil {
			return false, fmt.Errorf("解析 challenge 失败: %w", err)
		}
		nonce, err := base64.StdEncoding.DecodeString(ch.Nonce)
		if err != nil {
			return false, fmt.Errorf("解析 nonce 失败: %w", err)
		}
		sig := auth.SignProof(a.priv, nonce, mi.Hash)
		proof := protocol.Proof{Type: protocol.TypeProof, Signature: base64.StdEncoding.EncodeToString(sig)}
		if err := protocol.WriteMessage(conn, &proof); err != nil {
			return false, fmt.Errorf("发送 proof 失败: %w", err)
		}
		raw, err = protocol.ReadRaw(conn)
		if err != nil {
			return false, fmt.Errorf("读取认证结果失败: %w", err)
		}
		typ, _ = protocol.PeekType(raw)
	default:
		return false, fmt.Errorf("服务端响应类型未知: %s", typ)
	}

	switch typ {
	case protocol.TypeReject:
		return false, a.rejectError(raw)
	case protocol.TypePending:
		var pd protocol.Pending
		_ = json.Unmarshal(raw, &pd)
		if a.state.DeviceID != pd.DeviceID {
			a.state.DeviceID = pd.DeviceID
			a.state.KeyCode = pd.KeyCode
			_ = a.state.Save(a.opts.StateDir)
		}
		a.printPending(pd)
		retry := time.Duration(pd.RetryAfterSec) * time.Second
		if retry <= 0 {
			retry = 5 * time.Second
		}
		return false, &retryError{err: errors.New("设备待管理员审批"), delay: retry}
	case protocol.TypeOK:
		var ok protocol.OK
		if err := json.Unmarshal(raw, &ok); err != nil {
			return false, fmt.Errorf("解析 ok 失败: %w", err)
		}
		if a.state.DeviceID != ok.DeviceID || a.state.KeyCode != ok.KeyCode {
			a.state.DeviceID = ok.DeviceID
			a.state.KeyCode = ok.KeyCode
			_ = a.state.Save(a.opts.StateDir)
		}
		a.lastPending = ""
		a.log.Info("已连接服务端", "server", a.state.ServerAddr, "device_id", ok.DeviceID, "key_code", ok.KeyCode)
	default:
		return false, fmt.Errorf("认证结果类型未知: %s", typ)
	}

	_ = conn.SetDeadline(time.Time{})
	mux, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		return false, fmt.Errorf("初始化 yamux 失败: %w", err)
	}
	runErr := a.serveSession(ctx, mux)
	return true, runErr
}

func (a *Agent) rejectError(raw []byte) error {
	var rej protocol.Reject
	_ = json.Unmarshal(raw, &rej)
	msg := fmt.Sprintf("服务端拒绝连接：%s（%s）", rej.Message, rej.Code)
	switch rej.Code {
	case protocol.CodeUnsupportedVersion:
		return &retryError{err: errors.New(msg + "，请升级 Agent"), fatal: true}
	case protocol.CodeBadProof:
		return &retryError{err: errors.New(msg + "，本机身份密钥可能损坏，可执行 nrp-agent reset-identity 重置"), fatal: true}
	case protocol.CodeFingerprint:
		return &retryError{err: errors.New(msg + "，请管理员在管理台执行「重绑定机器」"), delay: 5 * time.Minute}
	case protocol.CodeBlocked, protocol.CodeDeviceLimit:
		return &retryError{err: errors.New(msg), delay: 5 * time.Minute}
	case protocol.CodeRateLimited, protocol.CodeServerBusy:
		return &retryError{err: errors.New(msg), delay: time.Minute}
	default:
		return &retryError{err: errors.New(msg), delay: time.Minute}
	}
}

func (a *Agent) printPending(pd protocol.Pending) {
	if a.lastPending == pd.DeviceID {
		return
	}
	a.lastPending = pd.DeviceID
	lines := []string{
		"",
		"================= 设备待审批 =================",
		"  密钥:   " + pd.KeyCode,
		"  主机:   " + machineid.Get().Hostname,
		"  设备ID: " + pd.DeviceID,
		"",
		"请登录服务端管理台，在「设备」页批准这台设备。",
		"(可通过 SSH 隧道访问管理台：",
		" ssh -L 7610:127.0.0.1:7610 root@<服务端地址>",
		" 然后打开 http://127.0.0.1:7610)",
		"==============================================",
		"",
	}
	fmt.Fprint(os.Stderr, strings.Join(lines, "\n"))
	a.log.Warn("设备待管理员审批", "key_code", pd.KeyCode, "device_id", pd.DeviceID)
}

// ---------------- 会话 ----------------

// clientSession 是认证成功后的一次长连接会话。
type clientSession struct {
	a    *Agent
	mux  *yamux.Session
	ctrl net.Conn
	wmu  sync.Mutex
	done chan struct{}
	once sync.Once
}

func (a *Agent) serveSession(ctx context.Context, mux *yamux.Session) error {
	stream, err := mux.OpenStream()
	if err != nil {
		return fmt.Errorf("打开控制流失败: %w", err)
	}
	if _, err := stream.Write([]byte{protocol.StreamControl}); err != nil {
		return err
	}
	s := &clientSession{a: a, mux: mux, ctrl: stream, done: make(chan struct{})}
	defer s.close()

	go s.readControl()
	go s.acceptData()

	statsTicker := time.NewTicker(statsEvery)
	defer statsTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.done:
			return errors.New("会话已断开")
		case <-statsTicker.C:
			_ = s.sendStats()
		}
	}
}

func (s *clientSession) close() {
	s.once.Do(func() {
		close(s.done)
		if s.ctrl != nil {
			_ = s.ctrl.Close()
		}
		_ = s.mux.Close()
	})
}

func (s *clientSession) send(v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.ctrl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return protocol.WriteMessage(s.ctrl, v)
}

func (s *clientSession) readControl() {
	defer s.close()
	for {
		_ = s.ctrl.SetReadDeadline(time.Now().Add(controlDeadline))
		raw, err := protocol.ReadRaw(s.ctrl)
		if err != nil {
			return
		}
		typ, _ := protocol.PeekType(raw)
		switch typ {
		case protocol.TypeConfig:
			var cfg protocol.Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				continue
			}
			// 丢弃乱序到达的旧配置，避免被并发推送覆盖成过期规则。
			if cfg.Revision > 0 && cfg.Revision < s.a.lastRev.Load() {
				ack := protocol.ConfigAck{Type: protocol.TypeConfigAck, Revision: cfg.Revision, OK: true}
				if err := s.send(&ack); err != nil {
					return
				}
				continue
			}
			applyErr := s.a.applyConfig(cfg.Rules)
			if applyErr == nil && cfg.Revision > 0 {
				s.a.lastRev.Store(cfg.Revision)
			}
			ack := protocol.ConfigAck{Type: protocol.TypeConfigAck, Revision: cfg.Revision, OK: applyErr == nil}
			if applyErr != nil {
				ack.Error = applyErr.Error()
			}
			if err := s.send(&ack); err != nil {
				return
			}
		case protocol.TypePing:
			var ping protocol.Ping
			_ = json.Unmarshal(raw, &ping)
			if err := s.send(&protocol.Pong{Type: protocol.TypePong, TS: ping.TS}); err != nil {
				return
			}
		case protocol.TypeKick:
			var kick protocol.Kick
			_ = json.Unmarshal(raw, &kick)
			s.a.log.Warn("被服务端断开", "reason", kick.Reason)
			return
		case protocol.TypeShutdown:
			s.a.log.Info("服务端停机，稍后重连")
			return
		}
	}
}

func (a *Agent) applyConfig(specs []protocol.RuleSpec) error {
	next := make(map[string]protocol.RuleSpec, len(specs))
	var rejected []string
	for _, r := range specs {
		if !a.allowedLocal(r.LocalAddr) {
			rejected = append(rejected, fmt.Sprintf("%s(%s)", r.Name, r.LocalAddr))
			continue
		}
		next[r.ID] = r
	}
	a.mu.Lock()
	a.rules = next
	a.mu.Unlock()
	if len(rejected) > 0 {
		msg := "以下映射的本地地址被安全策略拒绝（默认只允许 loopback，可用 -allow-lan 放开私网）：" + strings.Join(rejected, ", ")
		a.log.Warn(msg)
		return errors.New(msg)
	}
	a.log.Info("配置已更新", "rules", len(next))
	return nil
}

func (a *Agent) allowedLocal(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if a.opts.AllowLAN && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return true
	}
	return false
}

func (a *Agent) rule(id string) (protocol.RuleSpec, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	r, ok := a.rules[id]
	return r, ok
}

func (a *Agent) counterFor(ruleID string) *ruleCounters {
	a.statsMu.Lock()
	defer a.statsMu.Unlock()
	c, ok := a.stats[ruleID]
	if !ok {
		c = &ruleCounters{}
		a.stats[ruleID] = c
	}
	return c
}

func (s *clientSession) sendStats() error {
	stats := protocol.Stats{Type: protocol.TypeStats, At: time.Now().Unix()}
	s.a.mu.RLock()
	ids := make([]string, 0, len(s.a.rules))
	for id := range s.a.rules {
		ids = append(ids, id)
	}
	s.a.mu.RUnlock()
	for _, id := range ids {
		c := s.a.counterFor(id)
		stats.Rules = append(stats.Rules, protocol.RuleStats{
			RuleID:     id,
			BytesIn:    c.bytesIn.Load(),
			BytesOut:   c.bytesOut.Load(),
			ConnsTotal: c.connsTotal.Load(),
		})
	}
	return s.send(&stats)
}

// acceptData 接受服务端打开的数据流。
func (s *clientSession) acceptData() {
	for {
		st, err := s.mux.AcceptStream()
		if err != nil {
			s.close()
			return
		}
		go s.handleDataStream(st)
	}
}

func (s *clientSession) handleDataStream(st *yamux.Stream) {
	defer func() { _ = st.Close() }()
	var kind [1]byte
	_ = st.SetReadDeadline(time.Now().Add(handshakeWait))
	if _, err := io.ReadFull(st, kind[:]); err != nil || kind[0] != protocol.StreamData {
		return
	}
	var hdr protocol.DataHeader
	if err := protocol.ReadMessage(st, &hdr); err != nil {
		return
	}
	rule, ok := s.a.rule(hdr.RuleID)
	if !ok {
		_, _ = st.Write([]byte{protocol.DataStatusFail})
		return
	}
	cnt := s.a.counterFor(hdr.RuleID)
	cnt.connsTotal.Add(1)

	local, err := net.DialTimeout("tcp", rule.LocalAddr, 5*time.Second)
	if err != nil {
		_, _ = st.Write([]byte{protocol.DataStatusFail})
		_ = s.send(&protocol.Event{Type: protocol.TypeEvent, Level: "warn",
			Msg: fmt.Sprintf("连接本地服务 %s 失败: %v", rule.LocalAddr, err), RuleID: rule.ID})
		return
	}
	defer func() { _ = local.Close() }()
	_ = st.SetReadDeadline(time.Time{})
	if _, err := st.Write([]byte{protocol.DataStatusOK}); err != nil {
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(local, st)
		cnt.bytesIn.Add(n)
		closeWrite(local)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(st, local)
		cnt.bytesOut.Add(n)
		closeWrite(st)
	}()
	wg.Wait()
}

// ---------------- TLS ----------------

func (a *Agent) tlsConfig() (*tls.Config, error) {
	host, _, err := net.SplitHostPort(a.state.ServerAddr)
	if err != nil {
		host = a.state.ServerAddr
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"nrp/1"},
	}
	if net.ParseIP(host) == nil {
		cfg.ServerName = host
	}
	if a.opts.Insecure {
		cfg.InsecureSkipVerify = true
		return cfg, nil
	}
	if a.opts.CAFile != "" {
		// 明确要求用 CA 校验时，加载失败必须硬失败，不能静默降级为 TOFU。
		pemData, err := os.ReadFile(a.opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("读取 CA 文件 %s 失败: %w", a.opts.CAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("CA 文件 %s 中没有有效 PEM 证书", a.opts.CAFile)
		}
		cfg.RootCAs = pool
		return cfg, nil
	}
	// TOFU：首次连接记录服务端证书指纹，之后严格比对。
	addr := a.state.ServerAddr
	cfg.InsecureSkipVerify = true
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("服务端未提供证书")
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return err
		}
		fp := tlsutil.Fingerprint(cert)
		pin := a.state.TrustPins[addr]
		if pin == "" {
			a.state.TrustPins[addr] = fp
			if err := a.state.Save(a.opts.StateDir); err != nil {
				a.log.Warn("保存证书指纹失败", "err", err)
			}
			a.log.Warn("首次连接，已固定服务端证书指纹", "server", addr, "fingerprint", fp)
			return nil
		}
		if pin != fp {
			return fmt.Errorf("服务端证书指纹与首次连接不一致（可能被中间人劫持）；如确认服务端已重装证书，请删除状态目录中的 trust_pins 后重试")
		}
		return nil
	}
	return cfg, nil
}

func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.EnableKeepAlive = false
	c.AcceptBacklog = 256
	c.MaxStreamWindowSize = 256 * 1024
	c.StreamOpenTimeout = 10 * time.Second
	c.StreamCloseTimeout = 2 * time.Minute
	return c
}

func normalizeServerAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "nrp://")
	addr = strings.TrimPrefix(addr, "tls://")
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "7600")
	}
	return addr
}

type closeWriter interface {
	CloseWrite() error
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}
