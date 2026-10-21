// Package tlsutil 负责服务端 TLS 证书：优先加载给定证书，否则生成并持久化自签证书。
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LoadOrCreate 加载证书；文件不存在时生成自签证书并写入 certFile/keyFile。
func LoadOrCreate(certFile, keyFile, dataDir string, hosts []string) (tls.Certificate, error) {
	if certFile == "" || keyFile == "" {
		certFile = filepath.Join(dataDir, "tls", "server.crt")
		keyFile = filepath.Join(dataDir, "tls", "server.key")
	}
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return tls.Certificate{}, fmt.Errorf("tlsutil: load key pair: %w", err)
			}
			return cert, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsutil: mkdir: %w", err)
	}
	cert, err := generate(hosts)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writePEM(certFile, keyFile, cert); err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

func generate(hosts []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsutil: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsutil: serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "nrp-server", Organization: []string{"NRP"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	seen := map[string]bool{}
	for _, h := range append([]string{"localhost", "nrp-server"}, hosts...) {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsutil: create certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlsutil: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func writePEM(certFile, keyFile string, cert tls.Certificate) error {
	var certPEM []byte
	for _, c := range cert.Certificate {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return fmt.Errorf("tlsutil: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0o644); err != nil {
		return fmt.Errorf("tlsutil: write cert: %w", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		return fmt.Errorf("tlsutil: write key: %w", err)
	}
	return nil
}

// ServerConfig 返回控制端口的 TLS 配置。
func ServerConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"nrp/1"},
	}
}

// Fingerprint 返回证书 SHA-256 指纹（hex），用于 Agent 端 TOFU 固定。
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}
