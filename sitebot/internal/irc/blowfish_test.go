package irc

import "testing"

func TestNewBlowfishEncryptorDefaultsToCBC(t *testing.T) {
	enc, err := NewBlowfishEncryptor("PlainKey123")
	if err != nil {
		t.Fatalf("NewBlowfishEncryptor: %v", err)
	}
	if enc.mode != "CBC" {
		t.Fatalf("mode = %q, want %q", enc.mode, "CBC")
	}
}

func TestNewBlowfishEncryptorRejectsExplicitECB(t *testing.T) {
	if _, err := NewBlowfishEncryptor("ecb:PlainKey123"); err == nil {
		t.Fatalf("expected ecb mode to be rejected")
	}
}

func TestDecryptRejectsMalformedCiphertextWithoutPanicking(t *testing.T) {
	cases := []string{
		"cbc:PlainKey123",
		"PlainKey123",
	}
	for _, key := range cases {
		enc, err := NewBlowfishEncryptor(key)
		if err != nil {
			t.Fatalf("NewBlowfishEncryptor(%q): %v", key, err)
		}
		if _, err := enc.Decrypt("AAAA"); err == nil {
			t.Fatalf("Decrypt(%q) unexpectedly succeeded", key)
		}
	}
}
