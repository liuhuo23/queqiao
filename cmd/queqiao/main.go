// queqiao 是 鹊桥 客户端：零配置接入服务端，按服务端下发的映射转发流量。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/liuhuo23/queqiao/internal/agent"
	"github.com/liuhuo23/queqiao/internal/agentstate"
	"github.com/liuhuo23/queqiao/internal/agentsvc"
	"github.com/liuhuo23/queqiao/internal/auth"
	"github.com/liuhuo23/queqiao/internal/logx"
	"github.com/liuhuo23/queqiao/internal/version"
)

var usageLines = []string{
	"鹊桥 Agent —— 免配置内网穿透客户端",
	"",
	"用法:",
	"  queqiao [run] [options]             启动并持续连接（默认命令）",
	"  queqiao status                      查看本地状态",
	"  queqiao identity                    显示本机密钥码与公钥",
	"  queqiao reset-identity [-yes]       重置本机身份（会变成新设备，需重新审批）",
	"  queqiao install-service [options]   安装为开机自启服务",
	"  queqiao uninstall-service           卸载系统服务",
	"  queqiao version",
	"",
	"run 参数:",
	"  -s <host:port>     服务端地址（首次运行必填，之后记在本地状态目录）",
	"  -state-dir <dir>   状态目录（默认按系统）",
	"  -allow-lan         允许映射到私有网段地址（默认只允许 127.0.0.1/localhost）",
	"  -ca <file>         用指定 CA 校验证书（默认 TOFU 证书指纹固定）",
	"  -insecure          跳过证书校验（仅调试）",
	"  -log-level <lv>    debug/info/warn/error",
	"  -log-format <fmt>  text/json",
}

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "run":
		cmdRun(args)
	case "status":
		cmdStatus(args)
	case "identity":
		cmdIdentity(args)
	case "reset-identity":
		cmdResetIdentity(args)
	case "install-service":
		cmdInstallService(args)
	case "uninstall-service":
		cmdUninstallService(args)
	case "version", "-version", "--version":
		fmt.Println("queqiao", version.String())
	case "help", "-h", "--help":
		fmt.Print(strings.Join(usageLines, "\n") + "\n")
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", cmd)
		fmt.Print(strings.Join(usageLines, "\n") + "\n")
		os.Exit(2)
	}
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	serverAddr := fs.String("s", "", "服务端地址 host:port")
	stateDir := fs.String("state-dir", "", "状态目录")
	allowLAN := fs.Bool("allow-lan", false, "允许映射到私有网段地址")
	caFile := fs.String("ca", "", "CA 证书文件")
	insecure := fs.Bool("insecure", false, "跳过证书校验（仅调试）")
	logLevel := fs.String("log-level", "info", "日志级别")
	logFormat := fs.String("log-format", "text", "日志格式 text/json")
	_ = fs.Parse(args)

	logger := logx.New(*logLevel, *logFormat, os.Stderr)
	slog.SetDefault(logger)

	opts := agent.Options{
		ServerAddr: *serverAddr,
		StateDir:   *stateDir,
		AllowLAN:   *allowLAN,
		CAFile:     *caFile,
		Insecure:   *insecure,
		Log:        logger,
	}
	a, err := agent.New(opts)
	if err != nil {
		logger.Error("初始化失败", "err", err)
		os.Exit(1)
	}
	if a.ServerAddr() == "" {
		fmt.Fprintln(os.Stderr, "首次运行需要提供服务端地址，例如：")
		fmt.Fprintln(os.Stderr, "  queqiao -s YOUR_SERVER_IP:7600")
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "鹊桥 Agent %s\n  服务端:   %s\n  本机密钥: %s\n  状态目录: %s\n",
		version.String(), a.ServerAddr(), a.KeyCode(), a.StateDir())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil {
		logger.Error("Agent 退出", "err", err)
		os.Exit(1)
	}
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	stateDir := fs.String("state-dir", "", "状态目录")
	_ = fs.Parse(args)
	dir := resolveDir(*stateDir)
	st, err := agentstate.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取状态失败:", err)
		os.Exit(1)
	}
	identity := "未生成"
	if _, err := os.Stat(agentstate.IdentityPath(dir)); err == nil {
		identity = "已生成"
	}
	fmt.Printf("状态目录: %s\n", dir)
	fmt.Printf("服务端:   %s\n", emptyDash(st.ServerAddr))
	fmt.Printf("设备ID:   %s\n", emptyDash(st.DeviceID))
	fmt.Printf("密钥码:   %s\n", emptyDash(st.KeyCode))
	fmt.Printf("身份密钥: %s (%s)\n", identity, agentstate.IdentityPath(dir))
	fmt.Printf("已固定证书: %d 个\n", len(st.TrustPins))
}

func cmdIdentity(args []string) {
	fs := flag.NewFlagSet("identity", flag.ExitOnError)
	stateDir := fs.String("state-dir", "", "状态目录")
	_ = fs.Parse(args)
	dir := resolveDir(*stateDir)
	pub, _, _, err := agentstate.LoadOrCreateIdentity(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载身份失败:", err)
		os.Exit(1)
	}
	fmt.Printf("密钥码: %s\n", auth.KeyCode(pub))
	fmt.Printf("公钥:   %s\n", auth.EncodePublic(pub))
	fmt.Printf("文件:   %s\n", agentstate.IdentityPath(dir))
}

func cmdResetIdentity(args []string) {
	fs := flag.NewFlagSet("reset-identity", flag.ExitOnError)
	stateDir := fs.String("state-dir", "", "状态目录")
	yes := fs.Bool("yes", false, "确认重置")
	_ = fs.Parse(args)
	if !*yes {
		fmt.Fprintln(os.Stderr, "重置后本机将变成一台新设备（需要在管理台重新审批）。确认请加 -yes")
		os.Exit(2)
	}
	dir := resolveDir(*stateDir)
	if err := agentstate.RemoveIdentity(dir); err != nil {
		fmt.Fprintln(os.Stderr, "删除身份文件失败:", err)
		os.Exit(1)
	}
	if st, err := agentstate.Load(dir); err == nil {
		st.DeviceID = ""
		st.KeyCode = ""
		_ = st.Save(dir)
	}
	fmt.Println("已重置身份。下次启动会生成新密钥，并作为新设备等待审批。")
}

func cmdInstallService(args []string) {
	fs := flag.NewFlagSet("install-service", flag.ExitOnError)
	serverAddr := fs.String("s", "", "服务端地址")
	stateDir := fs.String("state-dir", "", "状态目录")
	allowLAN := fs.Bool("allow-lan", false, "允许映射到私有网段地址")
	dryRun := fs.Bool("dry-run", false, "只打印服务配置，不安装")
	_ = fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法定位当前可执行文件:", err)
		os.Exit(1)
	}
	err = agentsvc.Install(agentsvc.InstallOptions{
		ExePath:    exe,
		ServerAddr: *serverAddr,
		StateDir:   *stateDir,
		AllowLAN:   *allowLAN,
		DryRun:     *dryRun,
		Out:        os.Stdout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "安装服务失败:", err)
		os.Exit(1)
	}
}

func cmdUninstallService(args []string) {
	fs := flag.NewFlagSet("uninstall-service", flag.ExitOnError)
	_ = fs.Parse(args)
	if err := agentsvc.Uninstall(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "卸载服务失败:", err)
		os.Exit(1)
	}
}

func resolveDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	dir, err := agentstate.DefaultDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "无法确定状态目录:", err)
		os.Exit(1)
	}
	return dir
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
