package irc

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

func TestBuildTLSConfigDefaultsToStrict(t *testing.T) {
	b := &Bot{Host: "irc.example.net"}
	cfg, err := b.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("expected verification enabled by default (no InsecureSkipVerify)")
	}
	if cfg.RootCAs != nil {
		t.Fatal("expected no custom RootCAs in strict mode")
	}
	if cfg.ServerName != "irc.example.net" {
		t.Fatalf("expected ServerName to be set, got %q", cfg.ServerName)
	}
}

func TestBuildTLSConfigInsecure(t *testing.T) {
	b := &Bot{Host: "irc.example.net", TLSVerify: "insecure"}
	cfg, err := b.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify=true for explicit insecure opt-out")
	}
}

func TestBuildTLSConfigCustomCA(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(certPath, selfSignedTestCertPEM(t), 0644); err != nil {
		t.Fatalf("write ca file: %v", err)
	}

	b := &Bot{Host: "irc.example.net", TLSVerify: "custom", TLSCACert: certPath}
	cfg, err := b.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("expected verification enabled in custom mode")
	}
	if cfg.RootCAs == nil {
		t.Fatal("expected RootCAs to be populated in custom mode")
	}
}

func TestBuildTLSConfigCustomRequiresCACert(t *testing.T) {
	b := &Bot{Host: "irc.example.net", TLSVerify: "custom"}
	if _, err := b.buildTLSConfig(); err == nil {
		t.Fatal("expected error when tls_verify=custom but tls_ca_cert is empty")
	}
}

func TestBuildTLSConfigInvalidVerifyMode(t *testing.T) {
	b := &Bot{Host: "irc.example.net", TLSVerify: "bogus"}
	if _, err := b.buildTLSConfig(); err == nil {
		t.Fatal("expected error for unrecognized tls_verify value")
	}
}

func TestRedactDebugCommand(t *testing.T) {
	tests := map[string]string{
		"PASS hunter2":                               "PASS [REDACTED]",
		"AUTHENTICATE dXNlcjpzZWNyZXQ=":              "AUTHENTICATE [REDACTED]",
		"OPER sitebot super-secret":                  "OPER sitebot [REDACTED]",
		"PRIVMSG NickServ :IDENTIFY sitebot secret":  "PRIVMSG NickServ :IDENTIFY [REDACTED]",
		"PRIVMSG NickServ :REGISTER secret bot@site": "PRIVMSG NickServ :REGISTER [REDACTED]",
		"PRIVMSG #weave :release complete":           "PRIVMSG #weave :release complete",
	}

	for input, want := range tests {
		if got := redactDebugCommand(input); got != want {
			t.Errorf("redactDebugCommand(%q) = %q, want %q", input, got, want)
		}
	}
}

func selfSignedTestCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test IRC CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
