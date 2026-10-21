// Package agentstate 管理 Agent 本地状态：服务端地址、证书固定、身份密钥。
package agentstate

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/liuhuo23/queqiao/internal/auth"
)

const (
	stateFile    = "agent.json"
	identityFile = "identity.json"
)

// State 是 Agent 的非敏感状态。
type State struct {
	ServerAddr string            `json:"server_addr"`
	DeviceID   string            `json:"device_id,omitempty"`
	KeyCode    string            `json:"key_code,omitempty"`
	TrustPins  map[string]string `json:"trust_pins,omitempty"`
}

// Identity 是身份密钥对（私钥只存本机）。
type Identity struct {
	Version    int       `json:"version"`
	PublicKey  string    `json:"public_key"`
	PrivateKey string    `json:"private_key"`
	CreatedAt  time.Time `json:"created_at"`
}

// DefaultDir 返回默认状态目录：所有平台统一为用户主目录下的 .queqiao。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".queqiao"), nil
}

// Load 读取状态（不存在时返回空状态）。
func Load(dir string) (*State, error) {
	st := &State{TrustPins: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return nil, fmt.Errorf("agentstate: 读取状态: %w", err)
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("agentstate: 解析状态: %w", err)
	}
	if st.TrustPins == nil {
		st.TrustPins = map[string]string{}
	}
	return st, nil
}

// Save 原子写入状态文件。
func (s *State) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, stateFile), append(b, '\n'), 0o600)
}

// LoadOrCreateIdentity 加载或生成身份密钥。
func LoadOrCreateIdentity(dir string) (ed25519.PublicKey, ed25519.PrivateKey, *Identity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, nil, err
	}
	path := filepath.Join(dir, identityFile)
	if b, err := os.ReadFile(path); err == nil {
		var id Identity
		if err := json.Unmarshal(b, &id); err != nil {
			return nil, nil, nil, fmt.Errorf("agentstate: 解析身份文件: %w", err)
		}
		priv, err := auth.ParsePrivateKey(id.PrivateKey)
		if err != nil {
			return nil, nil, nil, err
		}
		pub, err := auth.ParsePublicKey(id.PublicKey)
		if err != nil {
			return nil, nil, nil, err
		}
		return pub, priv, &id, nil
	}
	pub, priv, err := auth.Generate()
	if err != nil {
		return nil, nil, nil, err
	}
	id := &Identity{
		Version:    1,
		PublicKey:  auth.EncodePublic(pub),
		PrivateKey: auth.EncodePrivate(priv),
		CreatedAt:  time.Now().UTC(),
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return nil, nil, nil, err
	}
	if err := atomicWrite(path, append(b, '\n'), 0o600); err != nil {
		return nil, nil, nil, err
	}
	return pub, priv, id, nil
}

// IdentityPath 返回身份文件路径。
func IdentityPath(dir string) string { return filepath.Join(dir, identityFile) }

// RemoveIdentity 删除身份文件（重置身份用）。
func RemoveIdentity(dir string) error {
	err := os.Remove(IdentityPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}
