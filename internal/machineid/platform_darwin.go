//go:build darwin

package machineid

import (
	"os/exec"
	"regexp"
	"strings"
)

var uuidRe = regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)

// platformID 从 ioreg 输出中提取 IOPlatformUUID。
func platformID() string {
	out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return ""
	}
	m := uuidRe.FindSubmatch(out)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}
