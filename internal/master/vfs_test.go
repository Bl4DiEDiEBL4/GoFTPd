package master

import (
	"strings"
	"testing"
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
	if users[0].Percent != 66 {
		t.Fatalf("expected 66 percent, got %d", users[0].Percent)
	}
	if len(groups) != 1 || groups[0].Name != "Admin" {
		t.Fatalf("expected Admin group stats, got %+v", groups)
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
