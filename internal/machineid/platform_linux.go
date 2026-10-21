//go:build linux

package machineid

import (
	"os"
	"strings"
)

// platformID 读取 /etc/machine-id（或 dbus 的 machine-id）。
func platformID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p)
		if err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}
