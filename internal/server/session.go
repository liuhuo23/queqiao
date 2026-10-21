package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/liuhuo23/queqiao/internal/protocol"
	"github.com/liuhuo23/queqiao/internal/store"
)

// session 表示一台在线设备的控制会话。
type session struct {
	srv       *Server
	deviceID  string
	conn      net.Conn
	mux       *yamux.Session
	ctrl      net.Conn
	wmu       sync.Mutex
	rulesMu   sync.RWMutex
	rules     map[string]protocol.RuleSpec
	lastSeen  atomic.Int64
	cfgRev    atomic.Int64
	createdAt time.Time
	remoteIP  string
	done      chan struct{}
	closeOnce sync.Once

	// cfgMu 串行化「计算规则 + 更新本地副本 + 发送 config」，避免并发写导致旧配置后到。
	cfgMu sync.Mutex
	// eventMu 限制单设备事件上报频率。
	eventMu     sync.Mutex
	eventCount  int
	eventWindow time.Time
}

func newSession(srv *Server, deviceID string, conn net.Conn, mux *yamux.Session, ip string) *session {
	s := &session{
		srv:       srv,
		deviceID:  deviceID,
		conn:      conn,
		mux:       mux,
		rules:     map[string]protocol.RuleSpec{},
		createdAt: time.Now(),
		remoteIP:  ip,
		done:      make(chan struct{}),
	}
	s.lastSeen.Store(time.Now().Unix())
	return s
}

// serve 是会话主循环：等待控制流、下发配置、维持心跳、接受数据流。
func (s *session) serve(ctx context.Context) {
	defer s.close()

	ctrl, err := s.acceptControl(handshakeTimeout)
	if err != nil {
		s.srv.log.Warn("等待控制流失败", "device_id", s.deviceID, "err", err)
		return
	}
	var kind [1]byte
	_ = ctrl.SetReadDeadline(time.Now().Add(handshakeTimeout))
	if _, err := io.ReadFull(ctrl, kind[:]); err != nil || kind[0] != protocol.StreamControl {
		s.srv.log.Warn("控制流首字节非法", "device_id", s.deviceID)
		return
	}
	_ = ctrl.SetReadDeadline(time.Time{})
	s.setCtrl(ctrl)

	if err := s.pushConfig(); err != nil {
		s.srv.log.Warn("下发配置失败", "device_id", s.deviceID, "err", err)
		return
	}
	go s.readControl()

	hb := time.NewTicker(heartbeatEvery)
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.send(&protocol.Shutdown{Type: protocol.TypeShutdown, Reason: "服务端停机"})
			return
		case <-s.done:
			return
		case <-hb.C:
			if time.Since(time.Unix(s.lastSeen.Load(), 0)) > controlDeadline {
				s.srv.log.Warn("控制流超时，断开设备", "device_id", s.deviceID)
				return
			}
			if err := s.send(&protocol.Ping{Type: protocol.TypePing, TS: time.Now().Unix()}); err != nil {
				return
			}
		}
	}
}

// acceptControl 在超时内等待 Agent 打开的控制流。
func (s *session) acceptControl(timeout time.Duration) (net.Conn, error) {
	type result struct {
		st  *yamux.Stream
		err error
	}
	ch := make(chan result, 1)
	go func() {
		st, err := s.mux.AcceptStream()
		ch <- result{st: st, err: err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.st, nil
	case <-time.After(timeout):
		return nil, errors.New("超时未收到控制流")
	case <-s.done:
		return nil, errors.New("会话已关闭")
	}
}

// setCtrl 登记控制流（与 send/close 用同一把锁，避免数据竞争）。
func (s *session) setCtrl(c net.Conn) {
	s.wmu.Lock()
	s.ctrl = c
	s.wmu.Unlock()
}

// send 写一条控制流消息（串行化写）。
func (s *session) send(v any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.ctrl == nil {
		return errors.New("控制流未就绪")
	}
	_ = s.ctrl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return protocol.WriteMessage(s.ctrl, v)
}

// kick 通知 Agent 后关闭会话。
func (s *session) kick(reason string) {
	_ = s.send(&protocol.Kick{Type: protocol.TypeKick, Reason: reason})
	s.close()
}

// close 幂等关闭会话。
func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.wmu.Lock()
		ctrl := s.ctrl
		s.wmu.Unlock()
		if ctrl != nil {
			_ = ctrl.Close()
		}
		if s.mux != nil {
			_ = s.mux.Close()
		}
		_ = s.conn.Close()
	})
}

