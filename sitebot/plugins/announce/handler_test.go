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
