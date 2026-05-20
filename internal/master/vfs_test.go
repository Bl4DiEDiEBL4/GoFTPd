package master

import (
	"strings"
	"testing"
	"time"

	"goftpd/internal/plugin"
)

func TestVFSListDirectoryUsesDirectChildren(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site/MP3", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/MP3/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/MP3/release/file1.rar", VFSFile{Size: 123, Seen: true})
	vfs.AddFile("/site/MP3/release/Sample", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/MP3/release/Sample/sample.mkv", VFSFile{Size: 456, Seen: true})

	children := vfs.ListDirectory("/site/MP3/release")
	if len(children) != 2 {
		t.Fatalf("expected 2 direct children, got %d", len(children))
	}

	got := map[string]bool{}
	for _, child := range children {
		got[child.Path] = true
	}
	if !got["/site/MP3/release/file1.rar"] {
		t.Fatalf("expected file child to be listed")
	}
	if !got["/site/MP3/release/Sample"] {
		t.Fatalf("expected sample dir child to be listed")
	}
	if got["/site/MP3/release/Sample/sample.mkv"] {
		t.Fatalf("did not expect nested sample file to be listed")
	}
}

func TestVFSRenameAndDeleteKeepChildrenIndexInSync(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site/GAMES/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/GAMES/release/file1.r00", VFSFile{Size: 1, Seen: true})
	vfs.AddFile("/site/GAMES/release/file1.r01", VFSFile{Size: 1, Seen: true})

	vfs.RenameFile("/site/GAMES/release", "/site/GAMES/renamed")

	children := vfs.ListDirectory("/site/GAMES/renamed")
	if len(children) != 2 {
		t.Fatalf("expected 2 direct children after rename, got %d", len(children))
	}
	for _, child := range children {
		if !strings.HasPrefix(child.Path, "/site/GAMES/renamed") {
			t.Fatalf("expected renamed child path, got %s", child.Path)
		}
	}

	vfs.DeleteFile("/site/GAMES/renamed/file1.r00")
	children = vfs.ListDirectory("/site/GAMES/renamed")
	if len(children) != 1 {
		t.Fatalf("expected 1 child after delete, got %d", len(children))
	}
	if children[0].Path != "/site/GAMES/renamed/file1.r01" {
		t.Fatalf("expected remaining child to be file1.r01, got %s", children[0].Path)
	}
}

func TestVFSRaceStatsUseCachedDirectChildren(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site/FLAC/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVData("/site/FLAC/release", "release.sfv", map[string]uint32{
		"01-track.flac": 1,
		"02-track.flac": 2,
		"03-track.flac": 3,
	})

	vfs.AddFile("/site/FLAC/release/01-track.flac", VFSFile{
		Size:     100,
		Seen:     true,
		Owner:    "steel",
		Group:    "Admin",
		XferTime: 1000,
	})
	vfs.AddFile("/site/FLAC/release/02-track.flac", VFSFile{
		Size:     200,
		Seen:     true,
		Owner:    "steel",
		Group:    "Admin",
		XferTime: 2000,
	})
	vfs.AddFile("/site/FLAC/release/Sample", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/FLAC/release/Sample/sample.flac", VFSFile{
		Size:     999,
		Seen:     true,
		Owner:    "other",
		Group:    "Other",
		XferTime: 500,
	})

	users, groups, totalBytes, present, total := vfs.GetRaceStats("/site/FLAC/release")
	if total != 3 {
		t.Fatalf("expected total 3, got %d", total)
	}
	if present != 2 {
		t.Fatalf("expected present 2, got %d", present)
	}
	if totalBytes != 300 {
		t.Fatalf("expected total bytes 300, got %d", totalBytes)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Name != "steel" {
		t.Fatalf("expected uploader steel, got %s", users[0].Name)
	}
	if users[0].Files != 2 {
		t.Fatalf("expected 2 files for steel, got %d", users[0].Files)
	}
	if users[0].DurationMs != 3000 {
		t.Fatalf("expected duration 3000ms, got %d", users[0].DurationMs)
	}
	if users[0].Speed != 150 {
		t.Fatalf("expected aggregate user speed 150 bytes/s, got %f", users[0].Speed)
	}
	if users[0].Percent != 66 {
		t.Fatalf("expected 66 percent, got %d", users[0].Percent)
	}
	if len(groups) != 1 || groups[0].Name != "Admin" {
		t.Fatalf("expected Admin group stats, got %+v", groups)
	}
	if groups[0].Speed != 150 {
		t.Fatalf("expected aggregate group speed 150 bytes/s, got %f", groups[0].Speed)
	}
}

func TestVFSRaceStatsRefreshAfterDelete(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site/MP3/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVData("/site/MP3/release", "release.sfv", map[string]uint32{
		"01-track.mp3": 1,
		"02-track.mp3": 2,
	})
	vfs.AddFile("/site/MP3/release/01-track.mp3", VFSFile{
		Size:     100,
		Seen:     true,
		Owner:    "n0pe",
		Group:    "Admin",
		XferTime: 1000,
	})
	vfs.AddFile("/site/MP3/release/02-track.mp3", VFSFile{
		Size:     200,
		Seen:     true,
		Owner:    "n0pe",
		Group:    "Admin",
		XferTime: 1000,
	})

	_, _, _, present, total := vfs.GetRaceStats("/site/MP3/release")
	if present != 2 || total != 2 {
		t.Fatalf("expected full race state before delete, got present=%d total=%d", present, total)
	}

	vfs.DeleteFile("/site/MP3/release/02-track.mp3")

	users, _, totalBytes, present, total := vfs.GetRaceStats("/site/MP3/release")
	if present != 1 || total != 2 {
		t.Fatalf("expected partial race state after delete, got present=%d total=%d", present, total)
	}
	if totalBytes != 100 {
		t.Fatalf("expected total bytes 100 after delete, got %d", totalBytes)
	}
	if len(users) != 1 || users[0].Files != 1 {
		t.Fatalf("expected one surviving user/file after delete, got %+v", users)
	}
}

