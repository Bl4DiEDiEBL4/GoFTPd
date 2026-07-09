package slave

import (
	"os"
	"path/filepath"
	"testing"

	"weaveftpd/internal/protocol"
)

// TestHandleRenameRejectsTraversalInToName guards against the RNTO privilege
// escalation where an unsanitized toName (destination filename) containing
// "../" sequences could escape the storage root via a bare filepath.Join.
func TestHandleRenameRejectsTraversalInToName(t *testing.T) {
	siteRoot := t.TempDir()
	outsideDir := t.TempDir()

	srcPath := filepath.Join(siteRoot, "incoming", "file.txt")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.WriteFile(srcPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	s := &Slave{
		roots: []MountedRoot{{Path: siteRoot, MountPath: "/"}},
	}

	// Compute how many "../" hops are needed to reach outsideDir from the
	// resolved destination directory, mirroring what a malicious client
	// would send as the RNTO argument.
	rel, err := filepath.Rel(siteRoot, outsideDir)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	maliciousToName := filepath.ToSlash(filepath.Join(rel, "escaped.txt"))

	resp := s.handleRename(&protocol.AsyncCommand{
		Index: "rename-1",
		Args:  []string{"/incoming/file.txt", "/incoming", maliciousToName},
	})

	errResp, ok := resp.(*protocol.AsyncResponseError)
	if !ok {
		t.Fatalf("expected traversal attempt to be rejected, got %T: %#v", resp, resp)
	}
	if errResp.Message == "" {
		t.Fatalf("expected non-empty error message")
	}

	if _, err := os.Stat(filepath.Join(outsideDir, "escaped.txt")); err == nil {
		t.Fatalf("file escaped storage root to %s", outsideDir)
	}
	if _, err := os.Stat(srcPath); err != nil {
		t.Fatalf("expected source file to remain in place after rejected rename: %v", err)
	}
}

func TestMountedRootContains(t *testing.T) {
	root := MountedRoot{Path: "/data/site"}

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"exact root", "/data/site", true},
		{"child path", "/data/site/incoming/file.txt", true},
		{"escaped via traversal", "/data/site/../../etc/passwd", false},
		{"sibling directory", "/data/site2/file.txt", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := root.contains(tc.path); got != tc.want {
				t.Fatalf("contains(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
