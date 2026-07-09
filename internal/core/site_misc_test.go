package core

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weaveftpd/internal/acl"
	"weaveftpd/internal/user"
)

type bufferConn struct {
	bytes.Buffer
}

func (c *bufferConn) Read(_ []byte) (int, error)         { return 0, nil }
func (c *bufferConn) Close() error                       { return nil }
func (c *bufferConn) LocalAddr() net.Addr                { return dummyAddr("local") }
func (c *bufferConn) RemoteAddr() net.Addr               { return dummyAddr("remote") }
func (c *bufferConn) SetDeadline(_ time.Time) error      { return nil }
func (c *bufferConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *bufferConn) SetWriteDeadline(_ time.Time) error { return nil }

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

func TestHandleSiteChmodDoesNotEscapeStorageRoot(t *testing.T) {
	baseDir := t.TempDir()
	storageDir := filepath.Join(baseDir, "site")
	outsideDir := filepath.Join(baseDir, "outside")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir storage: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	conn := &bufferConn{}
	s := &Session{
		Conn:       conn,
		CurrentDir: "/",
		Config: &Config{
			ACLBasePath: "/",
			Mode:        "local",
			StoragePath: storageDir,
		},
		ACLEngine: loadChmodTestEngine(t),
		User:      &user.User{Name: "siteop", Flags: "1"},
	}

	s.HandleSiteChmod([]string{"777", "../outside/secret.txt"})

	info, err := os.Stat(outsideFile)
	if err != nil {
		t.Fatalf("stat outside file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("outside file mode changed to %o", got)
	}
	if !strings.Contains(conn.String(), "550 CHMOD failed") {
		t.Fatalf("expected failed CHMOD response, got %q", conn.String())
	}
}

func loadChmodTestEngine(t *testing.T) *acl.Engine {
	t.Helper()

	aclPath := filepath.Join(t.TempDir(), "permissions.yml")
	if err := os.WriteFile(aclPath, []byte(`
roles:
  siteop:
    all_flags: ["1"]

rules:
  chmod:
    - path: /*
      required: $siteop
`), 0o644); err != nil {
		t.Fatalf("write permissions: %v", err)
	}
	engine, err := acl.LoadEngine(aclPath)
	if err != nil {
		t.Fatalf("LoadEngine() error = %v", err)
	}
	return engine
}
