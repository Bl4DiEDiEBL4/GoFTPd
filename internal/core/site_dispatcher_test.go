package core

import "testing"

func TestSensitiveSiteCommandsRequireFallbackFlag(t *testing.T) {
	for _, command := range []string{
		"WHO",
		"SWHO",
		"SEEN",
		"LASTLOGIN",
		"GROUPS",
		"GROUP",
		"GINFO",
		"GRP",
		"TRAFFIC",
	} {
		if got := requiredSiteCommandFlags(command); got != "1" {
			t.Fatalf("requiredSiteCommandFlags(%q) = %q, want %q", command, got, "1")
		}
	}
}

func TestBandwidthSiteCommandRemainsPublicInFallback(t *testing.T) {
	if got := requiredSiteCommandFlags("BW"); got != "" {
		t.Fatalf("requiredSiteCommandFlags(%q) = %q, want public command", "BW", got)
	}
}
