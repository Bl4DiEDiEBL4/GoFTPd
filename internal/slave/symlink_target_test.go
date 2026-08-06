package slave

import (
	"path/filepath"
	"testing"
)

func TestVirtualSymlinkTargetMapsPhysicalRootToVFS(t *testing.T) {
	siteRoot := filepath.Join(t.TempDir(), "site")
	s := &Slave{roots: []MountedRoot{{Path: siteRoot, MountPath: "/"}}}

	linkPath := filepath.Join(siteRoot, "!Today_XXX-0DAY")
	targetPath := filepath.Join(siteRoot, "XXX-0DAY", "0516")

	if got := s.virtualSymlinkTarget(linkPath, targetPath); got != "/XXX-0DAY/0516" {
		t.Fatalf("unexpected target: %q", got)
	}
}

func TestVirtualSymlinkTargetMapsRelativeRootTargetToVFS(t *testing.T) {
	siteRoot := filepath.Join(t.TempDir(), "site")
	s := &Slave{roots: []MountedRoot{{Path: siteRoot, MountPath: "/"}}}

	linkPath := filepath.Join(siteRoot, "!Today_XXX-0DAY")
	targetPath := filepath.Join("XXX-0DAY", "0516")

	if got := s.virtualSymlinkTarget(linkPath, targetPath); got != "/XXX-0DAY/0516" {
		t.Fatalf("unexpected target: %q", got)
	}
}

func TestVirtualSymlinkTargetMapsMountedRootToVFS(t *testing.T) {
	baseDir := t.TempDir()
	siteRoot := filepath.Join(baseDir, "site")
	archiveRoot := filepath.Join(baseDir, "DISK1")
	s := &Slave{roots: []MountedRoot{
		{Path: siteRoot, MountPath: "/"},
		{Path: archiveRoot, MountPath: "/ARCHiVE"},
	}}

	linkPath := filepath.Join(siteRoot, "ARCHiVE-LINK")
	targetPath := filepath.Join(archiveRoot, "XXX-0DAY", "0516")

	if got := s.virtualSymlinkTarget(linkPath, targetPath); got != "/ARCHiVE/XXX-0DAY/0516" {
		t.Fatalf("unexpected target: %q", got)
	}
}
