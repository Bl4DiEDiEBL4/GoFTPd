package core

import (
	"os"
	"path/filepath"
	"testing"

	"weaveftpd/internal/acl"
	"weaveftpd/internal/user"
)

// TestCanRenamePathEnforcesDestinationACL guards against the RNTO privilege
// escalation where a user who owns a file could RENAMEOWN it to any
// destination because only the source path was ACL-checked. A restrictive
// config here scopes renameown to /incoming/* only; a user owning a file
// under /incoming must not be able to rename it out to /private/*.
func TestCanRenamePathEnforcesDestinationACL(t *testing.T) {
	dir := t.TempDir()
	aclPath := filepath.Join(dir, "permissions.yml")
	if err := os.WriteFile(aclPath, []byte(`
roles:
  anyone:
    anyone: true
  siteop:
    all_flags: ["1"]

rules:
  rename:
    - path: /*
      required: $siteop

  renameown:
    - path: /incoming/*
      required: $anyone
    - path: /*
      required: $siteop
`), 0o644); err != nil {
		t.Fatalf("write %s: %v", aclPath, err)
	}

	engine, err := acl.LoadEngine(aclPath)
	if err != nil {
		t.Fatalf("LoadEngine() error = %v", err)
	}

	s := &Session{
		Config:    &Config{ACLBasePath: "/"},
		ACLEngine: engine,
		User:      &user.User{Name: "leech"},
	}

	// Owns the source file but destination sits outside the renameown-scoped
	// subtree; must be denied even though the user owns the source.
	if s.canRenamePath("/incoming/file.txt", "/private/file.txt") {
		t.Fatal("expected rename to a destination outside the renameown scope to be denied")
	}
}
