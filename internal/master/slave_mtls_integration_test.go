package master

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"weaveftpd/internal/tlsutil"
)

// This test exercises the actual security fix end-to-end: a real
// tls.Listen/tls.Dial pair configured exactly the way SlaveManager.Start()
// configures the slave-control listener (buildListenerTLSConfig), driving
// real TLS handshakes rather than just unit-testing helper functions.

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) issueServerCert(t *testing.T, sans []net.IP) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Test Master"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  sans,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (ca *testCA) issueClientCert(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano() % 1_000_000),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create client cert for CN=%s: %v", cn, err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (ca *testCA) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

func TestSlaveManagerMTLSListenerRejectsWrongOrMissingClientCert(t *testing.T) {
	ca := newTestCA(t)
	serverCert := ca.issueServerCert(t, []net.IP{net.ParseIP("127.0.0.1")})
	slave1Cert := ca.issueClientCert(t, "SLAVE1")
	otherCert := ca.issueClientCert(t, "IMPOSTOR")

	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw})
	caCertPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caCertPath, caCertPEM, 0644); err != nil {
		t.Fatalf("write ca.crt: %v", err)
	}

	sm := NewSlaveManager("127.0.0.1", 0, true, "", "", 60*time.Second)
	if err := sm.ConfigureSlaveMTLS(caCertPath); err != nil {
		t.Fatalf("ConfigureSlaveMTLS: %v", err)
	}
	if !sm.mTLSActive() {
		t.Fatal("expected mTLS to be active once slave_ca_cert is configured")
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", sm.buildListenerTLSConfig(serverCert))
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer ln.Close()

	acceptOnce := func() (*tls.Conn, error) {
		conn, err := ln.Accept()
		if err != nil {
			return nil, err
		}
		tlsConn := conn.(*tls.Conn)
		return tlsConn, tlsConn.Handshake()
	}

	t.Run("correct CN is accepted and identity check passes", func(t *testing.T) {
		errCh := make(chan error, 1)
		go func() {
			tlsConn, err := acceptOnce()
			if err != nil {
				errCh <- err
				return
			}
			defer tlsConn.Close()
			errCh <- tlsutil.VerifyPeerCommonName(tlsConn.ConnectionState(), "SLAVE1")
		}()

		clientCfg := &tls.Config{RootCAs: ca.pool(), Certificates: []tls.Certificate{slave1Cert}, ServerName: "127.0.0.1"}
		conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
		if err != nil {
			t.Fatalf("client dial: %v", err)
		}
		defer conn.Close()

		if err := <-errCh; err != nil {
			t.Fatalf("expected accept + CN match, got: %v", err)
		}
	})

	t.Run("wrong CN handshakes fine but fails the application-level identity check", func(t *testing.T) {
		errCh := make(chan error, 1)
		go func() {
			tlsConn, err := acceptOnce()
			if err != nil {
				errCh <- err
				return
			}
			defer tlsConn.Close()
			errCh <- tlsutil.VerifyPeerCommonName(tlsConn.ConnectionState(), "SLAVE1")
		}()

		clientCfg := &tls.Config{RootCAs: ca.pool(), Certificates: []tls.Certificate{otherCert}, ServerName: "127.0.0.1"}
		conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
		if err != nil {
			t.Fatalf("client dial: %v", err)
		}
		defer conn.Close()

		if err := <-errCh; err == nil {
			t.Fatal("expected CN mismatch to be rejected by the application-level identity check")
		}
	})

	t.Run("missing client cert fails the TLS handshake itself", func(t *testing.T) {
		errCh := make(chan error, 1)
		go func() {
			_, err := acceptOnce()
			errCh <- err
		}()

		clientCfg := &tls.Config{RootCAs: ca.pool(), ServerName: "127.0.0.1"} // no client cert
		conn, dialErr := tls.Dial("tcp", ln.Addr().String(), clientCfg)
		if conn != nil {
			defer conn.Close()
		}

		serverErr := <-errCh
		if dialErr == nil && serverErr == nil {
			t.Fatal("expected the handshake to fail without a client certificate when RequireAndVerifyClientCert is set")
		}
	})
}
