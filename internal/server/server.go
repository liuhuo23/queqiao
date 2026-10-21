package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/liuhuo23/queqiao/internal/auth"
	"github.com/liuhuo23/queqiao/internal/config"
	"github.com/liuhuo23/queqiao/internal/passwd"
	"github.com/liuhuo23/queqiao/internal/protocol"
	"github.com/liuhuo23/queqiao/internal/store"
	"github.com/liuhuo23/queqiao/internal/tlsutil"
	"github.com/liuhuo23/queqiao/internal/version"
	"github.com/liuhuo23/queqiao/internal/web"
)

const (
	handshakeTimeout = 10 * time.Second
	controlDeadline  = 45 * time.Second
	heartbeatEvery   = 15 * time.Second
	agentRetrySec    = 5

	// maxPendingDevices 限制未审批设备数量，避免陌生人用任意公钥占满设备名额。
	maxPendingDevices = 20
)

// Server 是 鹊桥 服务端：控制面（TLS+握手+yamux）、数据面（公网监听）、管理台。
type Server struct {
	cfg       config.Server
	store     *store.Store
	log       *slog.Logger
	startedAt time.Time
	tlsCfg    *tls.Config

	sessions    *registry
	tunnels     *tunnelManager
	stats       *globalStats
	events      *eventRing
	authLimiter *rateLimiter
	web         *web.Server

	controlLn   net.Listener
	controlAddr string
	webLn       net.Listener
	webAddr     string
	webSrv      *http.Server
}

// New 构造服务端（不监听端口）。
func New(cfg config.Server, st *store.Store, log *slog.Logger) (*Server, error) {
	cert, err := tlsutil.LoadOrCreate(cfg.TLSCert, cfg.TLSKey, cfg.DataDir,
		[]string{cfg.PublicHost, "localhost", "127.0.0.1"})
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:         cfg,
		store:       st,
		log:         log,
		startedAt:   time.Now(),
		tlsCfg:      tlsutil.ServerConfig(cert),
		sessions:    newRegistry(),
		stats:       &globalStats{},
		events:      newEventRing(1000),
		authLimiter: newIPLimiter(10, time.Minute),
	}
	s.tunnels = newTunnelManager(s)
	s.web = web.New(s, log)

	// 首次运行（还没有管理员）：把配置文件里的 bootstrap 值写入设置，并创建初始管理员。
	if st.AdminCount() == 0 {
		_, _ = st.UpdateSettings(func(x *store.Settings) error {
			if cfg.PublicHost != "" {
				x.PublicHost = cfg.PublicHost
			}
			if cfg.HTTPAddr != "" {
				x.HTTPAddr = cfg.HTTPAddr
			}
			x.PortRange = cfg.PortRange
			return nil
		})
		if err := s.createInitialAdmin(); err != nil {
			return nil, err
		}
	}
	s.tunnels.reconcileAll()
	return s, nil
}

func (s *Server) createInitialAdmin() error {
	pw, err := randomPassword(18)
	if err != nil {
		return err
	}
	hash, err := passwd.Hash(pw)
	if err != nil {
		return err
	}
	if _, err := s.store.AddAdmin("admin", hash); err != nil {
		return err
	}
	path := filepath.Join(s.cfg.DataDir, "initial-admin.txt")
	content := fmt.Sprintf("鹊桥 初始管理员账号\n\n用户名: admin\n密码:   %s\n\n请登录后立即修改密码。\n", pw)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		s.log.Warn("已创建初始管理员（写入密码文件失败，密码见本条日志）",
			"username", "admin", "password", pw)
	} else {
		s.log.Warn("已创建初始管理员账号，请使用密码文件中的密码登录并尽快修改",
			"username", "admin", "password_file", path)
	}
	return nil
}

func randomPassword(n int) (string, error) {
	const charset = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成初始密码失败: %w", err)
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b), nil
}

