package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func generateSelfSignedPEM(t *testing.T, cn string) (certPEM []byte, cert *x509.Certificate) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return certPEM, parsed
}

func TestLoadCACertPool(t *testing.T) {
	certPEM, _ := generateSelfSignedPEM(t, "Test CA")
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(path, certPEM, 0644); err != nil {
		t.Fatalf("write ca file: %v", err)
	}

	pool, err := LoadCACertPool(path)
	if err != nil {
		t.Fatalf("LoadCACertPool: %v", err)
	}
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}
}

func TestLoadCACertPool_MissingFile(t *testing.T) {
	if _, err := LoadCACertPool("/nonexistent/path/ca.crt"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadCACertPool_InvalidPEM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.crt")
	if err := os.WriteFile(path, []byte("not a cert"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := LoadCACertPool(path); err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestVerifyPeerCommonName(t *testing.T) {
	_, cert := generateSelfSignedPEM(t, "SLAVE1")

	t.Run("matches", func(t *testing.T) {
		state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		if err := VerifyPeerCommonName(state, "SLAVE1"); err != nil {
			t.Fatalf("expected match, got error: %v", err)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		if err := VerifyPeerCommonName(state, "SLAVE2"); err == nil {
			t.Fatal("expected error for CN mismatch")
		}
	})

	t.Run("no peer cert", func(t *testing.T) {
		state := tls.ConnectionState{}
		if err := VerifyPeerCommonName(state, "SLAVE1"); err == nil {
			t.Fatal("expected error when no peer certificate presented")
		}
	})
}