func TestVFSRaceStatsIgnoreChecksumMismatches(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVData("/site/X265/release", "release.sfv", map[string]uint32{
		"good.r00": 1,
		"bad.r01":  2,
	})
	vfs.AddFile("/site/X265/release/good.r00", VFSFile{
		Size:     100,
		Seen:     true,
		Owner:    "n0pe",
		Group:    "Admin",
		XferTime: 1000,
		Checksum: 1,
	})
	vfs.AddFile("/site/X265/release/bad.r01", VFSFile{
		Size:     200,
		Seen:     true,
		Owner:    "n0pe",
		Group:    "Admin",
		XferTime: 1000,
		Checksum: 999,
	})

	users, groups, totalBytes, present, total := vfs.GetRaceStats("/site/X265/release")
	if total != 2 || present != 1 {
		t.Fatalf("expected only checksum-valid file to count, got present=%d total=%d", present, total)
	}
	if totalBytes != 100 {
		t.Fatalf("expected only good file bytes to count, got %d", totalBytes)
	}
	if len(users) != 1 || users[0].Files != 1 {
		t.Fatalf("expected one valid user file, got %+v", users)
	}
	if len(groups) != 1 || groups[0].Files != 1 {
		t.Fatalf("expected one valid group file, got %+v", groups)
	}

	verified := vfs.GetVerifiedSFVPresentFiles("/site/X265/release")
	if len(verified) != 1 || !verified["good.r00"] || verified["bad.r01"] {
		t.Fatalf("expected only checksum-valid file in verified set, got %+v", verified)
	}
}

func TestVFSDeleteDirRemovesSubtreeAndMetadataWithoutRebuild(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site/TV/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/TV/release/file1.r00", VFSFile{Size: 100, Seen: true})
	vfs.AddFile("/site/TV/release/Sample", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/site/TV/release/Sample/sample.mkv", VFSFile{Size: 200, Seen: true})
	vfs.SetSFVData("/site/TV/release", "release.sfv", map[string]uint32{"file1.r00": 1})

	vfs.DeleteFile("/site/TV/release")

	if got := vfs.GetFile("/site/TV/release"); got != nil {
		t.Fatalf("expected deleted release dir to be gone, got %+v", got)
	}
	if got := vfs.GetFile("/site/TV/release/file1.r00"); got != nil {
		t.Fatalf("expected deleted release file to be gone, got %+v", got)
	}
	if got := vfs.GetFile("/site/TV/release/Sample/sample.mkv"); got != nil {
		t.Fatalf("expected deleted nested sample file to be gone, got %+v", got)
	}
	if got := vfs.GetSFVData("/site/TV/release"); got != nil {
		t.Fatalf("expected deleted release sfv metadata to be gone, got %+v", got)
	}
	children := vfs.ListDirectory("/site/TV")
	if len(children) != 0 {
		t.Fatalf("expected parent directory to have no children after delete, got %d", len(children))
	}
}

func TestVFSDeleteSFVClearsParentSFVMetadata(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/X265/release/release.sfv", VFSFile{Seen: true, Size: 10})
	vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{"file.r00": 1})

	vfs.DeleteFile("/X265/release/release.sfv")

	meta := vfs.GetSFVData("/X265/release")
	if meta != nil {
		t.Fatalf("expected sfv metadata to be removed after deleting sfv, got %+v", meta)
	}
}

func TestVFSRenameSFVAwayClearsParentSFVMetadata(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X264/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/X264/release/release.sfv", VFSFile{Seen: true, Size: 10})
	vfs.SetSFVData("/X264/release", "release.sfv", map[string]uint32{"file.r00": 1})

	vfs.RenameFile("/X264/release/release.sfv", "/X264/release/release.txt")

	meta := vfs.GetSFVData("/X264/release")
	if meta != nil {
		t.Fatalf("expected sfv metadata to be removed after renaming sfv away, got %+v", meta)
	}
}

