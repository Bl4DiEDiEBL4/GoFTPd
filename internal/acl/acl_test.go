package acl

import "testing"

func TestMatchesRulePathCoversPrivatePathDescendants(t *testing.T) {
	e := &Engine{RulesByType: map[string][]Rule{
		"privpath": {
			{Type: "privpath", Path: "/site/PRE/GROUP", Required: "=GROUP =SiteOP"},
			{Type: "privpath", Path: "/site/LINKS/*", Required: "1"},
		},
	}}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "exact", path: "/site/PRE/GROUP", want: true},
		{name: "child release", path: "/site/PRE/GROUP/Release-GRP", want: true},
		{name: "nested file", path: "/site/PRE/GROUP/Release-GRP/file.rar", want: true},
		{name: "sibling group", path: "/site/PRE/OTHER/Release-GRP", want: false},
		{name: "wildcard", path: "/site/LINKS/something", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.MatchesRulePath("privpath", tt.path)
			if got != tt.want {
				t.Fatalf("MatchesRulePath(privpath, %q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
