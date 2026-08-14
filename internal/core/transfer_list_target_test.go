package core

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weaveftpd/internal/acl"
	"weaveftpd/internal/user"
)

func TestListRequestTargetArgSkipsListOptions(t *testing.T) {
	target := listRequestTargetArg("LIST", []string{"-al", "/incoming/release"})
	if target != "/incoming/release" {
		t.Fatalf("expected list target after options, got %q", target)
	}
}

func TestListRequestTargetArgSkipsNLSTOptions(t *testing.T) {
	target := listRequestTargetArg("NLST", []string{"-a", "release-file"})
	if target != "release-file" {
		t.Fatalf("expected NLST target after options, got %q", target)
	}
}

func TestResolveListTargetPathUsesPreparedListTarget(t *testing.T) {
	s := &Session{
		CurrentDir: "/archive",
		PretCmd:    "LIST",
		PretArg:    "release",
	}

	target := s.resolveListTargetPath("LIST", nil, nil)
	if target != "/archive/release" {
		t.Fatalf("expected prepared list target to resolve, got %q", target)
	}
}

func TestResolveListTargetPathPrefersExplicitCommandTarget(t *testing.T) {
	s := &Session{
		CurrentDir: "/archive",
		PretCmd:    "LIST",
		PretArg:    "/old",
	}

	target := s.resolveListTargetPath("MLSD", []string{"new"}, nil)
	if target != "/archive/new" {
		t.Fatalf("expected explicit command target to win, got %q", target)
	}
}

func TestResolveSessionPathKeepsAbsoluteTargetsAbsolute(t *testing.T) {
	tests := map[string]string{
		"release":      "/archive/release",
		"../other":     "/other",
		"/MP3/release": "/MP3/release",
		" /FLAC/rel/ ": "/FLAC/rel",
	}
	for in, want := range tests {
		if got := resolveSessionPath("/archive", in); got != want {
			t.Fatalf("resolveSessionPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasPreparedDataConnection(t *testing.T) {
	s := &Session{}
	if s.hasPreparedDataConnection() {
		t.Fatalf("expected empty session to have no prepared data connection")
	}

	s.ActiveAddr = "127.0.0.1:2121"
	if !s.hasPreparedDataConnection() {
		t.Fatalf("expected active address to count as prepared data connection")
	}
}

func TestHasPreparedTransferChannelAcceptsPassthrough(t *testing.T) {
	s := &Session{PassthruSlave: "SLAVE1"}
	if !s.hasPreparedTransferChannel() {
		t.Fatalf("expected passthrough slave to count as prepared transfer channel")
	}
}

func TestValidateListDirectoryTargetHonorsPrivpath(t *testing.T) {
	storage := t.TempDir()
	if err := os.Mkdir(filepath.Join(storage, "PRIVATE"), 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	s := &Session{
		User: &user.User{
			Name:         "regular",
			Flags:        "3",
			PrimaryGroup: "USERS",
			Groups:       map[string]int{"USERS": 0},
		},
		Config: &Config{
			StoragePath: storage,
			ACLBasePath: "/",
		},
		ACLEngine: &acl.Engine{RulesByType: map[string][]acl.Rule{
			"list": {
				{Type: "list", Path: "/*", Requirement: &acl.Requirement{Anyone: true}},
			},
			"privpath": {
				{Type: "privpath", Path: "/PRIVATE", Requirement: &acl.Requirement{Nobody: true}},
			},
		}},
	}

	if err := s.validateListDirectoryTarget("/PRIVATE", nil); err == nil {
		t.Fatal("validateListDirectoryTarget should hide privpath targets")
	}
}

func TestValidateListTargetExistsHonorsPrivpathForFiles(t *testing.T) {
	storage := t.TempDir()
	privateDir := filepath.Join(storage, "PRIVATE")
	if err := os.Mkdir(privateDir, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	s := &Session{
		User: &user.User{
			Name:         "regular",
			Flags:        "3",
			PrimaryGroup: "USERS",
			Groups:       map[string]int{"USERS": 0},
		},
		Config: &Config{
			StoragePath: storage,
			ACLBasePath: "/",
		},
		ACLEngine: &acl.Engine{RulesByType: map[string][]acl.Rule{
			"list": {
				{Type: "list", Path: "/*", Requirement: &acl.Requirement{Anyone: true}},
			},
			"privpath": {
				{Type: "privpath", Path: "/PRIVATE", Requirement: &acl.Requirement{Nobody: true}},
			},
		}},
	}

	if err := s.validateListTargetExists("/PRIVATE/secret.txt", nil); err == nil {
		t.Fatal("validateListTargetExists should hide files below privpath targets")
	}
}

func TestCDUPHonorsPrivpath(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan string, 1)
	go func() {
		buf := make([]byte, 512)
		n, _ := client.Read(buf)
		done <- string(buf[:n])
	}()

	s := &Session{
		Conn:       server,
		CurrentDir: "/PRIVATE/release",
		User: &user.User{
			Name:         "regular",
			Flags:        "3",
			PrimaryGroup: "USERS",
			Groups:       map[string]int{"USERS": 0},
		},
		Config: &Config{
			ACLBasePath: "/",
		},
		ACLEngine: &acl.Engine{RulesByType: map[string][]acl.Rule{
			"list": {
				{Type: "list", Path: "/*", Requirement: &acl.Requirement{Anyone: true}},
			},
			"privpath": {
				{Type: "privpath", Path: "/PRIVATE", Requirement: &acl.Requirement{Nobody: true}},
			},
		}},
	}

	if quit := s.processCommand("CDUP", nil, nil); quit {
		t.Fatal("CDUP should not terminate the session")
	}
	if s.CurrentDir != "/PRIVATE/release" {
		t.Fatalf("expected CurrentDir to stay put, got %q", s.CurrentDir)
	}
	resp := <-done
	if !strings.Contains(resp, "550 /PRIVATE: no such file or directory") {
		t.Fatalf("expected privpath denial response, got %q", resp)
	}
}