func TestVFSPurgeUnseenChildrenRemovesGhostFilesForScannedDir(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/X265/release/wou.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/X265/release/wou.r01", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/X265/release/Sample", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/X265/release/Sample/sample.mkv", VFSFile{Size: 200, Seen: true, SlaveName: "LOCAL"})

	vfs.MarkAllUnseen("LOCAL")

	// Simulate a remerge batch for /X265/release where only wou.r00 and Sample still exist.
	vfs.AddFile("/X265/release/wou.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/X265/release/Sample", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	vfs.PurgeUnseenChildren("LOCAL", "/X265/release")

	if got := vfs.GetFile("/X265/release/wou.r01"); got != nil {
		t.Fatalf("expected stale direct child file to be purged, got %+v", got)
	}
	if got := vfs.GetFile("/X265/release/Sample"); got == nil {
		t.Fatalf("expected surviving direct child dir to remain")
	}
	if got := vfs.GetFile("/X265/release/Sample/sample.mkv"); got == nil {
		t.Fatalf("expected nested child to remain until its own directory batch is remerged")
	}
	children := vfs.ListDirectory("/X265/release")
	if len(children) != 2 {
		t.Fatalf("expected 2 direct children after purge, got %d", len(children))
	}
}

func TestVFSScopedRemergePurgesUnseenSubtreeOnly(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/TV-1080P", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/TV-1080P/old", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/TV-1080P/old/file.r00", VFSFile{Size: 100, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/TV-1080P/keep", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	vfs.AddFile("/X265/other", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})

	vfs.MarkSubtreeUnseen("LOCAL", "/TV-1080P")
	vfs.AddFile("/TV-1080P/keep", VFSFile{IsDir: true, Seen: true, SlaveName: "LOCAL"})
	vfs.PurgeUnseenSubtree("LOCAL", "/TV-1080P")

	if got := vfs.GetFile("/TV-1080P/old"); got != nil {
		t.Fatalf("expected scoped ghost dir to be purged, got %+v", got)
	}
	if got := vfs.GetFile("/TV-1080P/old/file.r00"); got != nil {
		t.Fatalf("expected scoped ghost file to be purged, got %+v", got)
	}
	if got := vfs.GetFile("/TV-1080P/keep"); got == nil {
		t.Fatalf("expected re-seen scoped dir to remain")
	}
	if got := vfs.GetFile("/X265/other"); got == nil {
		t.Fatalf("expected unrelated dir outside scoped path to remain")
	}
}

func TestParentDirModTimeBubblesOnChanges(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/site", VFSFile{IsDir: true, Seen: true, LastModified: 1})
	vfs.AddFile("/site/MP3", VFSFile{IsDir: true, Seen: true, LastModified: 1})
	vfs.AddFile("/site/MP3/release", VFSFile{IsDir: true, Seen: true, LastModified: 1})

	beforeSection := vfs.GetFile("/site/MP3").LastModified
	beforeRelease := vfs.GetFile("/site/MP3/release").LastModified

	time.Sleep(1100 * time.Millisecond)
	vfs.AddFile("/site/MP3/release/01-track.mp3", VFSFile{Size: 123, Seen: true})

	afterSection := vfs.GetFile("/site/MP3").LastModified
	afterRelease := vfs.GetFile("/site/MP3/release").LastModified
	if afterSection <= beforeSection {
		t.Fatalf("expected section modtime to increase, got %d <= %d", afterSection, beforeSection)
	}
	if afterRelease <= beforeRelease {
		t.Fatalf("expected release modtime to increase, got %d <= %d", afterRelease, beforeRelease)
	}

	time.Sleep(1100 * time.Millisecond)
	deleteBefore := vfs.GetFile("/site/MP3/release").LastModified
	vfs.DeleteFile("/site/MP3/release/01-track.mp3")
	deleteAfter := vfs.GetFile("/site/MP3/release").LastModified
	if deleteAfter <= deleteBefore {
		t.Fatalf("expected delete to bump release modtime, got %d <= %d", deleteAfter, deleteBefore)
	}
}

func TestVFSAddFilePreservesRemergeDirectoryModTimes(t *testing.T) {
	vfs := NewVirtualFileSystem()

	vfs.AddFile("/0DAY", VFSFile{IsDir: true, Seen: true, LastModified: 100})
	vfs.AddFile("/0DAY/2026-04-27", VFSFile{IsDir: true, Seen: true, LastModified: 200})
	vfs.AddFile("/0DAY/2026-04-27/Release-GRP", VFSFile{IsDir: true, Seen: true, LastModified: 300})
	vfs.AddFile("/0DAY/2026-04-27/Release-GRP/file.r00", VFSFile{Seen: true, Size: 123, LastModified: 250})

	if got := vfs.GetFile("/0DAY").LastModified; got != 300 {
		t.Fatalf("expected section modtime to keep newest seen child 300, got %d", got)
	}
	if got := vfs.GetFile("/0DAY/2026-04-27").LastModified; got != 300 {
		t.Fatalf("expected dated dir modtime to stay 300, got %d", got)
	}
	if got := vfs.GetFile("/0DAY/2026-04-27/Release-GRP").LastModified; got != 300 {
		t.Fatalf("expected release dir modtime to stay 300, got %d", got)
	}
}

func TestVFSRelocateFileMovesOwnership(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/2026-04-26/release", VFSFile{IsDir: true, Seen: true, SlaveName: "SLAVE1"})
	vfs.AddFile("/0DAY/2026-04-26/release/file1.zip", VFSFile{Seen: true, SlaveName: "SLAVE1", Size: 100})
	vfs.SetSFVData("/0DAY/2026-04-26/release", "release.sfv", map[string]uint32{"file1.zip": 1})

	vfs.RelocateFile("/0DAY/2026-04-26/release", "/ARCHiVE/0DAY/release", "SLAVE2")

	if vfs.GetFile("/0DAY/2026-04-26/release") != nil {
		t.Fatalf("expected source path to be gone after relocate")
	}
	dst := vfs.GetFile("/ARCHiVE/0DAY/release")
	if dst == nil || dst.SlaveName != "SLAVE2" {
		t.Fatalf("expected relocated dir on SLAVE2, got %+v", dst)
	}
	child := vfs.GetFile("/ARCHiVE/0DAY/release/file1.zip")
	if child == nil || child.SlaveName != "SLAVE2" {
		t.Fatalf("expected relocated child on SLAVE2, got %+v", child)
	}
	if meta := vfs.GetSFVData("/ARCHiVE/0DAY/release"); meta == nil || meta.SFVEntries["file1.zip"] != 1 {
		t.Fatalf("expected sfv metadata to move with relocate")
	}
}

