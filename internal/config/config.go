// Package config 定义服务端配置文件（JSON，便于零依赖解析）。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Server 是 queqiaod 的启动配置。管理台里的设置（Settings）是另一层，存于数据目录。
type Server struct {
	ControlAddr    string `json:"control_addr"`
	WebAddr        string `json:"web_addr"`
	DataDir        string `json:"data_dir"`
	PublicHost     string `json:"public_host"`
	PublicBindAddr string `json:"public_bind_addr"`
	HTTPAddr       string `json:"http_addr"`
	PortRange      [2]int `json:"port_range"`
	TLSCert        string `json:"tls_cert"`
	TLSKey         string `json:"tls_key"`
	LogLevel       string `json:"log_level"`
	LogFormat      string `json:"log_format"`
}

// Defaults 返回默认配置。
func Defaults() Server {
	return Server{
		ControlAddr:    ":7600",
		WebAddr:        "127.0.0.1:7610",
		DataDir:        "/var/lib/queqiao",
		PublicHost:     "",
		PublicBindAddr: "0.0.0.0",
		HTTPAddr:       "",
		PortRange:      [2]int{39000, 40000},
		LogLevel:       "info",
		LogFormat:      "text",
	}
}

// Load 读取 JSON 配置文件。path 为空时返回默认配置；文件不存在且显式指定时报错。
func Load(path string) (Server, error) {
	cfg := Defaults()
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("config: %s 不存在", path)
		}
		return cfg, fmt.Errorf("config: 读取 %s: %w", path, err)
	}
	// 允许字段缺失：先反序列化到默认值之上。
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("config: 解析 %s: %w", path, err)
	}
	if cfg.ControlAddr == "" {
		cfg.ControlAddr = Defaults().ControlAddr
	}
	if cfg.WebAddr == "" {
		cfg.WebAddr = Defaults().WebAddr
	}
	if cfg.DataDir == "" {
		cfg.DataDir = Defaults().DataDir
	}
	if cfg.PublicBindAddr == "" {
		cfg.PublicBindAddr = Defaults().PublicBindAddr
	}
	if cfg.PortRange[0] == 0 || cfg.PortRange[1] == 0 {
		cfg.PortRange = Defaults().PortRange
	}
	return cfg, nil
}
