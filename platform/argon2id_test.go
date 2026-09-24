package platform

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestArgon2idHashingAndVerification(t *testing.T) {
	password := "SecretPassword123!"
	fastParams := FastArgon2idParams()

	hash, err := HashPasswordArgon2id(password, fastParams)
	if err != nil {
		t.Fatalf("HashPasswordArgon2id failed: %v", err)
	}

	if hash == "" {
		t.Fatal("expected non-empty hash")
	}

	// Correct password
	match, err := VerifyPassword(password, hash)
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if !match {
		t.Fatal("expected password to match")
	}

	// Incorrect password
	matchWrong, err := VerifyPassword("WrongPassword123!", hash)
	if err != nil {
		t.Fatalf("VerifyPassword with wrong password failed: %v", err)
	}
	if matchWrong {
		t.Fatal("expected wrong password not to match")
	}
}

func TestVerifyPasswordBcryptFallback(t *testing.T) {
	password := "BcryptPassword456!"
	bcryptHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt generate failed: %v", err)
	}

	match, err := VerifyPassword(password, string(bcryptHash))
	if err != nil {
		t.Fatalf("VerifyPassword with bcrypt hash failed: %v", err)
	}
	if !match {
		t.Fatal("expected bcrypt password to match")
	}

	matchWrong, err := VerifyPassword("WrongPassword", string(bcryptHash))
	if err != nil {
		t.Fatalf("VerifyPassword failed: %v", err)
	}
	if matchWrong {
		t.Fatal("expected wrong password to fail on bcrypt")
	}
}