func TestSetProtectedDirsPrunesStaleUnownedRootDirs(t *testing.T) {
	vfs := NewVirtualFileSystem()

	vfs.files["/X264"] = &VFSFile{Path: "/X264", IsDir: true, Seen: true, SlaveName: "", LastModified: 100}
	vfs.files["/X265"] = &VFSFile{Path: "/X265", IsDir: true, Seen: true, SlaveName: "", LastModified: 100}

	vfs.SetProtectedDirs([]string{"/X265"})

	if stale := vfs.GetFile("/X264"); stale != nil {
		t.Fatalf("expected stale unconfigured root dir /X264 to be purged, got %+v", stale)
	}
	kept := vfs.GetFile("/X265")
	if kept == nil {
		t.Fatalf("expected configured protected dir /X265 to remain")
	}
	if kept.SlaveName != "" {
		t.Fatalf("expected protected dir /X265 to be detached from slave ownership, got %+v", kept)
	}
}

func TestSetHiddenPathsPrunesAndHidesSubtrees(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/ARCHiVE", VFSFile{Path: "/ARCHiVE", IsDir: true, Seen: true})
	vfs.AddFile("/ARCHiVE/_incoming", VFSFile{Path: "/ARCHiVE/_incoming", IsDir: true, Seen: true})
	vfs.AddFile("/ARCHiVE/_incoming/release", VFSFile{Path: "/ARCHiVE/_incoming/release", IsDir: true, Seen: true})
	vfs.AddFile("/ARCHiVE/visible", VFSFile{Path: "/ARCHiVE/visible", IsDir: true, Seen: true})

	vfs.SetHiddenPaths([]string{"/ARCHiVE/_incoming"})

	if got := vfs.GetFile("/ARCHiVE/_incoming"); got != nil {
		t.Fatalf("expected hidden path to be absent from VFS, got %+v", got)
	}
	if got := vfs.GetFile("/ARCHiVE/_incoming/release"); got != nil {
		t.Fatalf("expected hidden subtree path to be absent from VFS, got %+v", got)
	}

	children := vfs.ListDirectory("/ARCHiVE")
	if len(children) != 1 || children[0].Path != "/ARCHiVE/visible" {
		t.Fatalf("expected only visible child after hide, got %+v", children)
	}
}

func TestSetExcludePathsPrunesAndSkipsSubtrees(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/ARCHiVE", VFSFile{Path: "/ARCHiVE", IsDir: true, Seen: true})
	vfs.AddFile("/ARCHiVE/backup", VFSFile{Path: "/ARCHiVE/backup", IsDir: true, Seen: true})
	vfs.AddFile("/ARCHiVE/backup/release", VFSFile{Path: "/ARCHiVE/backup/release", IsDir: true, Seen: true})
	vfs.AddFile("/ARCHiVE/visible", VFSFile{Path: "/ARCHiVE/visible", IsDir: true, Seen: true})

	vfs.SetExcludePaths([]string{"/ARCHiVE/backup"})

	if got := vfs.GetFile("/ARCHiVE/backup"); got != nil {
		t.Fatalf("expected excluded path to be absent from VFS, got %+v", got)
	}
	if got := vfs.GetFile("/ARCHiVE/backup/release"); got != nil {
		t.Fatalf("expected excluded subtree path to be absent from VFS, got %+v", got)
	}

	children := vfs.ListDirectory("/ARCHiVE")
	if len(children) != 1 || children[0].Path != "/ARCHiVE/visible" {
		t.Fatalf("expected only visible child after exclude, got %+v", children)
	}

	vfs.AddFile("/ARCHiVE/backup/new-release", VFSFile{Path: "/ARCHiVE/backup/new-release", IsDir: true, Seen: true})
	if got := vfs.GetFile("/ARCHiVE/backup/new-release"); got != nil {
		t.Fatalf("expected new excluded path to be ignored, got %+v", got)
	}
}

func TestVFSResolvePathFollowsSymlinkSegments(t *testing.T) {
	vfs := NewVirtualFileSystem()

	vfs.AddFile("/FLAC", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/FLAC/2026-04-27", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/FLAC/2026-04-27/Release-GRP", VFSFile{IsDir: true, Seen: true})
	vfs.AddSymlink("/!Today_FLAC", "/FLAC/2026-04-27")

	got := vfs.ResolvePath("/!Today_FLAC/Release-GRP/file.r01")
	want := "/FLAC/2026-04-27/Release-GRP/file.r01"
	if got != want {
		t.Fatalf("expected resolved path %s, got %s", want, got)
	}
}