// pushConfig 下发全量配置快照（revision + 规则 + 发送是一个原子操作）。
func (s *session) pushConfig() error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	rev := s.srv.store.ConfigRevision()
	rules := s.srv.rulesForDevice(s.deviceID)
	s.setRules(rules)
	return s.send(&protocol.Config{Type: protocol.TypeConfig, Revision: rev, Rules: rules})
}

func (s *session) setRules(rules []protocol.RuleSpec) {
	m := make(map[string]protocol.RuleSpec, len(rules))
	for _, r := range rules {
		m[r.ID] = r
	}
	s.rulesMu.Lock()
	s.rules = m
	s.rulesMu.Unlock()
}

func (s *session) rule(id string) (protocol.RuleSpec, bool) {
	s.rulesMu.RLock()
	defer s.rulesMu.RUnlock()
	r, ok := s.rules[id]
	return r, ok
}

// readControl 读取 Agent 的控制流消息。
func (s *session) readControl() {
	for {
		_ = s.ctrl.SetReadDeadline(time.Now().Add(controlDeadline))
		raw, err := protocol.ReadRaw(s.ctrl)
		if err != nil {
			s.close()
			return
		}
		s.lastSeen.Store(time.Now().Unix())
		typ, _ := protocol.PeekType(raw)
		switch typ {
		case protocol.TypeConfigAck:
			var m protocol.ConfigAck
			if err := json.Unmarshal(raw, &m); err == nil {
				s.cfgRev.Store(m.Revision)
				if !m.OK {
					s.srv.log.Warn("Agent 应用配置失败", "device_id", s.deviceID, "err", m.Error)
				}
			}
		case protocol.TypePong:
			// 已刷新 lastSeen。
		case protocol.TypeStats:
			var m protocol.Stats
			if err := json.Unmarshal(raw, &m); err == nil {
				s.srv.log.Debug("收到 Agent 统计", "device_id", s.deviceID, "rules", len(m.Rules))
			}
		case protocol.TypeEvent:
			var m protocol.Event
			if err := json.Unmarshal(raw, &m); err == nil {
				level := m.Level
				if level == "" {
					level = "info"
				}
				s.srv.recordEvent(level, s.deviceID, m.Msg)
				s.srv.log.Info("Agent 事件", "device_id", s.deviceID, "level", level, "msg", m.Msg)
			}
		}
	}
}

// serveData 把一条公网连接接到设备：打开数据流 -> 写头 -> 等状态 -> 双向转发。
func (s *session) serveData(rule store.Rule, pub net.Conn, inbound io.Reader, preface []byte, cnt *counters, countTraffic bool) {
	defer func() { _ = pub.Close() }()

	stream, err := s.mux.OpenStream()
	if err != nil {
		s.srv.log.Debug("打开数据流失败", "device_id", s.deviceID, "err", err)
		return
	}
	defer func() { _ = stream.Close() }()

	_ = stream.SetDeadline(time.Now().Add(handshakeTimeout))
	if _, err := stream.Write([]byte{protocol.StreamData}); err != nil {
		return
	}
	hdr := protocol.DataHeader{
		RuleID:     rule.ID,
		Proto:      rule.Proto,
		RemoteAddr: pub.RemoteAddr().String(),
	}
	if err := protocol.WriteMessage(stream, &hdr); err != nil {
		return
	}
	var status [1]byte
	if _, err := io.ReadFull(stream, status[:]); err != nil {
		return
	}
	if status[0] != protocol.DataStatusOK {
		return
	}
	if len(preface) > 0 {
		n, err := stream.Write(preface)
		if countTraffic {
			cnt.addIn(int64(n))
			s.srv.stats.bytesIn.Add(int64(n))
		}
		if err != nil {
			return
		}
	}
	_ = stream.SetDeadline(time.Time{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(stream, inbound)
		if countTraffic {
			cnt.addIn(n)
			s.srv.stats.bytesIn.Add(n)
		}
		closeWrite(stream)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(pub, stream)
		if countTraffic {
			cnt.addOut(n)
			s.srv.stats.bytesOut.Add(n)
		}
		closeWrite(pub)
	}()
	wg.Wait()
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

// allowEvent 限制单设备每分钟最多 120 条事件，避免日志洪泛撑爆内存。
func (s *session) allowEvent() bool {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	now := time.Now()
	if now.After(s.eventWindow) {
		s.eventWindow = now.Add(time.Minute)
		s.eventCount = 0
	}
	s.eventCount++
	return s.eventCount <= 120
}
