package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nrp/internal/store"
	"nrp/internal/version"
)

type sessionCtxKey struct{}

func withSession(ctx context.Context, s *adminSession) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, s)
}

func sessionFrom(r *http.Request) (*adminSession, bool) {
	s, ok := r.Context().Value(sessionCtxKey{}).(*adminSession)
	return s, ok
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": version.String(),
		"time":    time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(s.be.Metrics()))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.loginLimiter.Allow(ip) {
		writeErr(w, NewErr(http.StatusTooManyRequests, "too_many_attempts", "尝试过于频繁，请稍后再试"))
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	av, ok := s.be.VerifyLogin(in.Username, in.Password, ip)
	if !ok {
		writeErr(w, NewErr(http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误"))
		return
	}
	ttl := time.Duration(s.be.GetSettings().AdminSessionTTLSec) * time.Second
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	s.sessions.cleanup()
	token, sess := s.sessions.create(av.ID, av.Username, ttl)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
		MaxAge:   int(ttl.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"admin":      av,
		"csrf_token": sess.csrf,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r)
	if !ok {
		writeErr(w, ErrUnauthorized)
		return
	}
	a, found := s.be.GetAdmin(sess.adminID)
	if !found {
		writeErr(w, ErrUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"admin":      a,
		"csrf_token": sess.csrf,
	})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r)
	if !ok {
		writeErr(w, ErrUnauthorized)
		return
	}
	var in struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if len(in.NewPassword) < 12 {
		writeErr(w, NewErr(http.StatusBadRequest, "invalid_request", "新密码至少 12 个字符"))
		return
	}
	if err := s.be.ChangePassword(sess.adminID, in.OldPassword, in.NewPassword); err != nil {
		writeErr(w, err)
		return
	}
	s.sessions.deleteByAdmin(sess.adminID)
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.be.Overview())
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devices := s.be.Devices()
	if want := strings.TrimSpace(r.URL.Query().Get("status")); want != "" && want != "all" {
		filtered := make([]DeviceView, 0, len(devices))
		for _, d := range devices {
			if d.Status == want {
				filtered = append(filtered, d)
			}
		}
		devices = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (s *Server) actor(r *http.Request) (actor, ip string) {
	if sess, ok := sessionFrom(r); ok {
		actor = sess.username
	}
	return actor, clientIP(r)
}

func (s *Server) handleApproveDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSONOptional(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	actor, ip := s.actor(r)
	d, err := s.be.ApproveDevice(r.PathValue("id"), strings.TrimSpace(in.Name), actor, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleRejectDevice(w http.ResponseWriter, r *http.Request) {
	actor, ip := s.actor(r)
	if err := s.be.RejectDevice(r.PathValue("id"), actor, ip); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleBlockDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Blocked bool `json:"blocked"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	actor, ip := s.actor(r)
	d, err := s.be.SetDeviceBlocked(r.PathValue("id"), in.Blocked, actor, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleRebindDevice(w http.ResponseWriter, r *http.Request) {
	actor, ip := s.actor(r)
	d, err := s.be.RebindDevice(r.PathValue("id"), actor, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name *string `json:"name"`
		Note *string `json:"note"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	actor, ip := s.actor(r)
	d, err := s.be.UpdateDevice(r.PathValue("id"), in.Name, in.Note, actor, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	actor, ip := s.actor(r)
	if err := s.be.DeleteDevice(r.PathValue("id"), actor, ip); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rules": s.be.Rules(r.URL.Query().Get("device_id"))})
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var in RuleInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	actor, ip := s.actor(r)
	rv, err := s.be.CreateRule(in, actor, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rv)
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	var in RulePatch
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	actor, ip := s.actor(r)
	rv, err := s.be.UpdateRule(r.PathValue("id"), in, actor, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rv)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	actor, ip := s.actor(r)
	if err := s.be.DeleteRule(r.PathValue("id"), actor, ip); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCheckPort(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil {
		writeErr(w, NewErr(http.StatusBadRequest, "invalid_request", "端口必须是数字"))
		return
	}
	ok, reason := s.be.CheckPort(port)
	writeJSON(w, http.StatusOK, map[string]any{"port": port, "available": ok, "reason": reason})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "audit"
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": s.be.Logs(kind, limit)})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsJSON(s.be.GetSettings()))
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in SettingsPatch
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	actor, ip := s.actor(r)
	st, err := s.be.UpdateSettings(in, actor, ip)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsJSON(st))
}

func settingsJSON(st store.Settings) map[string]any {
	return map[string]any{
		"public_host":           st.PublicHost,
		"port_range":            st.PortRange,
		"http_addr":             st.HTTPAddr,
		"device_auto_approve":   st.DeviceAutoApprove,
		"max_devices":           st.MaxDevices,
		"max_rules_per_device":  st.MaxRulesPerDevice,
		"admin_session_ttl_sec": st.AdminSessionTTLSec,
		"traffic_stats_enabled": st.TrafficStatsEnabled,
	}
}
