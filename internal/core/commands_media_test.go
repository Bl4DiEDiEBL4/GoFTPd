package core

import (
	"testing"

	"weaveftpd/internal/zipscript"
)

func TestMediaInfoReleaseDirAllowedRejectsSectionRoot(t *testing.T) {
	cfg := &Config{
		Zipscript: zipscript.Config{
			Enabled: true,
			Sections: zipscript.SectionsConfig{
				SFV: []string{"/TV-1080P/*", "/0DAY/*/*"},
			},
		},
	}

	if mediaInfoReleaseDirAllowed(cfg, "/TV-1080P") {
		t.Fatalf("section root should not be accepted as media-info release dir")
	}
	if mediaInfoReleaseDirAllowed(cfg, "/0DAY/0516") {
		t.Fatalf("dated container should not be accepted as media-info release dir")
	}
	if !mediaInfoReleaseDirAllowed(cfg, "/TV-1080P/Real.Release-GRP") {
		t.Fatalf("direct release dir should be accepted as media-info release dir")
	}
	if !mediaInfoReleaseDirAllowed(cfg, "/0DAY/0516/Real.Release-GRP") {
		t.Fatalf("dated release dir should be accepted as media-info release dir")
	}
}
