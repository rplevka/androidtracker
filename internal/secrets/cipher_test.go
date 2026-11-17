package secrets

import (
	"bytes"
	"testing"
)

func TestCipherRoundTripAndAuthentication(t *testing.T) {
	cipher, err := New("a-test-admin-token-with-enough-length")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt([]byte("broker-password"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("broker-password")) {
		t.Fatal("ciphertext contains plaintext")
	}
	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != "broker-password" {
		t.Fatalf("decrypted = %q", decrypted)
	}
	encrypted[len(encrypted)-1] ^= 0xff
	if _, err := cipher.Decrypt(encrypted); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestCipherRequiresAdminToken(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("empty admin token was accepted")
	}
}
