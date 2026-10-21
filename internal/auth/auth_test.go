package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"regexp"
	"testing"
)

func TestKeyCodeFormat(t *testing.T) {
	pub, _, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	code := KeyCode(pub)
	re := regexp.MustCompile("^QIAO-[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}$")
	if !re.MatchString(code) {
		t.Fatalf("密钥码格式不符: %s", code)
	}
	if code != KeyCode(pub) {
		t.Fatal("密钥码不稳定")
	}
}

func TestSignVerify(t *testing.T) {
	pub, priv, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte("0123456789abcdef0123456789abcdef")
	machineID := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	sig := SignProof(priv, nonce, machineID)

	if !VerifyProof(pub, nonce, machineID, sig) {
		t.Fatal("正确签名应校验通过")
	}
	if VerifyProof(pub, []byte("another-nonce-000000000000000000"), machineID, sig) {
		t.Fatal("不同 nonce 不应通过")
	}
	if VerifyProof(pub, nonce, "deadbeef", sig) {
		t.Fatal("不同机器码不应通过")
	}
	bad := append([]byte(nil), sig...)
	bad[0] ^= 0xff
	if VerifyProof(pub, nonce, machineID, bad) {
		t.Fatal("被篡改的签名不应通过")
	}
}

func TestPublicKeyRoundTrip(t *testing.T) {
	pub, priv, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	encoded := EncodePublic(pub)
	if encoded != base64.StdEncoding.EncodeToString(pub) {
		t.Fatal("公钥编码不一致")
	}
	decoded, err := ParsePublicKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.Equal(pub) {
		t.Fatal("公钥解码不一致")
	}
	privDecoded, err := ParsePrivateKey(EncodePrivate(priv))
	if err != nil {
		t.Fatal(err)
	}
	if !privDecoded.Equal(priv) {
		t.Fatal("私钥解码不一致")
	}
	if _, err := ParsePublicKey("not-base64"); err == nil {
		t.Fatal("非法公钥应报错")
	}
	if _, err := ParsePrivateKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("过短私钥应报错")
	}
	_ = ed25519.PublicKeySize
}
