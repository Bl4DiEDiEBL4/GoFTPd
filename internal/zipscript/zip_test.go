package zipscript

import "testing"

func TestZipExpectedPartsFromDIZContentMatchesDrFTPDStyleMarkers(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{name: "square brackets", content: "[01/15]", want: 15},
		{name: "angle brackets with o x digits", content: "<ox/o9>", want: 9},
		{name: "spaces around separator", content: "( 1 / 12 )", want: 12},
		{name: "missing marker", content: "no file count here", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseZipExpectedPartsFromDIZ([]byte(tt.content)); got != tt.want {
				t.Fatalf("ParseZipExpectedPartsFromDIZ(%q) = %d, want %d", tt.content, got, tt.want)
			}
		})
	}
}

func TestZipDirCompleteFollowsDIZCountStyle(t *testing.T) {
	tests := []struct {
		name     string
		entries  []ZipEntryInfo
		expected int
		want     bool
	}{
		{
			name: "exact expected count",
			entries: []ZipEntryInfo{
				{Name: "a.zip", Size: 5000},
				{Name: "b.zip", Size: 3200},
				{Name: "c.zip", Size: 2800},
			},
			expected: 3,
			want:     true,
		},
		{
			name: "more zip files than expected still complete",
			entries: []ZipEntryInfo{
				{Name: "a.zip", Size: 5000},
				{Name: "b.zip", Size: 3200},
				{Name: "c.zip", Size: 2800},
				{Name: "d.zip", Size: 1200},
			},
			expected: 3,
			want:     true,
		},
		{
			name: "missing a zip file",
			entries: []ZipEntryInfo{
				{Name: "a.zip", Size: 5000},
				{Name: "b.zip", Size: 5000},
			},
			expected: 3,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ZipDirComplete(tt.entries, tt.expected); got != tt.want {
				t.Fatalf("ZipDirComplete() = %v, want %v", got, tt.want)
			}
		})
	}
}