func TestVFSAddSymlinkKeepsTargetType(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/X265/release/file.r00", VFSFile{Size: 100, Seen: true})

	vfs.AddSymlink("/X265/release-link", "/X265/release")
	if got := vfs.GetFile("/X265/release-link"); got == nil || !got.IsSymlink || !got.IsDir {
		t.Fatalf("expected directory symlink, got %+v", got)
	}

	vfs.AddSymlink("/X265/file-link", "/X265/release/file.r00")
	if got := vfs.GetFile("/X265/file-link"); got == nil || !got.IsSymlink || got.IsDir {
		t.Fatalf("expected file symlink, got %+v", got)
	}
}

func TestVFSAddFilePreservesVerifiedTransferDataAcrossRemerge(t *testing.T) {
	vfs := NewVirtualFileSystem()

	vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:         476800000,
		LastModified: 1714930000,
		Seen:         true,
		SlaveName:    "LOCAL",
		Checksum:     12345,
		XferTime:     5000,
	})

	vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:         476800000,
		LastModified: 1714930000,
		Seen:         true,
		SlaveName:    "LOCAL",
	})

	got := vfs.GetFile("/X265/release/file.r00")
	if got == nil {
		t.Fatalf("expected file to remain present")
	}
	if got.Checksum != 12345 {
		t.Fatalf("expected checksum to survive unchanged remerge, got %d", got.Checksum)
	}
	if got.XferTime != 5000 {
		t.Fatalf("expected xfertime to survive unchanged remerge, got %d", got.XferTime)
	}
}

func TestVFSAddFilePreservesStrongOwnerAcrossRescan(t *testing.T) {
	vfs := NewVirtualFileSystem()

	vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:      476800000,
		Seen:      true,
		SlaveName: "LOCAL",
		Owner:     "Neptun",
		Group:     "iND",
		Checksum:  12345,
		XferTime:  5000,
	})

	vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:      476800000,
		Seen:      true,
		SlaveName: "LOCAL",
		Owner:     "GoFTPd",
		Group:     "root",
	})

	got := vfs.GetFile("/X265/release/file.r00")
	if got == nil {
		t.Fatalf("expected file to remain present")
	}
	if got.Owner != "Neptun" || got.Group != "iND" {
		t.Fatalf("expected strong owner/group to survive rescan, got %s/%s", got.Owner, got.Group)
	}
	if got.Checksum != 12345 || got.XferTime != 5000 {
		t.Fatalf("expected transfer metadata to survive rescan, checksum=%d xfer=%d", got.Checksum, got.XferTime)
	}
}

func TestVFSAddFilePreservesVerifiedTransferDataAcrossRemergeMtimeChange(t *testing.T) {
	vfs := NewVirtualFileSystem()

	vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:         476800000,
		LastModified: 1714930000,
		Seen:         true,
		SlaveName:    "LOCAL",
		Checksum:     12345,
		XferTime:     5000,
	})

	vfs.AddFile("/X265/release/file.r00", VFSFile{
		Size:         476800000,
		LastModified: 1714931234,
		Seen:         true,
		SlaveName:    "LOCAL",
	})

	got := vfs.GetFile("/X265/release/file.r00")
	if got == nil {
		t.Fatalf("expected file to remain present")
	}
	if got.Checksum != 12345 {
		t.Fatalf("expected checksum to survive same-size remerge, got %d", got.Checksum)
	}
	if got.XferTime != 5000 {
		t.Fatalf("expected xfertime to survive same-size remerge, got %d", got.XferTime)
	}
}

func TestVFSSFVMetadataRequiresCurrentSFVFileWhenStrict(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVDataWithChecksum("/X265/release", "release.sfv", 123, map[string]uint32{
		"file.r00": 1,
	})

	if meta := vfs.GetSFVData("/X265/release"); meta != nil {
		t.Fatalf("expected strict sfv metadata without current sfv file to be ignored, got %+v", meta)
	}

	vfs.AddFile("/X265/release/release.sfv", VFSFile{Seen: true, Checksum: 123})
	if meta := vfs.GetSFVData("/X265/release"); meta == nil || len(meta.SFVEntries) != 1 {
		t.Fatalf("expected metadata to become valid when current sfv file exists, got %+v", meta)
	}
}

func TestVFSSFVMetadataRejectsMismatchedSFVChecksum(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/X265/release/release.sfv", VFSFile{Seen: true, Checksum: 999})
	vfs.SetSFVDataWithChecksum("/X265/release", "release.sfv", 123, map[string]uint32{
		"file.r00": 1,
	})

	if meta := vfs.GetSFVData("/X265/release"); meta != nil {
		t.Fatalf("expected mismatched sfv checksum metadata to be ignored, got %+v", meta)
	}
}

func TestVFSUpdateFileVerificationRefreshesRaceTruth(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{
		"file1.r00": 1,
	})
	vfs.AddFile("/X265/release/file1.r00", VFSFile{
		Size:     100,
		Seen:     true,
		Owner:    "n0pe",
		Group:    "Admin",
		XferTime: 0,
		Checksum: 0,
	})

	verifiedBefore := vfs.GetVerifiedSFVPresentFiles("/X265/release")
	if len(verifiedBefore) != 0 {
		t.Fatalf("expected file to be unverified before checksum refresh, got %+v", verifiedBefore)
	}

	usersBefore, _, totalBytesBefore, presentBefore, totalBefore := vfs.GetRaceStats("/X265/release")
	if presentBefore != 1 || totalBefore != 1 || totalBytesBefore != 100 {
		t.Fatalf("expected unknown-CRC file to count for visible completeness only, got present=%d total=%d bytes=%d", presentBefore, totalBefore, totalBytesBefore)
	}
	if len(usersBefore) != 0 {
		t.Fatalf("expected unknown-CRC file to stay out of verified race stats, got %+v", usersBefore)
	}

	if !vfs.UpdateFileVerification("/X265/release/file1.r00", 1) {
		t.Fatalf("expected verification update to succeed")
	}

	verified := vfs.GetVerifiedSFVPresentFiles("/X265/release")
	if len(verified) != 1 || !verified["file1.r00"] {
		t.Fatalf("expected verified set to include refreshed file, got %+v", verified)
	}

	_, _, _, presentAfter, totalAfter := vfs.GetRaceStats("/X265/release")
	if presentAfter != 1 || totalAfter != 1 {
		t.Fatalf("expected file to count after checksum refresh, got present=%d total=%d", presentAfter, totalAfter)
	}
}

