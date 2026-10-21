// Package agentsvc 负责把 Agent 安装为系统服务（macOS launchd / Linux systemd）。
package agentsvc

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallOptions 是安装服务参数。
type InstallOptions struct {
	ExePath    string
	ServerAddr string
	StateDir   string
	AllowLAN   bool
	DryRun     bool
	Out        io.Writer
}

const (
	launchdLabel = "io.queqiao.agent"
	systemdUnit  = "queqiao.service"
)

// Install 安装并启动系统服务。
func Install(opts InstallOptions) error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(opts)
	case "linux":
		return installSystemd(opts)
	case "windows":
		return installWindows(opts)
	default:
		return fmt.Errorf("暂不支持的系统: %s", runtime.GOOS)
	}
}

// Uninstall 卸载系统服务。
func Uninstall(out io.Writer) error {
	switch runtime.GOOS {
	case "darwin":
		plist := launchdPlistPath()
		_ = exec.Command("launchctl", "bootout", "gui/"+uid(), plist).Run()
		if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(out, "已卸载 launchd 服务:", plist)
		return nil
	case "linux":
		_ = exec.Command("systemctl", "disable", "--now", "queqiao").Run()
		if err := os.Remove(filepath.Join("/etc/systemd/system", systemdUnit)); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
		fmt.Fprintln(out, "已卸载 systemd 服务:", systemdUnit)
		return nil
	default:
		return fmt.Errorf("请手动移除 Windows 服务（sc delete queqiao）")
	}
}

func installLaunchd(opts InstallOptions) error {
	plist := launchdPlistPath()
	args := serviceArgs(opts)
	logDir := filepath.Join(homeDir(), ".queqiao")
	content := launchdPlist(args, filepath.Join(logDir, "queqiao.log"), filepath.Join(logDir, "queqiao.err.log"))
	if opts.DryRun {
		fmt.Fprintln(opts.Out, "# 将写入", plist)
		fmt.Fprintln(opts.Out, content)
		return nil
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(content), 0o644); err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", "gui/"+uid(), plist).Run()
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid(), plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("launchctl", "enable", "gui/"+uid()+"/"+launchdLabel).Run()
	_ = exec.Command("launchctl", "kickstart", "-k", "gui/"+uid()+"/"+launchdLabel).Run()
	fmt.Fprintln(opts.Out, "已安装并启动 launchd 服务:", plist)
	fmt.Fprintln(opts.Out, "日志:", filepath.Join(logDir, "queqiao.log"))
	return nil
}

func installSystemd(opts InstallOptions) error {
	path := filepath.Join("/etc/systemd/system", systemdUnit)
	content := systemdUnitFile(serviceArgs(opts))
	if opts.DryRun {
		fmt.Fprintln(opts.Out, "# 将写入", path)
		fmt.Fprintln(opts.Out, content)
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("写入 unit 文件失败（需要 root）: %w", err)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "queqiao").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable 失败: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintln(opts.Out, "已安装并启动 systemd 服务:", systemdUnit)
	return nil
}

func installWindows(opts InstallOptions) error {
	fmt.Fprintln(opts.Out, "Windows 下请用服务管理器或以下命令安装（示例）：")
	fmt.Fprintf(opts.Out, "  sc create queqiao binPath= \"%s run -s %s\" start= auto\n", opts.ExePath, opts.ServerAddr)
	fmt.Fprintln(opts.Out, "  sc start queqiao")
	return nil
}

func serviceArgs(opts InstallOptions) []string {
	args := []string{opts.ExePath, "run"}
	if opts.ServerAddr != "" {
		args = append(args, "-s", opts.ServerAddr)
	}
	if opts.StateDir != "" {
		args = append(args, "-state-dir", opts.StateDir)
	}
	if opts.AllowLAN {
		args = append(args, "-allow-lan")
	}
	return args
}

func launchdPlist(args []string, stdoutPath, stderrPath string) string {
	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	sb.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	sb.WriteString("<plist version=\"1.0\">\n<dict>\n")
	sb.WriteString("  <key>Label</key><string>" + launchdLabel + "</string>\n")
	sb.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, a := range args {
		sb.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	sb.WriteString("  </array>\n")
	sb.WriteString("  <key>RunAtLoad</key><true/>\n")
	sb.WriteString("  <key>KeepAlive</key><true/>\n")
	sb.WriteString("  <key>StandardOutPath</key><string>" + xmlEscape(stdoutPath) + "</string>\n")
	sb.WriteString("  <key>StandardErrorPath</key><string>" + xmlEscape(stderrPath) + "</string>\n")
	sb.WriteString("</dict>\n</plist>\n")
	return sb.String()
}

func systemdUnitFile(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return "[Unit]\n" +
		"Description=鹊桥 Agent (reverse tunnel client)\n" +
		"After=network-online.target\nWants=network-online.target\n\n" +
		"[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + strings.Join(quoted, " ") + "\n" +
		"Restart=always\nRestartSec=5\n" +
		"LimitNOFILE=65536\n\n" +
		"[Install]\nWantedBy=multi-user.target\n"
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'") {
		return s
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

func launchdPlistPath() string {
	return filepath.Join(homeDir(), "Library", "LaunchAgents", launchdLabel+".plist")
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return h
}

func uid() string {
	return fmt.Sprintf("%d", os.Getuid())
}
