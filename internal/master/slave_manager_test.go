package master

import (
	"testing"
	"time"
)

func TestSlaveAuthGuardBansAfterConfiguredFailures(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.ConfigureAuthGuard(2, time.Minute, 10*time.Minute)

	sm.recordAuthFailure("1.2.3.4", "1.2.3.4:1234", "unexpected EOF")
	if banned, _ := sm.isAuthBanned("1.2.3.4"); banned {
		t.Fatalf("IP should not be banned after first failed handshake")
	}

	sm.recordAuthFailure("1.2.3.4", "1.2.3.4:1234", "unexpected EOF")
	if banned, _ := sm.isAuthBanned("1.2.3.4"); !banned {
		t.Fatalf("IP should be banned after reaching the failure limit")
	}
}

func TestSlaveAuthGuardClearsOnSuccessfulSlaveLogin(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	sm.ConfigureAuthGuard(2, time.Minute, 10*time.Minute)

	sm.recordAuthFailure("1.2.3.4", "1.2.3.4:1234", "unexpected EOF")
	sm.clearAuthState("1.2.3.4")

	if banned, _ := sm.isAuthBanned("1.2.3.4"); banned {
		t.Fatalf("IP should not remain banned after state is cleared")
	}
}

func TestSlaveAuthAllowlistSupportsExactIPsAndCIDRs(t *testing.T) {
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	if err := sm.ConfigureAuthAllowlist([]string{"1.2.3.4", "10.0.0.0/8"}); err != nil {
		t.Fatalf("ConfigureAuthAllowlist returned error: %v", err)
	}

	if !sm.isAuthAllowed("1.2.3.4") {
		t.Fatalf("exact allowlist IP should be allowed")
	}
	if !sm.isAuthAllowed("10.5.6.7") {
		t.Fatalf("CIDR allowlist IP should be allowed")
	}
	if sm.isAuthAllowed("8.8.8.8") {
		t.Fatalf("non-allowlisted IP should not be allowed")
	}
}

func TestSlaveAuthDenylistAddRemove(t *testing.T) {
	dir := t.TempDir()
	sm := NewSlaveManager("127.0.0.1", 1099, false, "", "", 60*time.Second)
	if err := sm.ConfigureAuthDenylistFile(dir + "/slave_denylist.txt"); err != nil {
		t.Fatalf("ConfigureAuthDenylistFile returned error: %v", err)
	}

	entry, err := sm.AddAuthDenyEntry("1.2.3.4")
	if err != nil {
		t.Fatalf("AddAuthDenyEntry returned error: %v", err)
	}
	if entry != "1.2.3.4" {
		t.Fatalf("unexpected canonical entry %q", entry)
	}

	if denied, _ := sm.isAuthExplicitlyDenied("1.2.3.4"); !denied {
		t.Fatalf("expected IP to be denylisted")
	}

	removed, err := sm.RemoveAuthDenyEntry("1.2.3.4")
	if err != nil {
		t.Fatalf("RemoveAuthDenyEntry returned error: %v", err)
	}
	if !removed {
		t.Fatalf("expected denylist entry to be removed")
	}

	if denied, _ := sm.isAuthExplicitlyDenied("1.2.3.4"); denied {
		t.Fatalf("expected IP to be removed from denylist")
	}
}
