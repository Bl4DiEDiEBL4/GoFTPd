package core

import "testing"

func TestVerifyPasswordSupportsLegacyGlftpdHash(t *testing.T) {
	hash := "$01020304$d631702386055e6797948aa58b4551b2ba70492a"
	if !VerifyPassword("secret", hash) {
		t.Fatalf("VerifyPassword() = false, want true for valid legacy glFTPD hash")
	}
	if VerifyPassword("wrong", hash) {
		t.Fatalf("VerifyPassword() = true, want false for wrong password")
	}
}
