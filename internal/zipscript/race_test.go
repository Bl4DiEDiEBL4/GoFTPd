package zipscript

import "testing"

func TestShouldStartRaceWindowForReleaseDir(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X264-HD-720P/*", "/0DAY/*/*", "/REQUESTS/*/*"},
		},
	}

	if !ShouldStartRaceWindow(cfg, "/X264-HD-720P/Release-GRP") {
		t.Fatalf("expected normal release dir to start race window")
	}
	if !ShouldStartRaceWindow(cfg, "/0DAY/0517/Release-GRP") {
		t.Fatalf("expected dated release dir to start race window")
	}
	if !ShouldStartRaceWindow(cfg, "/REQUESTS/REQ-test/Release-GRP") {
		t.Fatalf("expected request release dir to start race window")
	}
	if !ShouldStartRaceWindow(cfg, "/X264-HD-720P/Release-GRP/") {
		t.Fatalf("expected trailing slash release dir to start race window")
	}
}

func TestShouldStartRaceWindowSkipsParentsAndSceneSubdirs(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X264-HD-720P/*", "/0DAY/*/*"},
		},
	}

	if ShouldStartRaceWindow(cfg, "/X264-HD-720P") {
		t.Fatalf("did not expect section root to start race window")
	}
	if ShouldStartRaceWindow(cfg, "/0DAY/0517") {
		t.Fatalf("did not expect dated parent dir to start race window")
	}
	if ShouldStartRaceWindow(cfg, "/X264-HD-720P/Release-GRP/Sample") {
		t.Fatalf("did not expect scene subdir to start race window")
	}
}
