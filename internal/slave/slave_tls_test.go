package slave

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSelfSignedPair(t *testing.T, dir, prefix string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: prefix},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath = filepath.Join(dir, prefix+".crt")
	keyPath = filepath.Join(dir, prefix+".key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0644); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

func TestBuildMasterTLSConfigWithoutCA(t *testing.T) {
	s := &Slave{masterHost: "127.0.0.1"}
	cfg, err := s.buildMasterTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify=true when master_ca_cert is unset (legacy fallback)")
	}
	if cfg.RootCAs != nil {
		t.Fatal("expected no RootCAs when master_ca_cert is unset")
	}
}

func TestBuildMasterTLSConfigWithCA(t *testing.T) {
	dir := t.TempDir()
	caCertPath, _ := writeSelfSignedPair(t, dir, "ca")

	s := &Slave{masterHost: "master.example.net", masterCACert: caCertPath}
	cfg, err := s.buildMasterTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("expected verification enabled (InsecureSkipVerify=false) when master_ca_cert is set")
	}
	if cfg.RootCAs == nil {
		t.Fatal("expected RootCAs to be populated")
	}
	if cfg.ServerName != "master.example.net" {
		t.Fatalf("expected ServerName to default to masterHost, got %q", cfg.ServerName)
	}
}

func TestBuildMasterTLSConfigWithClientCert(t *testing.T) {
	dir := t.TempDir()
	caCertPath, _ := writeSelfSignedPair(t, dir, "ca")
	clientCertPath, clientKeyPath := writeSelfSignedPair(t, dir, "slave1")

	s := &Slave{
		masterHost:   "127.0.0.1",
		masterCACert: caCertPath,
		clientCert:   clientCertPath,
		clientKey:    clientKeyPath,
	}
	cfg, err := s.buildMasterTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected one client certificate to be presented, got %d", len(cfg.Certificates))
	}
}

func TestBuildMasterTLSConfigBadCAPath(t *testing.T) {
	s := &Slave{masterHost: "127.0.0.1", masterCACert: "/nonexistent/ca.crt"}
	if _, err := s.buildMasterTLSConfig(); err == nil {
		t.Fatal("expected error for missing master_ca_cert file")
	}
}
