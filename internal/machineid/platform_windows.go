//go:build windows

package machineid

import (
	"os/exec"
	"strings"
)

// platformID 读取注册表 MachineGuid。
func platformID() string {
	out, err := exec.Command("reg", "query", "HKLM\\SOFTWARE\\Microsoft\\Cryptography", "/v", "MachineGuid").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.EqualFold(fields[0], "MachineGuid") {
			return fields[len(fields)-1]
		}
	}
	return ""
}
