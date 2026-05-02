package announce

import (
	"testing"

	"goftpd/sitebot/internal/event"
)

func TestVarsNormalizesBrokenUploadReleaseName(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type:     event.EventUpload,
		Section:  "BLURAY",
		Filename: "the.boxer.2009.multi.complete.bluray-prawn.r00",
		Path:     "/BLURAY/The.Boxer.2009.MULTi.COMPLETE.BLURAY-PRAWN/the.boxer.2009.multi.complete.bluray-prawn.r00",
		Data: map[string]string{
			"relname": "/",
		},
	}

	vars := p.vars(evt)

	if got, want := vars["relname"], "The.Boxer.2009.MULTi.COMPLETE.BLURAY-PRAWN"; got != want {
		t.Fatalf("relname = %q, want %q", got, want)
	}
	if got, want := vars["reldir"], "The.Boxer.2009.MULTi.COMPLETE.BLURAY-PRAWN"; got != want {
		t.Fatalf("reldir = %q, want %q", got, want)
	}
}

func TestNormalizeReleaseDisplayNameUsesUploadParentDir(t *testing.T) {
	evt := &event.Event{
		Type:     event.EventUpload,
		Filename: "street.trash.1987.remastered.1080p.bluray.x264-watchable.r00",
		Path:     "/X264-HD-1080P/Street.Trash.1987.REMASTERED.1080P.BLURAY.X264-WATCHABLE/street.trash.1987.remastered.1080p.bluray.x264-watchable.r00",
	}

	got := normalizeReleaseDisplayName(evt, "/")
	want := "Street.Trash.1987.REMASTERED.1080P.BLURAY.X264-WATCHABLE"
	if got != want {
		t.Fatalf("normalized release = %q, want %q", got, want)
	}
}

func TestVarsProvidesDrFTPDStyleAliases(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type:     event.EventUpload,
		Section:  "TV-DE",
		User:     "N0pe",
		Group:    "Admin",
		Filename: "example.r00",
		Path:     "/FOREIGN/TV-DE/Example.Release-GRP/example.r00",
		Size:     150 * 1024 * 1024,
		Data: map[string]string{
			"t_mbytes":    "3678MB",
			"t_files":     "20F",
			"u_count":     "3",
			"leader_name": "musch3l",
			"leader_mb":   "953.7",
			"leader_files": "5",
			"leader_pct":  "50",
			"leader_speed": "456.41MB/s",
			"t_filesleft": "5",
		},
	}

	vars := p.vars(evt)

	checks := map[string]string{
		"user":         "N0pe",
		"group":        "Admin",
		"file":         "example.r00",
		"size":         "3678MB",
		"files":        "20F",
		"racers":       "3",
		"leaduser":     "musch3l",
		"leadsize":     "953.7MB",
		"leadfiles":    "5",
		"leadpercent":  "50%",
		"leadspeed":    "456.41MB/s",
		"filesleft":    "5",
		"sectioncolor": vars["sec_c2"],
	}
	for key, want := range checks {
		if got := vars[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}