func TestVFSRaceStatsIgnoreVerifiedFilesWithoutXferTime(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{
		"old.r00":  1,
		"live.r01": 2,
	})
	vfs.AddFile("/X265/release/old.r00", VFSFile{
		Size:     100,
		Seen:     true,
		Checksum: 1,
		XferTime: 0,
		Owner:    "olduser",
		Group:    "Admin",
	})
	vfs.AddFile("/X265/release/live.r01", VFSFile{
		Size:     200,
		Seen:     true,
		Checksum: 2,
		XferTime: 2000,
		Owner:    "liveuser",
		Group:    "Admin",
	})

	users, groups, totalBytes, present, total := vfs.GetRaceStats("/X265/release")
	if present != 2 || total != 2 {
		t.Fatalf("expected both verified files to count toward completeness, got present=%d total=%d", present, total)
	}
	if totalBytes != 300 {
		t.Fatalf("expected total bytes to still reflect verified payload, got %d", totalBytes)
	}
	if len(users) != 1 || users[0].Name != "liveuser" {
		t.Fatalf("expected only xfertime-backed user stats, got %+v", users)
	}
	if len(groups) != 1 || groups[0].Files != 1 || groups[0].Bytes != 200 {
		t.Fatalf("expected group stats to ignore xfertime-less files, got %+v", groups)
	}
}

func TestVFSVerifiedPresentFilesFilteredExcludeUploading(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{
		"file1.r00": 1,
		"file2.r01": 2,
	})
	vfs.AddFile("/X265/release/file1.r00", VFSFile{Seen: true, Checksum: 1})
	vfs.AddFile("/X265/release/file2.r01", VFSFile{Seen: true, Checksum: 2})

	verified := vfs.GetVerifiedSFVPresentFilesFiltered("/X265/release", map[string]bool{
		"file2.r01": true,
	})
	if len(verified) != 1 || !verified["file1.r00"] || verified["file2.r01"] {
		t.Fatalf("expected uploading file to be excluded from verified set, got %+v", verified)
	}
}

func TestVFSRaceStatsFilteredExcludeUploading(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/X265/release", VFSFile{IsDir: true, Seen: true})
	vfs.SetSFVData("/X265/release", "release.sfv", map[string]uint32{
		"done.r00": 1,
		"live.r01": 2,
	})
	vfs.AddFile("/X265/release/done.r00", VFSFile{
		Size:     100,
		Seen:     true,
		Checksum: 1,
		XferTime: 1000,
		Owner:    "doneuser",
		Group:    "Admin",
	})
	vfs.AddFile("/X265/release/live.r01", VFSFile{
		Size:     200,
		Seen:     true,
		Checksum: 2,
		XferTime: 1000,
		Owner:    "liveuser",
		Group:    "Admin",
	})

	users, groups, totalBytes, present, total := vfs.GetRaceStatsFiltered("/X265/release", map[string]bool{
		"live.r01": true,
	})
	if present != 1 || total != 2 {
		t.Fatalf("expected uploading file to stay out of present count, got present=%d total=%d", present, total)
	}
	if totalBytes != 100 {
		t.Fatalf("expected only completed verified bytes, got %d", totalBytes)
	}
	if len(users) != 1 || users[0].Name != "doneuser" {
		t.Fatalf("expected only completed uploader stats, got %+v", users)
	}
	if len(groups) != 1 || groups[0].Files != 1 || groups[0].Bytes != 100 {
		t.Fatalf("expected only completed group stats, got %+v", groups)
	}
}

func TestVFSRequestMetadataRoundTrip(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/REQUESTS", VFSFile{IsDir: true, Seen: true})
	vfs.SetRequestData("/REQUESTS",
		[]plugin.RequestRecord{{
			Release: "Some.Release-TEST",
			By:      "alice",
			Mode:    "gl",
			For:     "bob",
			Date:    "2026-05-18 18:00",
		}},
		[]plugin.RequestFillRecord{{
			Release:     "Other.Release-TEST",
			RequestedBy: "carol",
			FilledBy:    "dave",
			Date:        "2026-05-18 18:05",
		}},
	)

	requests, fills := vfs.GetRequestData("/REQUESTS")
	if len(requests) != 1 || requests[0].By != "alice" || requests[0].For != "bob" {
		t.Fatalf("unexpected request metadata %+v", requests)
	}
	if len(fills) != 1 || fills[0].FilledBy != "dave" {
		t.Fatalf("unexpected fill metadata %+v", fills)
	}

	requests[0].By = "mutated"
	fills[0].FilledBy = "mutated"

	requests, fills = vfs.GetRequestData("/REQUESTS")
	if requests[0].By != "alice" || fills[0].FilledBy != "dave" {
		t.Fatalf("expected metadata copies, got requests=%+v fills=%+v", requests, fills)
	}
}

