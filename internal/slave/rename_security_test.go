package slave

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weaveftpd/internal/protocol"
)

func TestHandleRenameRejectsTraversalInToName(t *testing.T) {
	baseDir := t.TempDir()
	siteRoot := filepath.Join(baseDir, "site")
	outsideDir := filepath.Join(baseDir, "outside")
	srcPath := filepath.Join(siteRoot, "incoming", "file.txt")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	if err := os.WriteFile(srcPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	s := &Slave{
		roots: []MountedRoot{{Path: siteRoot, MountPath: "/"}},
	}

	resp := s.handleRename(&protocol.AsyncCommand{
		Index: "rename-1",
		Args:  []string{"/incoming/file.txt", "/incoming", "../../outside/escaped.txt"},
	})

	errResp, ok := resp.(*protocol.AsyncResponseError)
	if !ok {
		t.Fatalf("expected traversal attempt to be rejected, got %T: %#v", resp, resp)
	}
	if !strings.Contains(errResp.Message, "destination escapes storage root") {
		t.Fatalf("expected storage-root escape error, got %q", errResp.Message)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "escaped.txt")); err == nil {
		t.Fatalf("file escaped storage root to %s", outsideDir)
	}
	if _, err := os.Stat(srcPath); err != nil {
		t.Fatalf("expected source file to remain in place after rejected rename: %v", err)
	}
}

func TestHandleRenameDoesNotCreateDestinationDirectory(t *testing.T) {
	siteRoot := t.TempDir()
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

	resp := s.handleRename(&protocol.AsyncCommand{
		Index: "rename-1",
		Args:  []string{"/incoming/file.txt", "/incoming/newdir", "file.txt"},
	})

	errResp, ok := resp.(*protocol.AsyncResponseError)
	if !ok {
		t.Fatalf("expected missing destination directory to be rejected, got %T: %#v", resp, resp)
	}
	if !strings.Contains(errResp.Message, "destination directory not found") {
		t.Fatalf("expected destination directory error, got %q", errResp.Message)
	}
	if _, err := os.Stat(filepath.Join(siteRoot, "incoming", "newdir")); !os.IsNotExist(err) {
		t.Fatalf("destination directory was created unexpectedly: %v", err)
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
