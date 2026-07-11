package master

import (
	"strings"
	"testing"
)

// cleanVFSPath must always return a rooted, forward-slash, canonical path with
// no ".." segments, on every platform, and must be idempotent. Relative ".."
// input has to collapse against "/" instead of surviving as an alias key like
// "/../bar" that no lookup would ever match.
func TestCleanVFSPathCanonical(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "/"},
		{".", "/"},
		{"/", "/"},
		{"..", "/"},
		{"/..", "/"},
		{"../..", "/"},
		{"foo", "/foo"},
		{"/foo/bar", "/foo/bar"},
		{"foo/../../bar", "/bar"},
		{"/foo/../../bar", "/bar"},
		{"a/./b", "/a/b"},
		{"a//b/", "/a/b"},
		{"  /x  ", "/x"},
		{"\\TV\\Some.Release\\file.rar", "/TV/Some.Release/file.rar"},
		{"TV\\..\\..\\etc", "/etc"},
		{"/TV/Some.Release/../Other", "/TV/Other"},
	}
	for _, c := range cases {
		got := cleanVFSPath(c.in)
		if got != c.want {
			t.Errorf("cleanVFSPath(%q) = %q, want %q", c.in, got, c.want)
		}
		if !strings.HasPrefix(got, "/") {
			t.Errorf("cleanVFSPath(%q) = %q: not rooted", c.in, got)
		}
		if got != "/" && strings.Contains(got+"/", "/../") {
			t.Errorf("cleanVFSPath(%q) = %q: contains ..", c.in, got)
		}
		if again := cleanVFSPath(got); again != got {
			t.Errorf("cleanVFSPath not idempotent: %q -> %q -> %q", c.in, got, again)
		}
	}
}
