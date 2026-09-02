package core

import (
	"strings"
	"testing"
	"time"
)

func TestFormatUserBandwidthLineUsesMatchedSlaveStat(t *testing.T) {
	snap := sessionSnapshot{
		User:              "username",
		TransferDirection: "upload",
		TransferPath:      "/TV-1080P/Dutton.Ranch.S01E02.1080p.WEB.h264-ABCD/dutton.ranch.102.1080p.web.h264-abcd.r03",
		TransferStartedAt: time.Now().Add(-10 * time.Second),
		TransferSlaveName: "LOCAL",
		TransferSlaveIdx:  77,
	}
	stats := []LiveTransferStat{{
		SlaveName:     "LOCAL",
		TransferIndex: 77,
		Direction:     "upload",
		Path:          snap.TransferPath,
		SpeedBytes:    25 * 1024 * 1024,
	}}

	line := formatUserBandwidthLine(snap, stats)
	if !strings.Contains(line, "209.72Mb/s via LOCAL") {
		t.Fatalf("expected matched slave speed in line, got %q", line)
	}
	if strings.Contains(line, "0.00Gb/s") {
		t.Fatalf("expected non-zero slave speed, got %q", line)
	}
}

func TestFormatBandwidthSpeedUsesNetworkUnits(t *testing.T) {
	tests := []struct {
		name           string
		bytesPerSecond float64
		want           string
	}{
		{name: "zero", bytesPerSecond: 0, want: "0.00Gb/s"},
		{name: "megabit", bytesPerSecond: 25 * 1024 * 1024, want: "209.72Mb/s"},
		{name: "gigabit", bytesPerSecond: 125_000_000, want: "1.00Gb/s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBandwidthSpeed(tt.bytesPerSecond); got != tt.want {
				t.Fatalf("formatBandwidthSpeed(%v) = %q, want %q", tt.bytesPerSecond, got, tt.want)
			}
		})
	}
}

func TestSlaveStatMatchedPreventsPassthroughDuplicate(t *testing.T) {
	snaps := []sessionSnapshot{{
		User:              "username",
		TransferDirection: "upload",
		TransferPath:      "/0DAY/release/file.zip",
		TransferSlaveName: "LOCAL",
		TransferSlaveIdx:  42,
	}}
	stat := LiveTransferStat{
		SlaveName:     "LOCAL",
		TransferIndex: 42,
		Direction:     "upload",
		Path:          "/0DAY/release/file.zip",
	}

	if !slaveStatMatched(stat, snaps) {
		t.Fatal("expected slave stat to be matched to the control session")
	}
}

func TestFindUserBandwidthSnapshotPrefersTransfer(t *testing.T) {
	snaps := []sessionSnapshot{
		{
			User:          "username",
			CurrentDir:    "/",
			LastCommandAt: time.Now(),
		},
		{
			User:              "username",
			CurrentDir:        "/TV-1080P",
			TransferDirection: "upload",
			TransferPath:      "/TV-1080P/release/file.r03",
			TransferStartedAt: time.Now().Add(-5 * time.Second),
		},
	}

	snap, ok := findUserBandwidthSnapshot(snaps, "username")
	if !ok {
		t.Fatal("expected user snapshot")
	}
	if snap.TransferDirection != "upload" {
		t.Fatalf("expected active transfer snapshot, got direction %q", snap.TransferDirection)
	}
}
