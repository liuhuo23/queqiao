//go:build !linux && !darwin && !windows

package machineid

// platformID 在不支持的平台上返回空串，由 MAC 指纹兜底。
func platformID() string { return "" }
