package server

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"nrp/internal/auth"
	"nrp/internal/config"
	"nrp/internal/machineid"
	"nrp/internal/protocol"
	"nrp/internal/store"
)

// newTestServer 在随机端口起一个真实服务端（TLS + 临时目录存储）。
func newTestServer(t *testing.T) (addr string, st *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Defaults()
	cfg.DataDir = dir
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.WebAddr = "127.0.0.1:0"
	cfg.PublicBindAddr = "127.0.0.1"
	cfg.PortRange = [2]int{39100, 39199}
	srv, err := New(cfg, st, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv.ControlAddr(), st
}

// doHandshake 完成一次客户端握手，返回服务端结果类型与错误码。
func doHandshake(t *testing.T, addr, publicKey string, priv ed25519.PrivateKey) (typ, code string) {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"nrp/1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mi := machineid.Get()
	hello := protocol.Hello{
		Type: protocol.TypeHello, Version: protocol.Version,
		PublicKey: publicKey, MachineID: mi.Hash,
		Hostname: "test-host", OS: mi.OS, Arch: mi.Arch, AgentVersion: "test",
	}
	if err := protocol.WriteMessage(conn, &hello); err != nil {
		t.Fatal(err)
	}
	raw, err := protocol.ReadRaw(conn)
	if err != nil {
		t.Fatal(err)
	}
	typ, _ = protocol.PeekType(raw)
	if typ != protocol.TypeChallenge {
		var rej protocol.Reject
		_ = json.Unmarshal(raw, &rej)
		return typ, rej.Code
	}
	var ch protocol.Challenge
	if err := json.Unmarshal(raw, &ch); err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.StdEncoding.DecodeString(ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	sig := auth.SignProof(priv, nonce, mi.Hash)
	if err := protocol.WriteMessage(conn, &protocol.Proof{
		Type: protocol.TypeProof, Signature: base64.StdEncoding.EncodeToString(sig),
	}); err != nil {
		t.Fatal(err)
	}
	raw, err = protocol.ReadRaw(conn)
	if err != nil {
		t.Fatal(err)
	}
	typ, _ = protocol.PeekType(raw)
	if typ == protocol.TypeReject {
		var rej protocol.Reject
		_ = json.Unmarshal(raw, &rej)
		return typ, rej.Code
	}
	return typ, ""
}

// TestHandshakeCanonicalPublicKey 覆盖安全修复：base64 解码会忽略换行，
// 服务端必须只接受规范编码，否则同一私钥可用字符串变体绕过封禁/指纹绑定。
func TestHandshakeCanonicalPublicKey(t *testing.T) {
	addr, _ := newTestServer(t)
	pub, priv, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	canonical := auth.EncodePublic(pub)

	// 1) 规范编码 -> pending（新设备待审批）
	if typ, code := doHandshake(t, addr, canonical, priv); typ != protocol.TypePending {
		t.Fatalf("规范公钥应进入 pending, got type=%s code=%s", typ, code)
	}

	// 2) 同一把私钥，公钥中间插入换行 -> 必须被拒绝
	injected := canonical[:8] + "\r\n" + canonical[8:]
	if typ, code := doHandshake(t, addr, injected, priv); typ != protocol.TypeReject || code != protocol.CodeBadRequest {
		t.Fatalf("非规范公钥编码必须被拒绝, got type=%s code=%s", typ, code)
	}

	// 3) 尾部空格的变体也必须被拒绝
	if typ, code := doHandshake(t, addr, canonical+" ", priv); typ != protocol.TypeReject || code != protocol.CodeBadRequest {
		t.Fatalf("带空白的公钥编码必须被拒绝, got type=%s code=%s", typ, code)
	}
}

// TestRevokedKeyRejected 覆盖“删除设备 = 吊销公钥”：同一私钥不能重新登记。
func TestRevokedKeyRejected(t *testing.T) {
	addr, st := newTestServer(t)
	pub, priv, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	canonical := auth.EncodePublic(pub)
	if typ, code := doHandshake(t, addr, canonical, priv); typ != protocol.TypePending {
		t.Fatalf("首次连接应 pending, got type=%s code=%s", typ, code)
	}
	devices := st.ListDevices()
	if len(devices) != 1 {
		t.Fatalf("应登记 1 台设备, got %d", len(devices))
	}
	if err := st.DeleteDevice(devices[0].ID); err != nil {
		t.Fatal(err)
	}
	if !st.IsKeyRevoked(canonical) {
		t.Fatal("删除设备后公钥应被吊销")
	}
	if typ, code := doHandshake(t, addr, canonical, priv); typ != protocol.TypeReject || code != protocol.CodeBlocked {
		t.Fatalf("被吊销公钥必须被拒绝, got type=%s code=%s", typ, code)
	}
}
