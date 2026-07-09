package core

import (
	"os"
	"path/filepath"
	"testing"

	"weaveftpd/internal/acl"
	"weaveftpd/internal/user"
)

type ownerACLTestBridge struct {
	entries map[string][]MasterFileEntry
}

func (b ownerACLTestBridge) ListDir(dirPath string) []MasterFileEntry {
	return b.entries[dirPath]
}

func TestCanRenamePathEnforcesDestinationACLForOwnedFiles(t *testing.T) {
	engine := loadOwnerACLTestEngine(t, `
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
`)
	s := &Session{
		Config: &Config{
			ACLBasePath: "/",
			Mode:        "master",
		},
		ACLEngine: engine,
		MasterManager: ownerACLTestBridge{entries: map[string][]MasterFileEntry{
			"/incoming": {
				{Name: "file.txt", Owner: "leech"},
			},
		}},
		User: &user.User{Name: "leech"},
	}

	if !s.canRenamePath("/incoming/file.txt", "/incoming/renamed.txt") {
		t.Fatal("expected owned rename inside renameown scope to be allowed")
	}
	if s.canRenamePath("/incoming/file.txt", "/private/file.txt") {
		t.Fatal("expected owned rename outside destination renameown scope to be denied")
	}
}

func loadOwnerACLTestEngine(t *testing.T, data string) *acl.Engine {
	t.Helper()

	aclPath := filepath.Join(t.TempDir(), "permissions.yml")
	if err := os.WriteFile(aclPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write permissions: %v", err)
	}
	engine, err := acl.LoadEngine(aclPath)
	if err != nil {
		t.Fatalf("LoadEngine() error = %v", err)
	}
	return engine
}
