// Package version 保存构建版本信息（可用 -ldflags -X 覆盖）。
package version

// Version 由 Makefile 通过 -ldflags 注入。
var Version = "0.1.0-dev"

// String 返回展示用版本号。
func String() string { return Version }
