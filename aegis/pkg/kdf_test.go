package pkg

import "testing"

func TestDeriveKeySameSaltReproducesKey(t *testing.T) {
	key1, info, err := DeriveKey("correct horse battery staple", KDFInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Salt == "" {
		t.Fatal("DeriveKey did not generate a salt")
	}
	if len(key1) != 32 {
		t.Fatalf("key is %d bytes, want 32", len(key1))
	}

	key2, _, err := DeriveKey("correct horse battery staple", info)
	if err != nil {
		t.Fatal(err)
	}
	if string(key1) != string(key2) {
		t.Fatal("the same passphrase and salt produced different keys")
	}

	key3, _, err := DeriveKey("a different passphrase", info)
	if err != nil {
		t.Fatal(err)
	}
	if string(key1) == string(key3) {
		t.Fatal("different passphrases produced the same key")
	}
}

func TestDeriveKeyDifferentSaltsDiffer(t *testing.T) {
	key1, info1, err := DeriveKey("same passphrase", KDFInfo{})
	if err != nil {
		t.Fatal(err)
	}
	key2, info2, err := DeriveKey("same passphrase", KDFInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info1.Salt == info2.Salt {
		t.Fatal("two calls with no salt given produced the same salt")
	}
	if string(key1) == string(key2) {
		t.Fatal("two random salts produced the same key for the same passphrase")
	}
}

func TestDeriveKeyRejectsUnknownAlgorithm(t *testing.T) {
	if _, _, err := DeriveKey("x", KDFInfo{Algorithm: "scrypt"}); err == nil {
		t.Fatal("expected an error for an unsupported algorithm")
	}
}

func TestDeriveKeyRejectsBadSalt(t *testing.T) {
	if _, _, err := DeriveKey("x", KDFInfo{Salt: "not-hex"}); err == nil {
		t.Fatal("expected an error for a non-hex salt")
	}
	if _, _, err := DeriveKey("x", KDFInfo{Salt: "aabb"}); err == nil {
		t.Fatal("expected an error for a too-short salt")
	}
}
