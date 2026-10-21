// Package logx 提供 slog 初始化。
package logx

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// New 按级别与格式创建 logger。
// format: "text"（默认，适合 journald）或 "json"。
func New(level, format string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	var lv slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lv = slog.LevelDebug
	case "warn", "warning":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}