// Start 监听控制端口与管理台。
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.ControlAddr)
	if err != nil {
		return fmt.Errorf("监听控制端口 %s 失败: %w", s.cfg.ControlAddr, err)
	}
	s.controlLn = ln
	s.controlAddr = ln.Addr().String()
	tln := tls.NewListener(ln, s.tlsCfg)
	go s.acceptControl(ctx, tln)

	wln, err := net.Listen("tcp", s.cfg.WebAddr)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("监听管理台 %s 失败: %w", s.cfg.WebAddr, err)
	}
	s.webLn = wln
	s.webAddr = wln.Addr().String()
	s.webSrv = &http.Server{
		Handler:           s.web.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		if err := s.webSrv.Serve(wln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("管理台服务退出", "err", err)
		}
	}()

	s.log.Info("鹊桥 服务端已启动",
		"version", version.String(),
		"control", s.controlAddr,
		"web", s.webAddr,
		"data_dir", s.cfg.DataDir)
	return nil
}

// Run 启动并阻塞到 ctx 结束，然后优雅关闭。
func (s *Server) Run(ctx context.Context) error {
	if err := s.Start(ctx); err != nil {
		return err
	}
	return s.Wait(ctx)
}

// Wait 阻塞到 ctx 结束，然后优雅关闭（要求已 Start）。
func (s *Server) Wait(ctx context.Context) error {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.Shutdown(shutdownCtx)
}

// Shutdown 关闭监听、踢下线并停止管理台。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.controlLn != nil {
		_ = s.controlLn.Close()
	}
	s.sessions.kickAll("服务端停机")
	if s.webSrv != nil {
		_ = s.webSrv.Shutdown(ctx)
	}
	s.tunnels.closeAll()
	_ = s.store.Close()
	s.log.Info("鹊桥 服务端已停止")
	return nil
}

// ControlAddr 返回控制端口实际监听地址。
func (s *Server) ControlAddr() string { return s.controlAddr }

// WebAddr 返回管理台实际监听地址。
func (s *Server) WebAddr() string { return s.webAddr }

// Uptime 返回运行时长。
func (s *Server) Uptime() time.Duration { return time.Since(s.startedAt) }

func (s *Server) acceptControl(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			s.log.Warn("控制端口 accept 失败", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go s.handleConn(ctx, c)
	}
}

