package announce

import (
	"strings"
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

func TestReleaseNameUsesDirectoryBaseForNukeEvents(t *testing.T) {
	evt := &event.Event{
		Type:     event.EventNuke,
		Section:  "TV-PL",
		Filename: "Santa.Clarita.Diet.S02E03.POLISH.HDR.2160p.WEB.H265-FLAME",
		Path:     "/FOREIGN/TV-PL/Santa.Clarita.Diet.S02E03.POLISH.HDR.2160p.WEB.H265-FLAME",
	}

	got := releaseName(evt)
	want := "Santa.Clarita.Diet.S02E03.POLISH.HDR.2160p.WEB.H265-FLAME"
	if got != want {
		t.Fatalf("releaseName(nuke) = %q, want %q", got, want)
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
			"t_mbytes":     "3678MB",
			"t_files":      "20F",
			"u_count":      "3",
			"leader_name":  "musch3l",
			"leader_mb":    "953.7",
			"leader_files": "5",
			"leader_pct":   "50",
			"leader_speed": "456.41MB/s",
			"t_filesleft":  "5",
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

func TestUploadRaceAnnouncesStopAfterComplete(t *testing.T) {
	p := New()

	releasePath := "/MP3/03.05.26/Artist-Album-2026-GRP"
	completeEvt := &event.Event{
		Type:    event.EventRaceEnd,
		Section: "MP3",
		Path:    releasePath,
		Data: map[string]string{
			"u_count":    "3",
			"t_mbytes":   "50MB",
			"t_files":    "10F",
			"t_avgspeed": "12.50MB/s",
			"t_duration": "4.0s",
		},
	}
	outs, err := p.OnEvent(completeEvt)
	if err != nil {
		t.Fatalf("OnEvent complete failed: %v", err)
	}
	if len(outs) == 0 {
		t.Fatalf("expected complete output")
	}

	uploadEvt := &event.Event{
		Type:     event.EventUpload,
		Section:  "MP3",
		User:     "lateuser",
		Group:    "GRP",
		Filename: "01-track.mp3",
		Path:     releasePath + "/01-track.mp3",
		Speed:    12.34,
		Data: map[string]string{
			"leader_name":  "lateuser",
			"leader_mb":    "5.0",
			"leader_files": "1",
			"leader_pct":   "10",
			"leader_speed": "12.34MB/s",
			"t_present":    "10",
			"t_files":      "10",
			"t_filesleft":  "0",
		},
	}
	outs, err = p.OnEvent(uploadEvt)
	if err != nil {
		t.Fatalf("OnEvent upload failed: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("expected no post-complete race outputs, got %+v", outs)
	}
}

func TestPretimeAnnounceIsSuppressedAfterComplete(t *testing.T) {
	p := New()

	releasePath := "/EBOOKS/Anthony.Shadid.House.of.Stone.A.Memoir.of.Home.Family.and.a.Lost.Middle.East.2012.RETAiL.EPUB.eBook-NODE"
	completeEvt := &event.Event{
		Type:    event.EventRaceEnd,
		Section: "EBOOKS",
		Path:    releasePath,
		Data: map[string]string{
			"u_count":    "1",
			"t_mbytes":   "2MB",
			"t_files":    "1F",
			"t_avgspeed": "1.57MB/s",
			"t_duration": "1.0210s",
		},
	}
	if _, err := p.OnEvent(completeEvt); err != nil {
		t.Fatalf("OnEvent complete failed: %v", err)
	}

	pretimeEvt := &event.Event{
		Type:    event.EventNewPreTime,
		Section: "EBOOKS",
		Path:    releasePath,
		Data: map[string]string{
			"preage": "0s",
		},
	}
	outs, err := p.OnEvent(pretimeEvt)
	if err != nil {
		t.Fatalf("OnEvent pretime failed: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("expected late pretime to be suppressed, got %+v", outs)
	}
}

func TestPretimeAnnounceIsSuppressedAfterCompleteAcrossPathVariants(t *testing.T) {
	p := New()

	completeEvt := &event.Event{
		Type:    event.EventRaceEnd,
		Section: "EBOOKS",
		Path:    "/EBOOKS/20260518/Cindy.Trimm.Commanding.Your.Morning.Unleash.the.Power.of.God.in.Your.Life.2010.RETAiL.EPUB.eBook-NODE",
		Data: map[string]string{
			"u_count":    "1",
			"t_mbytes":   "2MB",
			"t_files":    "1F",
			"t_avgspeed": "1.57MB/s",
			"t_duration": "1.0210s",
		},
	}
	if _, err := p.OnEvent(completeEvt); err != nil {
		t.Fatalf("OnEvent complete failed: %v", err)
	}

	pretimeEvt := &event.Event{
		Type:    event.EventNewPreTime,
		Section: "EBOOKS",
		Path:    "/!Today_EBOOKS/Cindy.Trimm.Commanding.Your.Morning.Unleash.the.Power.of.God.in.Your.Life.2010.RETAiL.EPUB.eBook-NODE",
		Data: map[string]string{
			"preage": "0s",
		},
	}
	outs, err := p.OnEvent(pretimeEvt)
	if err != nil {
		t.Fatalf("OnEvent pretime failed: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("expected late pretime across path variants to be suppressed, got %+v", outs)
	}
}

func TestMediaInfoAnnounceIsSuppressedAfterComplete(t *testing.T) {
	p := New()

	releasePath := "/TV-1080P/Gilmore.Girls.S05E04.1080p.BluRay.x264-BORDURE"
	completeEvt := &event.Event{
		Type:    event.EventRaceEnd,
		Section: "TV-1080P",
		Path:    releasePath,
		Data: map[string]string{
			"u_count":    "4",
			"t_mbytes":   "3853MB",
			"t_files":    "81F",
			"t_avgspeed": "490.08MB/s",
			"t_duration": "30.3040s",
		},
	}
	if _, err := p.OnEvent(completeEvt); err != nil {
		t.Fatalf("OnEvent complete failed: %v", err)
	}

	mediaEvt := &event.Event{
		Type:    event.EventMediaInfo,
		Section: "TV-1080P",
		Path:    releasePath,
		Data: map[string]string{
			"video_format": "AVC",
			"audio_format": "FLAC",
			"duration":     "1m04s",
		},
	}
	outs, err := p.OnEvent(mediaEvt)
	if err != nil {
		t.Fatalf("OnEvent media info failed: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("expected late media info to be suppressed, got %+v", outs)
	}
}

func TestMediaInfoAnnounceIsSuppressedAfterCompleteAcrossPathVariants(t *testing.T) {
	p := New()

	completeEvt := &event.Event{
		Type:    event.EventRaceEnd,
		Section: "TV-1080P",
		Path:    "/TV-1080P/20260520/Gilmore.Girls.S05E04.1080p.BluRay.x264-BORDURE",
		Data: map[string]string{
			"u_count":    "4",
			"t_mbytes":   "3853MB",
			"t_files":    "81F",
			"t_avgspeed": "490.08MB/s",
			"t_duration": "30.3040s",
		},
	}
	if _, err := p.OnEvent(completeEvt); err != nil {
		t.Fatalf("OnEvent complete failed: %v", err)
	}

	mediaEvt := &event.Event{
		Type:    event.EventMediaInfo,
		Section: "TV-1080P",
		Path:    "/!Today_TV-1080P/Gilmore.Girls.S05E04.1080p.BluRay.x264-BORDURE",
		Data: map[string]string{
			"video_format": "AVC",
			"audio_format": "FLAC",
			"duration":     "1m04s",
		},
	}
	outs, err := p.OnEvent(mediaEvt)
	if err != nil {
		t.Fatalf("OnEvent media info failed: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("expected late media info across path variants to be suppressed, got %+v", outs)
	}
}

func TestRequestReleaseDirEmitsNewAnnounce(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type:     event.EventMKDir,
		Section:  "REQUESTS",
		User:     "alice",
		Filename: "Space.Haven.Linux-rG",
		Path:     "/REQUESTS/REQ-Space.Haven.Linux-rG/Space.Haven.Linux-rG",
		Data:     map[string]string{},
	}

	outs, err := p.OnEvent(evt)
	if err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	if len(outs) != 1 || outs[0].Type != "NEW" {
		t.Fatalf("expected request release mkdir to emit NEW, got %#v", outs)
	}
}

func TestDirectRequestUploadEmitsSingleSyntheticNewAnnounce(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type:     event.EventUpload,
		Section:  "REQUESTS",
		User:     "alice",
		Filename: "space.haven.linux-rg.r00",
		Path:     "/REQUESTS/REQ-Space.Haven.Linux-rG/space.haven.linux-rg.r00",
		Size:     100,
		Data: map[string]string{
			"t_files": "10F",
		},
	}

	outs, err := p.OnEvent(evt)
	if err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	newCount := 0
	for _, out := range outs {
		if out.Type == "NEW" {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("expected first direct request upload to emit one NEW, got %#v", outs)
	}

	outs, err = p.OnEvent(evt)
	if err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	for _, out := range outs {
		if out.Type == "NEW" {
			t.Fatalf("did not expect duplicate NEW for same direct request upload, got %#v", outs)
		}
	}
}

func TestMediaInfoIgnoresSectionRootEvent(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type:     event.EventMediaInfo,
		Section:  "TV-1080P",
		Filename: "TV-1080P",
		Path:     "/TV-1080P",
		Data: map[string]string{
			"video_format": "AVC",
			"audio_format": "AAC",
			"duration":     "1m00s",
		},
	}

	outs, err := p.OnEvent(evt)
	if err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("expected section-root media info event to be ignored, got %#v", outs)
	}
}

func TestLoginFailDiskFullDoesNotSuggestBan(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type: event.EventLoginFail,
		Data: map[string]string{
			"username":    "Finity",
			"remote_ip":   "95.211.6.225",
			"remote_mask": "*@95.211.6.225",
			"reason":      "disk_full",
		},
	}

	outs, err := p.OnEvent(evt)
	if err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(outs))
	}
	if !strings.Contains(outs[0].Text, "disk full") {
		t.Fatalf("output %q does not mention disk full", outs[0].Text)
	}
	if strings.Contains(outs[0].Text, "SITE BAN") {
		t.Fatalf("output %q should not suggest a ban", outs[0].Text)
	}
}

func TestLoginFailNoIPMasksDoesNotSuggestBan(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type: event.EventLoginFail,
		Data: map[string]string{
			"username":    "Ostral",
			"remote_ip":   "185.7.81.122",
			"remote_mask": "*@185.7.81.122",
			"reason":      "no_ip_masks",
		},
	}

	outs, err := p.OnEvent(evt)
	if err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(outs))
	}
	if !strings.Contains(outs[0].Text, "no IP masks configured") {
		t.Fatalf("output %q does not mention missing IP masks", outs[0].Text)
	}
	if strings.Contains(outs[0].Text, "SITE BAN") {
		t.Fatalf("output %q should not suggest a ban", outs[0].Text)
	}
}

