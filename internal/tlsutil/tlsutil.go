// Package tlsutil holds small TLS helpers shared by the master and slave
// mTLS code paths (loading a CA trust pool, and checking a verified peer
// certificate's identity against an expected name).
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadCACertPool reads a PEM-encoded CA certificate (or bundle) from path
// and returns an *x509.CertPool containing it.
func LoadCACertPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no valid certificates found in %q", path)
	}
	return pool, nil
}

// VerifyPeerCommonName checks that the already-verified peer certificate
// chain (state.PeerCertificates, populated by crypto/tls once ClientAuth is
// set to VerifyClientCertIfGiven/RequireAndVerifyClientCert) presents a leaf
// certificate whose Subject.CommonName equals expected.
func VerifyPeerCommonName(state tls.ConnectionState, expected string) error {
	if len(state.PeerCertificates) == 0 {
		return fmt.Errorf("no peer certificate presented")
	}
	cn := state.PeerCertificates[0].Subject.CommonName
	if cn != expected {
		return fmt.Errorf("peer certificate CN %q does not match expected identity %q", cn, expected)
	}
	return nil
}
