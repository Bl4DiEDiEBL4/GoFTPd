package core

import "testing"

func TestSensitiveSiteCommandsRequireFallbackFlag(t *testing.T) {
	for _, command := range []string{
		"WHO",
		"SWHO",
		"BW",
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
