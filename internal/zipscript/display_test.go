package zipscript

import (
	"strings"
	"testing"
)

func TestBuildSectionRulesBoxWrapsContentToFixedWidth(t *testing.T) {
	lines := BuildSectionRulesBox("TV-720P", []string{
		"This is a deliberately long section rule that must wrap without resizing the drawbox or losing words.",
	}, "1.3.0")
	if len(lines) < 6 {
		t.Fatalf("expected wrapped rules box, got %q", lines)
	}
	for _, line := range lines {
		if got := len([]byte(line)); got != BoxInnerWidth+2 {
			t.Fatalf("line width = %d, want %d: %q", got, BoxInnerWidth+2, line)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "TV-720P RULES") {
		t.Fatalf("missing section title: %q", lines)
	}
}