func TestVFSZipExpectedPartsRequiresCurrentDIZFile(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.CacheZipExpectedParts("/0DAY/release", 12, 123)

	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("expected zip metadata without current diz file to be ignored, got expected=%d ok=%v", expected, ok)
	}

	vfs.AddFile("/0DAY/release/release.zip", VFSFile{Seen: true, Size: 100})
	vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Seen: true, Checksum: 123})
	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); !ok || expected != 12 {
		t.Fatalf("expected zip metadata to become valid with current diz file, got expected=%d ok=%v", expected, ok)
	}

	vfs.CacheZipExpectedParts("/0DAY/release", 0, 0)
	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("expected zip metadata clear to remove cached value, got expected=%d ok=%v", expected, ok)
	}
}

func TestVFSZipExpectedPartsRejectsMismatchedDIZChecksum(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/release.zip", VFSFile{Seen: true, Size: 100})
	vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Seen: true, Checksum: 999})
	vfs.CacheZipExpectedParts("/0DAY/release", 12, 123)

	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("expected mismatched diz checksum metadata to be ignored, got expected=%d ok=%v", expected, ok)
	}
}

func TestVFSZipExpectedPartsRequireCurrentZipArchive(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Seen: true, Checksum: 123})
	vfs.CacheZipExpectedParts("/0DAY/release", 12, 123)

	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("expected zip metadata without a current zip archive to be ignored, got expected=%d ok=%v", expected, ok)
	}
}

func TestVFSDeleteDIZClearsZipMetadata(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/release.zip", VFSFile{Seen: true, Size: 100})
	vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Seen: true, Checksum: 123})
	vfs.CacheZipExpectedParts("/0DAY/release", 12, 123)

	vfs.DeleteFile("/0DAY/release/file_id.diz")

	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("expected deleting file_id.diz to clear zip metadata, got expected=%d ok=%v", expected, ok)
	}
}

func TestVFSRenameDIZAwayClearsZipMetadata(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/release.zip", VFSFile{Seen: true, Size: 100})
	vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Seen: true, Checksum: 123})
	vfs.CacheZipExpectedParts("/0DAY/release", 12, 123)

	vfs.RenameFile("/0DAY/release/file_id.diz", "/0DAY/release/file_id.txt")

	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("expected renaming file_id.diz away to clear zip metadata, got expected=%d ok=%v", expected, ok)
	}
}

func TestVFSDeleteLastZipClearsZipMetadata(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/release.zip", VFSFile{Seen: true, Size: 100})
	vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Seen: true, Checksum: 123})
	vfs.CacheZipExpectedParts("/0DAY/release", 12, 123)

	vfs.DeleteFile("/0DAY/release/release.zip")

	if expected, ok := vfs.GetZipExpectedParts("/0DAY/release"); ok || expected != 0 {
		t.Fatalf("expected deleting the last zip archive to clear zip metadata, got expected=%d ok=%v", expected, ok)
	}
}

func TestVFSZipPayloadPreservesStrongerCompletedSizeOnWeakOverwrite(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/part1.zip", VFSFile{
		Seen:         true,
		Size:         4096,
		Owner:        "steel",
		Group:        "iND",
		XferTime:     1500,
		Checksum:     0x12345678,
		LastModified: 1,
	})

	vfs.AddFile("/0DAY/release/part1.zip", VFSFile{
		Seen:         true,
		Size:         1024,
		Owner:        "GoFTPd",
		Group:        "root",
		LastModified: 2,
	})

	got := vfs.GetFile("/0DAY/release/part1.zip")
	if got == nil {
		t.Fatalf("expected zip payload to remain present")
	}
	if got.Size != 4096 {
		t.Fatalf("expected stronger completed size 4096 to survive weak overwrite, got %d", got.Size)
	}
	if got.Checksum != 0x12345678 || got.XferTime != 1500 {
		t.Fatalf("expected transfer metadata to survive weak overwrite, checksum=%08X xfer=%d", got.Checksum, got.XferTime)
	}
}

func TestVFSHydrateRaceFileRestoresStrongerZipSize(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/part1.zip", VFSFile{
		Seen:         true,
		Size:         1024,
		Owner:        "GoFTPd",
		Group:        "root",
		LastModified: 1,
	})

	if !vfs.HydrateRaceFile("/0DAY/release/part1.zip", "steel", "iND", 4096, 1500, 0x12345678) {
		t.Fatalf("expected hydrate to update weak zip payload metadata")
	}

	got := vfs.GetFile("/0DAY/release/part1.zip")
	if got == nil {
		t.Fatalf("expected hydrated zip payload to remain present")
	}
	if got.Size != 4096 {
		t.Fatalf("expected hydrate to restore size 4096, got %d", got.Size)
	}
	if got.Owner != "steel" || got.Group != "iND" {
		t.Fatalf("expected hydrate to restore owner/group steel/iND, got %s/%s", got.Owner, got.Group)
	}
	if got.Checksum != 0x12345678 || got.XferTime != 1500 {
		t.Fatalf("expected hydrate to restore transfer metadata, checksum=%08X xfer=%d", got.Checksum, got.XferTime)
	}
}

