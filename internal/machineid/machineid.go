// Package machineid 计算机器指纹：平台机器码优先，缺失时回落到排序后的物理网卡 MAC。
//
// 只把 SHA-256 哈希发送给服务端，服务端不存裸 MAC。
package machineid

import (
	"crypto/sha256"
	"net"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
)

const hashDomain = "nrp-machine-v1"

// Info 是本机指纹信息。
type Info struct {
	Hash     string // 64 位小写 hex
	Hostname string
	OS       string
	Arch     string
}

var (
	once sync.Once
	info Info
)

// Get 返回本机指纹（结果缓存）。
func Get() Info {
	once.Do(func() {
		host, _ := os.Hostname()
		id := platformID()
		if id == "" {
			id = macFingerprint()
		}
		sum := sha256.Sum256([]byte(hashDomain + "\x00" + id))
		info = Info{
			Hash:     hexEncode(sum[:]),
			Hostname: host,
			OS:       runtime.GOOS,
			Arch:     runtime.GOARCH,
		}
	})
	return info
}

// macFingerprint 返回排序后的物理网卡 MAC 拼接串。
func macFingerprint() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var macs []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || len(iface.HardwareAddr) == 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		// 排除常见虚拟网卡（VPN/容器/虚拟化）。
		if isVirtualIface(name) {
			continue
		}
		macs = append(macs, strings.ToLower(iface.HardwareAddr.String()))
	}
	sort.Strings(macs)
	return strings.Join(macs, ",")
}

func isVirtualIface(name string) bool {
	prefixes := []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "utun", "tun", "tap", "tailscale", "wg", "zt", "lo"}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
