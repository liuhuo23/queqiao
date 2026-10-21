// queqiaod 是 鹊桥 服务端：控制面 + 公网映射 + Web 管理台。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/liuhuo23/queqiao/internal/config"
	"github.com/liuhuo23/queqiao/internal/logx"
	"github.com/liuhuo23/queqiao/internal/server"
	"github.com/liuhuo23/queqiao/internal/store"
	"github.com/liuhuo23/queqiao/internal/version"
)

func main() {
	fs := flag.NewFlagSet("queqiaod", flag.ExitOnError)
	var (
		configPath = fs.String("config", "", "配置文件路径（JSON，可选）")
		dataDir    = fs.String("data-dir", "", "数据目录（覆盖配置文件）")
		control    = fs.String("control-addr", "", "控制端口监听地址，如 :7600")
		webAddr    = fs.String("web-addr", "", "管理台监听地址，如 127.0.0.1:7610")
		publicHost = fs.String("public-host", "", "对公网展示的主机名/IP")
		publicBind = fs.String("public-bind-addr", "", "公网映射监听地址，默认 0.0.0.0")
		httpAddr   = fs.String("http-addr", "", "HTTP 域名映射监听地址，如 :39080（空=关闭）")
		logLevel   = fs.String("log-level", "", "日志级别 debug/info/warn/error")
		logFormat  = fs.String("log-format", "", "日志格式 text/json")
		showVer    = fs.Bool("version", false, "显示版本")
	)
	_ = fs.Parse(os.Args[1:])
	if *showVer {
		fmt.Println("queqiaod", version.String())
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["data-dir"] {
		cfg.DataDir = *dataDir
	}
	if set["control-addr"] {
		cfg.ControlAddr = *control
	}
	if set["web-addr"] {
		cfg.WebAddr = *webAddr
	}
	if set["public-host"] {
		cfg.PublicHost = *publicHost
	}
	if set["public-bind-addr"] {
		cfg.PublicBindAddr = *publicBind
	}
	if set["http-addr"] {
		cfg.HTTPAddr = *httpAddr
	}
	if set["log-level"] {
		cfg.LogLevel = *logLevel
	}
	if set["log-format"] {
		cfg.LogFormat = *logFormat
	}

	logger := logx.New(cfg.LogLevel, cfg.LogFormat, os.Stderr)
	slog.SetDefault(logger)

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		logger.Error("打开数据目录失败", "err", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg, st, logger)
	if err != nil {
		logger.Error("初始化服务端失败", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		logger.Error("启动失败", "err", err)
		os.Exit(1)
	}
	printBanner(srv, cfg)
	if err := srv.Wait(ctx); err != nil {
		logger.Error("服务端退出", "err", err)
		os.Exit(1)
	}
}

func printBanner(srv *server.Server, cfg config.Server) {
	fmt.Fprintf(os.Stderr, "\n鹊桥 服务端 %s 已启动\n", version.String())
	fmt.Fprintf(os.Stderr, "  控制端口: %s (TLS)\n", srv.ControlAddr())
	fmt.Fprintf(os.Stderr, "  管理台:   http://%s\n", srv.WebAddr())
	fmt.Fprintf(os.Stderr, "  数据目录: %s\n", cfg.DataDir)
	fmt.Fprintf(os.Stderr, "\n远程访问管理台（无需开放端口）:\n")
	fmt.Fprintf(os.Stderr, "  ssh -L 7610:%s root@<服务器地址>  然后打开 http://127.0.0.1:7610\n", srv.WebAddr())
	fmt.Fprintf(os.Stderr, "初始管理员密码见数据目录 initial-admin.txt\n\n")
}
