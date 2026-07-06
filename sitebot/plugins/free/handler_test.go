package free

import (
	"strings"
	"testing"
	"time"

	"weaveftpd/sitebot/internal/event"
)

func TestAggregateNamedGroupSumsMatchingMountPaths(t *testing.T) {
	statuses := []diskStatus{
		{
			Name: "LOCAL",
			Roots: []diskRootStatus{
				{Path: "/glftpd/site", MountPath: "/", Free: 10, Total: 100},
				{Path: "/glftpd/DISK1", MountPath: "/ARCHiVE", Free: 50, Total: 200},
				{Path: "/glftpd/DISK2", MountPath: "/ARCHiVE", Free: 70, Total: 300},
				{Path: "/glftpd/DISK8", MountPath: "/OFFSITE", Free: 90, Total: 400},
			},
		},
	}

	freeBytes, totalBytes, ok := aggregateNamedGroup(statuses, namedGroup{
		Name:       "ARCHiVE",
		MountPaths: []string{"/ARCHiVE"},
	})
	if !ok {
		t.Fatalf("expected group to match")
	}
	if freeBytes != 120 || totalBytes != 500 {
		t.Fatalf("aggregateNamedGroup() = %d/%d, want 120/500", freeBytes, totalBytes)
	}
}

func TestAggregateNamedGroupSumsMatchingPaths(t *testing.T) {
	statuses := []diskStatus{
		{
			Name: "LOCAL",
			Roots: []diskRootStatus{
				{Path: "/glftpd/DISK1", MountPath: "/ARCHiVE", Free: 50, Total: 200},
				{Path: "/glftpd/DISK2", MountPath: "/ARCHiVE", Free: 70, Total: 300},
				{Path: "/glftpd/DISK8", MountPath: "/OFFSITE", Free: 90, Total: 400},
			},
		},
	}

	freeBytes, totalBytes, ok := aggregateNamedGroup(statuses, namedGroup{
		Name:  "LOCAL ARCH DISKS",
		Paths: []string{"/glftpd/DISK1", "/glftpd/DISK2"},
	})
	if !ok {
		t.Fatalf("expected group to match")
	}
	if freeBytes != 120 || totalBytes != 500 {
		t.Fatalf("aggregateNamedGroup() = %d/%d, want 120/500", freeBytes, totalBytes)
	}
}

func TestNewKeepsDiskStatusAcrossPluginReload(t *testing.T) {
	rememberedStatusStore.Lock()
	rememberedStatusStore.slaves = map[string]diskStatus{}
	rememberedStatusStore.Unlock()
	t.Cleanup(func() {
		rememberedStatusStore.Lock()
		rememberedStatusStore.slaves = map[string]diskStatus{}
		rememberedStatusStore.Unlock()
	})

	first := New()
	first.slaves["LOCAL"] = diskStatus{
		Name:    "LOCAL",
		Free:    10,
		Total:   100,
		Updated: time.Now(),
		Roots: []diskRootStatus{
			{Path: "/glftpd/site", MountPath: "/", Free: 10, Total: 100},
		},
	}
	rememberDiskStatus(first.slaves["LOCAL"])

	reloaded := New()
	got, ok := reloaded.slaves["LOCAL"]
	if !ok {
		t.Fatalf("expected reloaded plugin to keep disk status")
	}
	if got.Free != 10 || got.Total != 100 || len(got.Roots) != 1 {
		t.Fatalf("unexpected remembered status: %+v", got)
	}
}

func TestShowCanHideRawSlaveLines(t *testing.T) {
	p := New()
	if err := p.Initialize(map[string]interface{}{
		"free": map[string]interface{}{
			"show_slave_lines": false,
			"show_total":       false,
			"named_groups": []interface{}{
				map[string]interface{}{
					"name":        "RACE",
					"mount_paths": []interface{}{"/"},
				},
				map[string]interface{}{
					"name":        "ARCHiVE",
					"mount_paths": []interface{}{"/ARCHiVE"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	p.slaves["LOCAL"] = diskStatus{
		Name:      "LOCAL",
		Free:      60,
		Total:     300,
		Online:    true,
		Available: true,
		Updated:   time.Now(),
		Roots: []diskRootStatus{
			{Path: "/glftpd/site", MountPath: "/", Free: 10, Total: 100},
			{Path: "/glftpd/DISK1", MountPath: "/ARCHiVE", Free: 50, Total: 200},
		},
	}

	out := p.show(&event.Event{
		Type: event.EventCommand,
		User: "tester",
		Data: map[string]string{"channel": "#chan"},
	})
	if len(out) != 2 {
		t.Fatalf("expected 2 grouped lines, got %d: %+v", len(out), out)
	}
	joined := out[0].Text + "\n" + out[1].Text
	if strings.Contains(joined, "LOCAL") {
		t.Fatalf("expected raw LOCAL slave line to be hidden, got:\n%s", joined)
	}
	if !strings.Contains(joined, "RACE") || !strings.Contains(joined, "ARCHiVE") {
		t.Fatalf("expected grouped output, got:\n%s", joined)
	}
}
