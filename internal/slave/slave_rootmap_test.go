package slave

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weaveftpd/internal/protocol"
)

func TestRootsForVirtualPathPrefersSpecificMount(t *testing.T) {
	s := &Slave{
		roots: []MountedRoot{
			{Path: "/site", MountPath: "/"},
			{Path: "/archive1", MountPath: "/ARCHiVE"},
			{Path: "/archive2", MountPath: "/ARCHiVE"},
		},
	}

	got := s.rootsForVirtualPath("/ARCHiVE/EBOOKS/release")
	if len(got) != 3 {
		t.Fatalf("expected 3 matching roots, got %d", len(got))
	}
	if got[0].MountPath != "/ARCHiVE" || got[1].MountPath != "/ARCHiVE" {
		t.Fatalf("expected archive mounts first, got %+v", got)
	}
}

func TestGetDirForUploadUsesMountedArchiveRoots(t *testing.T) {
	siteRoot := t.TempDir()
	archiveRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(archiveRoot, "EBOOKS", "0514"), 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}

	s := &Slave{
		roots: []MountedRoot{
			{Path: siteRoot, MountPath: "/"},
			{Path: archiveRoot, MountPath: "/ARCHiVE"},
		},
	}

	fullPath, err := s.getDirForUpload("/ARCHiVE/EBOOKS/0514/release")
	if err != nil {
		t.Fatalf("getDirForUpload failed: %v", err)
	}
	want := filepath.Join(archiveRoot, "EBOOKS", "0514", "release")
	if fullPath != want {
		t.Fatalf("expected %s, got %s", want, fullPath)
	}
}

func TestGetDirForUploadRejectsRootBelowFreeSpaceThreshold(t *testing.T) {
	siteRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(siteRoot, "TV-1080P"), 0o755); err != nil {
		t.Fatalf("mkdir site root: %v", err)
	}
	avail, _ := getDiskSpace(siteRoot)
	if avail <= 0 {
		t.Skip("filesystem free space is unavailable")
	}

	s := &Slave{
		roots: []MountedRoot{
			{Path: siteRoot, MountPath: "/"},
		},
		freeSpaceMB: int(avail/(1024*1024)) + 1,
	}

	_, err := s.getDirForUpload("/TV-1080P/release/file.r00")
	if err == nil {
		t.Fatalf("expected upload to be rejected below free_space_mb")
	}
	if !strings.Contains(err.Error(), "free_space_mb") {
		t.Fatalf("expected free_space_mb error, got %v", err)
	}
}

func TestHandleZipIntegrityReturnsFailedCheckForTruncatedZip(t *testing.T) {
	siteRoot := t.TempDir()
	zipPath := filepath.Join(siteRoot, "0DAY", "bad.zip")
	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		t.Fatalf("mkdir zip dir: %v", err)
	}
	if err := os.WriteFile(zipPath, []byte("not a real zip"), 0o644); err != nil {
		t.Fatalf("write truncated zip: %v", err)
	}

	s := &Slave{
		roots: []MountedRoot{{Path: siteRoot, MountPath: "/"}},
	}
	resp := s.handleZipIntegrity(&protocol.AsyncCommand{
		Index: "zip-check",
		Args:  []string{"/0DAY/bad.zip"},
	})

	got, ok := resp.(*protocol.AsyncResponseZipIntegrity)
	if !ok {
		t.Fatalf("expected zip integrity response, got %T: %#v", resp, resp)
	}
	if got.OK {
		t.Fatalf("expected truncated zip to fail integrity")
	}
}

func TestScanTargetsForBaseIncludeArchiveMountAtRoot(t *testing.T) {
	s := &Slave{
		roots: []MountedRoot{
			{Path: "/site", MountPath: "/"},
			{Path: "/archive1", MountPath: "/ARCHiVE"},
		},
	}

	targets := s.scanTargetsForBase("/", "all")
	if len(targets) != 2 {
		t.Fatalf("expected 2 scan targets, got %d", len(targets))
	}
	if targets[0].virtualBase != "/" || targets[1].virtualBase != "/ARCHiVE" {
		t.Fatalf("expected site root first at / remerge, got %+v", targets)
	}
	if len(targets[0].skipVirtualSubtrees) != 1 || targets[0].skipVirtualSubtrees[0] != "/ARCHiVE" {
		t.Fatalf("expected catch-all root to skip archive subtree, got %+v", targets[0].skipVirtualSubtrees)
	}
}

func TestScanTargetsForSiteOnlyRootSkipsMountedRoots(t *testing.T) {
	s := &Slave{
		roots: []MountedRoot{
			{Path: "/site", MountPath: "/"},
			{Path: "/archive1", MountPath: "/ARCHiVE"},
		},
	}

	targets := s.scanTargetsForBase("/", "normal")
	if len(targets) != 1 {
		t.Fatalf("expected only normal root for site-only remerge, got %+v", targets)
	}
	if targets[0].virtualBase != "/" {
		t.Fatalf("expected site root, got %+v", targets[0])
	}
	if len(targets[0].skipVirtualSubtrees) != 1 || targets[0].skipVirtualSubtrees[0] != "/ARCHiVE" {
		t.Fatalf("expected site root to protect archive subtree, got %+v", targets[0].skipVirtualSubtrees)
	}
}

func TestScanTargetsForArchiveScopeSkipsCatchAllRoot(t *testing.T) {
	s := &Slave{
		roots: []MountedRoot{
			{Path: "/site", MountPath: "/"},
			{Path: "/archive1", MountPath: "/ARCHiVE"},
			{Path: "/archive2", MountPath: "/ARCHiVE"},
		},
	}

	targets := s.scanTargetsForBase("/ARCHiVE", "mounted")
	if len(targets) != 2 {
		t.Fatalf("expected only dedicated archive roots, got %+v", targets)
	}
	for _, target := range targets {
		if target.root.MountPath != "/ARCHiVE" {
			t.Fatalf("did not expect catch-all root in archive scoped remerge, got %+v", targets)
		}
	}
}

func TestSkippedRemergeSubtree(t *testing.T) {
	if !isSkippedRemergeSubtree("/ARCHiVE/TV/release", []string{"/ARCHiVE"}) {
		t.Fatalf("expected archive child to be skipped")
	}
	if isSkippedRemergeSubtree("/ARCHiVE2/TV/release", []string{"/ARCHiVE"}) {
		t.Fatalf("did not expect similarly named sibling to be skipped")
	}
}

func TestWaitForRemergeSlotStopsOnAbort(t *testing.T) {
	s := &Slave{}
	s.online.Store(true)
	s.remergeAbort.Store(true)

	if s.waitForRemergeSlot(remergeScanOptions{}) {
		t.Fatalf("expected remerge slot wait to stop when abort is requested")
	}
}
