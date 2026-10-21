// Package auth 负责 Agent 身份：Ed25519 密钥生成、密钥码、挑战-应答签名。
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// proofDomain 是签名域分隔符，防止跨协议重放。
const proofDomain = "nrp-auth-v1"

var (
	// ErrBadPublicKey 表示公钥格式非法。
	ErrBadPublicKey = errors.New("auth: invalid public key")
	// ErrBadPrivateKey 表示私钥格式非法。
	ErrBadPrivateKey = errors.New("auth: invalid private key")
)

// Generate 生成一个新的 Ed25519 密钥对。
func Generate() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: generate key: %w", err)
	}
	return pub, priv, nil
}

// EncodePublic 把公钥编码为 base64（std）。
func EncodePublic(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// EncodePrivate 把私钥编码为 base64（std）。
func EncodePrivate(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv)
}

// ParsePublicKey 解析 base64 公钥。
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, ErrBadPublicKey
	}
	return ed25519.PublicKey(raw), nil
}

// ParsePrivateKey 解析 base64 私钥。
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, ErrBadPrivateKey
	}
	return ed25519.PrivateKey(raw), nil
}

// KeyCode 生成给人看的短密钥码，例如 NRP-7K3F-9Q2M-8XZP。
// 它只是公钥哈希的缩写，不参与认证。
func KeyCode(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(h[:10])
	enc = enc[:12]
	return "NRP-" + enc[0:4] + "-" + enc[4:8] + "-" + enc[8:12]
}

// KeyFingerprint 返回完整公钥指纹（sha256 hex），用于日志排查。
func KeyFingerprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return fmt.Sprintf("%x", h[:])
}

// ProofMessage 构造被签名的字节串。
func ProofMessage(nonce []byte, machineID string) []byte {
	buf := make([]byte, 0, len(proofDomain)+len(nonce)+len(machineID)+2)
	buf = append(buf, proofDomain...)
	buf = append(buf, 0)
	buf = append(buf, nonce...)
	buf = append(buf, 0)
	buf = append(buf, machineID...)
	return buf
}

// SignProof 对 nonce 与 machineID 签名。
func SignProof(priv ed25519.PrivateKey, nonce []byte, machineID string) []byte {
	return ed25519.Sign(priv, ProofMessage(nonce, machineID))
}

// VerifyProof 校验签名。
func VerifyProof(pub ed25519.PublicKey, nonce []byte, machineID string, sig []byte) bool {
	if len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, ProofMessage(nonce, machineID), sig)
}