func TestLoginFailStorageErrorShowsDetail(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type: event.EventLoginFail,
		Data: map[string]string{
			"username": "XtrEme",
			"reason":   "storage_error",
			"action":   "last_login",
			"error":    "refusing unsafe userfile save for XtrEme: would reset RATIO from 3 to 0",
		},
	}

	outs, err := p.OnEvent(evt)
	if err != nil {
		t.Fatalf("OnEvent failed: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("outputs = %d, want 1", len(outs))
	}
	for _, needle := range []string{"storage write failed", "last_login", "would reset RATIO"} {
		if !strings.Contains(outs[0].Text, needle) {
			t.Fatalf("output %q missing %q", outs[0].Text, needle)
		}
	}
}

func TestFormatAudioInfoSummaryOmitsMissingSampleRate(t *testing.T) {
	got := formatAudioInfoSummary(map[string]string{
		"genre":        "Acoustic",
		"year":         "2020",
		"sample_rate":  "",
		"channels":     "Stereo",
		"bitrate":      "2991kbps",
		"bitrate_mode": "VBR",
	})

	want := "Get ready for some Acoustic from 2020 in Stereo 2991kbps (VBR)"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestFormatSampleVideoLabelFallsBackCleanlyWithoutDimensions(t *testing.T) {
	got := formatSampleVideoLabel(map[string]string{
		"video_format": "AVC",
		"width":        "",
		"height":       "",
	})
	if got != "AVC" {
		t.Fatalf("video label = %q, want %q", got, "AVC")
	}
}