// handleConn 完成认证握手并进入会话循环。
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	ip := remoteHost(conn)
	log := s.log.With("ip", ip)
	defer func() { _ = conn.Close() }()

	if !s.authLimiter.allow(ip) {
		s.stats.authFailures.Add(1)
		s.rejectConn(conn, protocol.CodeRateLimited, "认证尝试过于频繁，请稍后再试")
		return
	}

	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	var hello protocol.Hello
	if err := protocol.ReadMessage(conn, &hello); err != nil {
		log.Debug("握手读取 hello 失败", "err", err)
		return
	}
	if hello.Type != protocol.TypeHello {
		s.rejectConn(conn, protocol.CodeBadRequest, "首帧必须是 hello")
		return
	}
	if hello.Version != protocol.Version {
		s.rejectConn(conn, protocol.CodeUnsupportedVersion,
			fmt.Sprintf("协议版本不匹配：服务端 %d，客户端 %d", protocol.Version, hello.Version))
		return
	}
	pub, err := auth.ParsePublicKey(hello.PublicKey)
	if err != nil {
		s.rejectConn(conn, protocol.CodeBadRequest, "公钥格式非法")
		return
	}
	// 只接受规范编码：base64 解码会忽略 \r/\n，若不校验，攻击者可用同一把私钥的
	// 不同字符串变体绕过「按公钥查已登记设备/封禁/指纹绑定」的逻辑。
	publicKey := auth.EncodePublic(pub)
	if hello.PublicKey != publicKey {
		s.rejectConn(conn, protocol.CodeBadRequest, "公钥必须是规范 base64 编码")
		return
	}
	if s.store.IsKeyRevoked(publicKey) {
		s.stats.authFailures.Add(1)
		s.audit("system", "device.revoked_login", "", truncate(strings.TrimSpace(hello.Hostname), 64), ip, "warn")
		s.rejectConn(conn, protocol.CodeBlocked, "该密钥已被吊销，请重新安装 Agent 生成新身份")
		return
	}
	machineID := strings.ToLower(strings.TrimSpace(hello.MachineID))
	if len(machineID) != 64 || !isHex(machineID) {
		s.rejectConn(conn, protocol.CodeBadRequest, "机器指纹格式非法")
		return
	}
	hostname := truncate(strings.TrimSpace(hello.Hostname), 128)
	if hostname == "" {
		hostname = "未命名设备"
	}

	dev, exists := s.store.GetDeviceByPublicKey(publicKey)
	if exists {
		if dev.Status == store.StatusBlocked {
			s.stats.authFailures.Add(1)
			s.audit("system", "device.blocked_login", dev.ID, dev.Name, ip, "warn")
			s.rejectConn(conn, protocol.CodeBlocked, "设备已被封禁，请联系管理员")
			return
		}
		if dev.MachineID != "" && dev.MachineID != machineID {
			_, _ = s.store.UpdateDevice(dev.ID, func(d *store.Device) error {
				d.FingerprintConflict = true
				d.LastError = "机器指纹与登记不一致"
				d.LastIP = ip
				return nil
			})
			s.audit("system", "device.fingerprint_mismatch", dev.ID, dev.Name, ip, "warn")
			s.recordEvent("warn", dev.ID, fmt.Sprintf("设备 %s 机器指纹不匹配，需管理员重绑定", dev.Name))
			s.stats.authFailures.Add(1)
			s.rejectConn(conn, protocol.CodeFingerprint, "机器指纹与首次登记不一致，需管理员在网页重绑定")
			return
		}
	}

	// 挑战-应答。
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		s.rejectConn(conn, protocol.CodeServerBusy, "服务端随机数失败")
		return
	}
	ch := protocol.Challenge{
		Type:    protocol.TypeChallenge,
		Nonce:   base64.StdEncoding.EncodeToString(nonce),
		KeyCode: auth.KeyCode(pub),
		// 不提前返回设备状态，避免未认证方探测某公钥是否已登记/被封禁。
	}
	if exists {
		ch.DeviceID = dev.ID
	}
	if err := protocol.WriteMessage(conn, &ch); err != nil {
		return
	}
	var proof protocol.Proof
	if err := protocol.ReadMessage(conn, &proof); err != nil {
		return
	}
	sig, err := base64.StdEncoding.DecodeString(proof.Signature)
	if err != nil || !auth.VerifyProof(pub, nonce, machineID, sig) {
		s.stats.authFailures.Add(1)
		s.audit("system", "device.bad_proof", dev.ID, hostname, ip, "warn")
		s.rejectConn(conn, protocol.CodeBadProof, "密钥证明校验失败")
		return
	}

	// 认证成功：清零该 IP 的失败计数（限流只针对失败尝试）。
	s.authLimiter.reset(ip)
	settings := s.store.Settings()
	now := time.Now().UTC()

	// 证明通过后再创建新设备，避免未认证的垃圾记录。
	if !exists {
		if s.store.CountDevices() >= settings.MaxDevices {
			s.rejectConn(conn, protocol.CodeDeviceLimit, "服务端设备数量已达上限")
			return
		}
		if s.store.CountDevicesByStatus(store.StatusPending) >= maxPendingDevices {
			s.stats.authFailures.Add(1)
			s.rejectConn(conn, protocol.CodeServerBusy, "待审批设备过多，请管理员先处理")
			return
		}
		status := store.StatusPending
		var approvedAt *time.Time
		if settings.DeviceAutoApprove {
			status = store.StatusActive
			approvedAt = &now
		}
		nd := &store.Device{
			KeyCode:      auth.KeyCode(pub),
			PublicKey:    publicKey,
			Name:         hostname,
			Status:       status,
			MachineID:    machineID,
			Hostname:     hostname,
			OS:           truncate(hello.OS, 16),
			Arch:         truncate(hello.Arch, 16),
			AgentVersion: truncate(hello.AgentVersion, 32),
			LastIP:       ip,
			ApprovedAt:   approvedAt,
		}
		created, err := s.store.AddDevice(nd)
		if err != nil {
			log.Error("创建设备失败", "err", err)
			s.rejectConn(conn, protocol.CodeServerBusy, "服务端保存设备失败")
			return
		}
		dev = *created
		exists = true
		s.audit("system", "device.register", dev.ID, dev.Name, ip, "info")
		s.recordEvent("info", dev.ID, fmt.Sprintf("新设备接入：%s（%s/%s），等待审批", dev.Name, dev.OS, dev.Arch))
		log.Info("新设备接入", "device_id", dev.ID, "key_code", dev.KeyCode, "status", dev.Status)
	}

	// 重新读取，管理员可能在握手期间批准/封禁。
	dev, _ = s.store.GetDevice(dev.ID)
	if dev.Status == store.StatusBlocked {
		s.rejectConn(conn, protocol.CodeBlocked, "设备已被封禁")
		return
	}
	_, _ = s.store.UpdateDevice(dev.ID, func(d *store.Device) error {
		d.Hostname = hostname
		d.OS = truncate(hello.OS, 16)
		d.Arch = truncate(hello.Arch, 16)
		d.AgentVersion = truncate(hello.AgentVersion, 32)
		d.LastIP = ip
		if d.MachineID == "" {
			d.MachineID = machineID
		}
		d.FingerprintConflict = false
		// 密钥码前缀可能随版本变化，认证成功后同步成当前格式
		d.KeyCode = auth.KeyCode(pub)
		return nil
	})

	if dev.Status == store.StatusPending {
		_ = protocol.WriteMessage(conn, &protocol.Pending{
			Type:          protocol.TypePending,
			DeviceID:      dev.ID,
			KeyCode:       dev.KeyCode,
			Message:       "设备待管理员审批",
			RetryAfterSec: agentRetrySec,
		})
		log.Info("设备待审批", "device_id", dev.ID, "key_code", dev.KeyCode)
		return
	}

	// 认证通过：下发 ok，升级为 yamux。
	ok := protocol.OK{
		Type:          protocol.TypeOK,
		DeviceID:      dev.ID,
		KeyCode:       dev.KeyCode,
		ConfigVersion: s.store.ConfigRevision(),
		HeartbeatSec:  int(heartbeatEvery / time.Second),
		ServerTime:    time.Now().Unix(),
	}
	if err := protocol.WriteMessage(conn, &ok); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	mux, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		log.Warn("yamux 初始化失败", "err", err)
		return
	}
	sess := newSession(s, dev.ID, conn, mux, ip)

	s.sessions.set(sess)
	s.stats.sessionsActive.Add(1)
	defer s.stats.sessionsActive.Add(-1)
	s.tunnels.reconcileDevice(dev.ID)

	seen := time.Now().UTC()
	_, _ = s.store.UpdateDevice(dev.ID, func(d *store.Device) error {
		d.LastSeenAt = &seen
		d.LastError = ""
		return nil
	})
	s.recordEvent("info", dev.ID, fmt.Sprintf("设备已连接：%s（%s）", dev.Name, ip))
	log.Info("设备已连接", "device_id", dev.ID, "name", dev.Name, "key_code", dev.KeyCode)

	sess.serve(ctx)

	lastSeen := time.Unix(sess.lastSeen.Load(), 0).UTC()
	_, _ = s.store.UpdateDevice(dev.ID, func(d *store.Device) error {
		d.LastSeenAt = &lastSeen
		return nil
	})
	s.sessions.remove(sess)
	s.tunnels.reconcileDevice(dev.ID)
	s.recordEvent("info", dev.ID, fmt.Sprintf("设备已断开：%s", dev.Name))
	log.Info("设备已断开", "device_id", dev.ID, "name", dev.Name)
}

// rejectConn 发送 reject 帧后关闭连接。
func (s *Server) rejectConn(conn net.Conn, code, msg string) {
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_ = protocol.WriteMessage(conn, &protocol.Reject{Type: protocol.TypeReject, Code: code, Message: msg})
}

func (s *Server) recordEvent(level, deviceID, msg string) {
	s.events.add(web.LogEntry{
		At:     time.Now().UTC(),
		Level:  level,
		Action: "agent",
		Target: deviceID,
		Msg:    msg,
	})
}

func (s *Server) audit(actor, action, target, detail, ip, level string) {
	_ = s.store.AppendAudit(store.AuditEntry{
		At:     time.Now().UTC(),
		Level:  level,
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
		IP:     ip,
	})
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

func remoteHost(c net.Conn) string {
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return c.RemoteAddr().String()
	}
	return host
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// portOf 从 "host:port" 中取端口号，失败返回 0。
func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}
