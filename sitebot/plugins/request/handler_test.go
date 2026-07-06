package request

import (
	"strings"
	"testing"

	"weaveftpd/sitebot/internal/event"
)

func TestReqTopResponseFormatsLikeTop(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type: event.EventCommand,
		User: "tester",
		Data: map[string]string{"channel": "#chan"},
	}

	out := p.reqTopResponse(evt, []string{
		"200- Request Fill Leaderboard:",
		"200- [01] alice - 3 fill(s)",
		"200  [02] bob - 1 fill(s)",
	})

	if len(out) != 4 {
		t.Fatalf("expected 4 output lines, got %d: %#v", len(out), out)
	}
	if !strings.Contains(out[0].Text, "REQTOP FILLERS") {
		t.Fatalf("expected header, got %q", out[0].Text)
	}
	if !strings.Contains(out[1].Text, "[01] alice - (3 Fills)") {
		t.Fatalf("expected first entry, got %q", out[1].Text)
	}
	if !strings.Contains(out[3].Text, "TOTAL REQUEST FILLS: ( 4 Fills )") {
		t.Fatalf("expected total, got %q", out[3].Text)
	}
}

func TestReqTopResponseEmpty(t *testing.T) {
	p := New()
	evt := &event.Event{
		Type: event.EventCommand,
		User: "tester",
		Data: map[string]string{"channel": "#chan"},
	}

	out := p.reqTopResponse(evt, []string{"200 No filled request stats yet."})
	if len(out) != 1 || !strings.Contains(out[0].Text, "No filled requests recorded yet") {
		t.Fatalf("expected empty output, got %#v", out)
	}
}
