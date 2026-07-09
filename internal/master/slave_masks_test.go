package master

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaskMatchesIP(t *testing.T) {
	cases := []struct {
		mask string
		ip   string
		want bool
	}{
		{"1.2.3.4/32", "1.2.3.4", true},
		{"1.2.3.4/32", "1.2.3.5", false},
		{"1.2.3.0/24", "1.2.3.200", true},
		{"1.2.3.0/24", "1.2.4.1", false},
		{"1.2.3.*", "1.2.3.55", true},
		{"1.2.3.*", "1.2.4.55", false},
		{"1.2.*.4", "1.2.99.4", true},
		{"1.2.*.4", "1.2.99.5", false},
		{"127.0.0.1", "127.0.0.1", true},
		{"127.0.0.1", "127.0.0.2", false},
		{"", "127.0.0.1", false},
	}
	for _, tc := range cases {
		if got := maskMatchesIP(tc.mask, tc.ip); got != tc.want {
			t.Errorf("maskMatchesIP(%q, %q) = %v, want %v", tc.mask, tc.ip, got, tc.want)
		}
	}
}

func TestValidateMaskSyntax(t *testing.T) {
	valid := []string{"1.2.3.4/32", "1.2.3.0/24", "1.2.3.*", "1.2.3.4", "::1"}
	for _, m := range valid {
		if err := validateMaskSyntax(m); err != nil {
			t.Errorf("expected %q to be valid, got error: %v", m, err)
		}
	}
	invalid := []string{"", "not-an-ip", "1.2.3.999", "1.2.3", "1.2.3.4/abc"}
	for _, m := range invalid {
		if err := validateMaskSyntax(m); err == nil {
			t.Errorf("expected %q to be invalid", m)
		}
	}
}

func TestSlaveManagerMasksAddRemoveList(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	dir := t.TempDir()
	maskFile := filepath.Join(dir, "slave_masks.txt")
	if err := sm.ConfigureSlaveMasksFile(maskFile); err != nil {
		t.Fatalf("ConfigureSlaveMasksFile: %v", err)
	}

	if masks := sm.ListSlaveMasks("SLAVE1"); len(masks) != 0 {
		t.Fatalf("expected no masks for unregistered slave, got %v", masks)
	}

	if err := sm.AddSlaveMask("SLAVE1", "1.2.3.4/32"); err != nil {
		t.Fatalf("AddSlaveMask: %v", err)
	}
	if err := sm.AddSlaveMask("SLAVE1", "1.2.3.4/32"); err != nil { // duplicate, no-op
		t.Fatalf("AddSlaveMask duplicate: %v", err)
	}
	if err := sm.AddSlaveMask("SLAVE1", "bad mask"); err == nil {
		t.Fatal("expected error for invalid mask syntax")
	}

	masks := sm.ListSlaveMasks("SLAVE1")
	if len(masks) != 1 || masks[0] != "1.2.3.4/32" {
		t.Fatalf("expected [\"1.2.3.4/32\"], got %v", masks)
	}

	if !sm.slaveMaskAllows("SLAVE1", "1.2.3.4") {
		t.Fatal("expected SLAVE1 to be allowed from 1.2.3.4")
	}
	if sm.slaveMaskAllows("SLAVE1", "9.9.9.9") {
		t.Fatal("expected SLAVE1 to be denied from 9.9.9.9")
	}
	// Fail closed: a slave name with zero masks is always denied.
	if sm.slaveMaskAllows("NEVERREGISTERED", "127.0.0.1") {
		t.Fatal("expected fail-closed denial for a slave with no registered masks")
	}

	removed, err := sm.RemoveSlaveMask("SLAVE1", "1.2.3.4/32")
	if err != nil || !removed {
		t.Fatalf("RemoveSlaveMask: removed=%v err=%v", removed, err)
	}
	if len(sm.ListSlaveMasks("SLAVE1")) != 0 {
		t.Fatal("expected no masks after removal")
	}
	removed, err = sm.RemoveSlaveMask("SLAVE1", "1.2.3.4/32")
	if err != nil || removed {
		t.Fatalf("expected second removal to report not-found, got removed=%v err=%v", removed, err)
	}

	// Persistence round-trip: a fresh SlaveManager loading the same file
	// should see what was saved.
	if err := sm.AddSlaveMask("SLAVE2", "10.0.0.0/8"); err != nil {
		t.Fatalf("AddSlaveMask SLAVE2: %v", err)
	}
	if _, err := os.Stat(maskFile); err != nil {
		t.Fatalf("expected mask file to exist: %v", err)
	}

	sm2 := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	if err := sm2.ConfigureSlaveMasksFile(maskFile); err != nil {
		t.Fatalf("ConfigureSlaveMasksFile (reload): %v", err)
	}
	all := sm2.ListAllSlaveMasks()
	if len(all["SLAVE2"]) != 1 || all["SLAVE2"][0] != "10.0.0.0/8" {
		t.Fatalf("expected reloaded masks for SLAVE2, got %v", all)
	}
	if _, ok := all["SLAVE1"]; ok {
		t.Fatalf("did not expect SLAVE1 entry after its only mask was removed, got %v", all)
	}
}
