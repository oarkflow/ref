package pkg

import (
	"strings"
	"testing"
)

func TestOpenRoundTrips(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()
	builder, err := NewBuilder("open-test", "1.0.0", ks, encKey)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("arbitrary bytes, not necessarily an IR module")
	sealed, err := builder.BuildPayload(payload, "", 0)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Open(sealed, encKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()
	builder, _ := NewBuilder("open-test", "1.0.0", ks, encKey)
	sealed, _ := builder.BuildPayload([]byte("secret"), "", 0)

	wrongKey := make([]byte, 32)
	copy(wrongKey, "a different 32-byte key entirely")
	if _, err := Open(sealed, wrongKey); err == nil {
		t.Fatal("expected Open to reject a wrong key")
	}
}

func TestOpenRejectsTamperedPayload(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()
	builder, _ := NewBuilder("open-test", "1.0.0", ks, encKey)
	sealed, _ := builder.BuildPayload([]byte("secret"), "", 0)

	sealed.Payload[len(sealed.Payload)-1] ^= 0xFF
	_, err := Open(sealed, encKey)
	if err == nil {
		t.Fatal("expected Open to reject a tampered payload")
	}
	if !strings.Contains(err.Error(), "hash mismatch") && !strings.Contains(err.Error(), "decryption failed") {
		t.Errorf("unexpected error for tampered payload: %v", err)
	}
}

func TestOpenRejectsWrongKeyLength(t *testing.T) {
	ks := testKeySet()
	encKey := testEncryptionKey()
	builder, _ := NewBuilder("open-test", "1.0.0", ks, encKey)
	sealed, _ := builder.BuildPayload([]byte("secret"), "", 0)

	if _, err := Open(sealed, []byte("too short")); err == nil {
		t.Fatal("expected Open to reject a key that is not 32 bytes")
	}
}

func TestOpenRejectsNilPackage(t *testing.T) {
	if _, err := Open(nil, testEncryptionKey()); err == nil {
		t.Fatal("expected Open to reject a nil package")
	}
}
