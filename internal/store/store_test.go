package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDeviceCRUDAndPersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := s.AddDevice(&Device{PublicKey: "pk1", KeyCode: "NRP-AAAA-BBBB-CCCC", Status: StatusPending, MachineID: "mid1", Hostname: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if dev.ID == "" || dev.CreatedAt.IsZero() {
		t.Fatal("ID/CreatedAt 应自动填充")
	}
	if _, err := s.AddDevice(&Device{PublicKey: "pk1"}); err != ErrConflict {
		t.Fatal("重复公钥应冲突")
	}
	if got, ok := s.GetDeviceByPublicKey("pk1"); !ok || got.ID != dev.ID {
		t.Fatal("按公钥查询失败")
	}
	updated, err := s.UpdateDevice(dev.ID, func(d *Device) error {
		d.Status = StatusActive
		return nil
	})
	if err != nil || updated.Status != StatusActive {
		t.Fatalf("更新失败: %v", err)
	}
	if rev := s.BumpConfigRevision(); rev != 1 || s.ConfigRevision() != 1 {
		t.Fatal("配置版本未递增")
	}
	_ = s.Close()

	// 重新打开验证持久化。
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok := s2.GetDevice(dev.ID)
	if !ok || got.Status != StatusActive || got.MachineID != "mid1" {
		t.Fatal("重新加载后数据不一致")
	}
	if s2.ConfigRevision() != 1 {
		t.Fatal("配置版本未持久化")
	}
}

func TestRulesUniqueness(t *testing.T) {
	s := newStore(t)
	dev, _ := s.AddDevice(&Device{PublicKey: "pk", Status: StatusActive})
	r1, err := s.AddRule(&Rule{DeviceID: dev.ID, Proto: ProtoTCP, LocalAddr: "127.0.0.1:8080", RemotePort: 39001, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.PortInUse(39001, "") {
		t.Fatal("端口应被占用")
	}
	if s.PortInUse(39001, r1.ID) {
		t.Fatal("排除自身后不应被占用")
	}
	if s.PortInUse(39002, "") {
		t.Fatal("未使用端口不应被占用")
	}
	if _, err := s.AddRule(&Rule{DeviceID: dev.ID, Proto: ProtoHTTP, LocalAddr: "127.0.0.1:8080", Domain: "a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if !s.DomainInUse("A.example.com", "") {
		t.Fatal("域名匹配应大小写不敏感")
	}
	if err := s.DeleteRule(r1.ID); err != nil || s.PortInUse(39001, "") {
		t.Fatal("删除规则失败")
	}
}

func TestDeleteDeviceCascadesRules(t *testing.T) {
	s := newStore(t)
	dev, _ := s.AddDevice(&Device{PublicKey: "pk", Status: StatusActive})
	if _, err := s.AddRule(&Rule{DeviceID: dev.ID, Proto: ProtoTCP, LocalAddr: "127.0.0.1:8080", RemotePort: 39003}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(dev.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ListRules("")) != 0 {
		t.Fatal("删除设备应级联删除规则")
	}
}

func TestSettingsAndAudit(t *testing.T) {
	s := newStore(t)
	if s.Settings().MaxDevices != DefaultSettings().MaxDevices {
		t.Fatal("默认设置不正确")
	}
	if _, err := s.UpdateSettings(func(x *Settings) error {
		x.PublicHost = "example.com"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if s.Settings().PublicHost != "example.com" {
		t.Fatal("设置未更新")
	}
	if err := s.AppendAudit(AuditEntry{Action: "device.approve", Target: "dev_1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAudit(AuditEntry{Action: "rule.create"}); err != nil {
		t.Fatal(err)
	}
	entries := s.RecentAudit(10)
	if len(entries) != 2 || entries[0].Action != "rule.create" {
		t.Fatalf("审计顺序错误: %+v", entries)
	}
	if entries[0].At.IsZero() {
		t.Fatal("审计时间未填充")
	}
	// 重新打开后审计文件仍可读取。
	dir := s.Dir()
	_ = s.Close()
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if len(s2.RecentAudit(10)) != 2 {
		t.Fatal("审计未持久化")
	}
	if fi, err := os.Stat(filepath.Join(dir, "audit.jsonl")); err != nil || fi.Size() == 0 {
		t.Fatal("审计文件不存在")
	}
	_ = time.Now()
}
