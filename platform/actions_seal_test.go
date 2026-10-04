package platform

import "testing"

func TestSealRoundTrip(t *testing.T) {
	key := "a-sealing-key-of-sufficient-length"
	sealed, err := Seal(key, `{"password":"s3cret"}`)
	if err != nil || sealed == `{"password":"s3cret"}` {
		t.Fatalf("seal = %q, %v", sealed, err)
	}
	if again, _ := Seal(key, `{"password":"s3cret"}`); again == sealed {
		t.Error("two seals of the same text must differ (random nonce)")
	}
	if plain, err := Unseal(key, sealed); err != nil || plain != `{"password":"s3cret"}` {
		t.Fatalf("unseal = %q, %v", plain, err)
	}
	if _, err := Unseal("another-key-of-sufficient-length", sealed); err == nil {
		t.Error("a different key must not open the value")
	}
	if _, err := Unseal(key, "plain text"); err == nil {
		t.Error("unsealed text must be refused")
	}
	if _, err := Seal("short", "x"); err == nil {
		t.Error("a short key must be refused")
	}
}
