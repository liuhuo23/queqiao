package server

import "sync"

// registry 维护 deviceID -> *session 的在线会话表。
type registry struct {
	mu sync.RWMutex
	m  map[string]*session
}

func newRegistry() *registry { return &registry{m: map[string]*session{}} }

// set 登记会话；同一设备旧会话会被替换（kick）。
func (r *registry) set(s *session) {
	r.mu.Lock()
	old := r.m[s.deviceID]
	r.m[s.deviceID] = s
	r.mu.Unlock()
	if old != nil && old != s {
		go old.kick("replaced")
	}
}

// get 返回在线会话。
func (r *registry) get(deviceID string) *session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[deviceID]
}

// remove 仅当当前登记的就是 s 时删除（避免误删新会话）。
func (r *registry) remove(s *session) {
	r.mu.Lock()
	if r.m[s.deviceID] == s {
		delete(r.m, s.deviceID)
	}
	r.mu.Unlock()
}

// online 判断设备是否在线。
func (r *registry) online(deviceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[deviceID] != nil
}

// onlineIDs 返回在线设备 ID 集合。
func (r *registry) onlineIDs() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]bool, len(r.m))
	for id := range r.m {
		out[id] = true
	}
	return out
}

// snapshot 返回会话快照（用于展示在线时长）。
func (r *registry) snapshot() map[string]*session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*session, len(r.m))
	for id, s := range r.m {
		out[id] = s
	}
	return out
}

// count 返回在线设备数。
func (r *registry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.m)
}

// kickDevice 踢掉指定设备的会话。
func (r *registry) kickDevice(deviceID, reason string) {
	r.mu.RLock()
	s := r.m[deviceID]
	r.mu.RUnlock()
	if s != nil {
		s.kick(reason)
	}
}

// kickAll 踢掉全部会话。
func (r *registry) kickAll(reason string) {
	r.mu.RLock()
	list := make([]*session, 0, len(r.m))
	for _, s := range r.m {
		list = append(list, s)
	}
	r.mu.RUnlock()
	for _, s := range list {
		s.kick(reason)
	}
}