func TestVFSGetReleaseStatusForSFVTracksMissingFiles(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/MP3/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/MP3/release/release.sfv", VFSFile{Seen: true, Size: 10, Checksum: 123})
	vfs.AddFile("/MP3/release/release.nfo", VFSFile{Seen: true, Size: 10})
	vfs.SetSFVDataWithChecksum("/MP3/release", "release.sfv", 123, map[string]uint32{
		"01-track.mp3": 1,
		"02-track.mp3": 2,
	})
	vfs.AddFile("/MP3/release/01-track.mp3", VFSFile{Seen: true, Size: 100, Checksum: 1})

	status, ok := vfs.GetReleaseStatus("/MP3/release")
	if !ok {
		t.Fatalf("expected release status to be available")
	}
	if status.Kind != "sfv" {
		t.Fatalf("expected sfv kind, got %q", status.Kind)
	}
	if status.Present != 1 || status.Total != 2 {
		t.Fatalf("expected sfv present/total 1/2, got %d/%d", status.Present, status.Total)
	}
	if len(status.MissingFiles) != 1 || status.MissingFiles[0] != "02-track.mp3" {
		t.Fatalf("expected one missing file 02-track.mp3, got %#v", status.MissingFiles)
	}
}

func TestVFSGetReleaseStatusForSFVUsesNormalizedPresentFileKeys(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/MP3/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/MP3/release/release.sfv", VFSFile{Seen: true, Size: 10, Checksum: 123})
	vfs.SetSFVDataWithChecksum("/MP3/release", "release.sfv", 123, map[string]uint32{
		"01-Track.MP3": 1,
		"02-track.mp3": 2,
	})
	vfs.AddFile("/MP3/release/01-track.mp3", VFSFile{Seen: true, Size: 100, Checksum: 1})

	status, ok := vfs.GetReleaseStatus("/MP3/release")
	if !ok {
		t.Fatalf("expected release status to be available")
	}
	if status.Present != 1 || status.Total != 2 {
		t.Fatalf("expected sfv present/total 1/2, got %d/%d", status.Present, status.Total)
	}
	if len(status.MissingFiles) != 1 || status.MissingFiles[0] != "02-track.mp3" {
		t.Fatalf("expected one missing file 02-track.mp3, got %#v", status.MissingFiles)
	}
}

func TestVFSGetReleaseStatusForSFVTreatsChecksumMismatchAsMissing(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/MP3/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/MP3/release/release.sfv", VFSFile{Seen: true, Size: 10, Checksum: 123})
	vfs.SetSFVDataWithChecksum("/MP3/release", "release.sfv", 123, map[string]uint32{
		"01-track.mp3": 1,
		"02-track.mp3": 2,
	})
	vfs.AddFile("/MP3/release/01-track.mp3", VFSFile{Seen: true, Size: 100, Checksum: 1})
	vfs.AddFile("/MP3/release/02-track.mp3", VFSFile{Seen: true, Size: 100, Checksum: 999})

	status, ok := vfs.GetReleaseStatus("/MP3/release")
	if !ok {
		t.Fatalf("expected release status to be available")
	}
	if status.Present != 1 || status.Total != 2 {
		t.Fatalf("expected sfv present/total 1/2, got %d/%d", status.Present, status.Total)
	}
	if len(status.MissingFiles) != 1 || status.MissingFiles[0] != "02-track.mp3" {
		t.Fatalf("expected checksum mismatch to be treated as missing, got %#v", status.MissingFiles)
	}
}

func TestVFSGetReleaseStatusForZipUsesCachedExpectedParts(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/0DAY/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/0DAY/release/file_id.diz", VFSFile{Seen: true, Checksum: 321})
	vfs.AddFile("/0DAY/release/a.zip", VFSFile{Seen: true, Size: 100})
	vfs.AddFile("/0DAY/release/b.z01", VFSFile{Seen: true, Size: 100})
	vfs.CacheZipExpectedParts("/0DAY/release", 3, 321)

	status, ok := vfs.GetReleaseStatus("/0DAY/release")
	if !ok {
		t.Fatalf("expected release status to be available")
	}
	if status.Kind != "zip" {
		t.Fatalf("expected zip kind, got %q", status.Kind)
	}
	if status.Present != 2 || status.Total != 3 {
		t.Fatalf("expected zip present/total 2/3, got %d/%d", status.Present, status.Total)
	}
}

func TestVFSListDirectoryIgnoresMislinkedDeepChildren(t *testing.T) {
	vfs := NewVirtualFileSystem()
	vfs.AddFile("/MP3", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/MP3/0519", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/MP3/0519/release", VFSFile{IsDir: true, Seen: true})
	vfs.AddFile("/MP3/0519/release/track.mp3", VFSFile{Seen: true, Size: 1234})

	vfs.mu.Lock()
	vfs.ensureChildrenBucketLocked("/")
	vfs.children["/"]["/MP3/0519/release/track.mp3"] = struct{}{}
	vfs.mu.Unlock()

	rootEntries := vfs.ListDirectory("/")
	for _, entry := range rootEntries {
		if entry == nil {
			continue
		}
		if entry.Path == "/MP3/0519/release/track.mp3" {
			t.Fatalf("expected root listing to ignore mislinked deep child")
		}
	}
}
