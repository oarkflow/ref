package platform

import (
	"context"
	"encoding/base32"
	"net/url"
	"testing"
	"time"
)

func TestPublicRegistrationRequiresAuthenticatorConfirmation(t *testing.T) {
	d := openTestDirectory(t)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	ctx := context.Background()
	uri, err := d.RegisterTOTP(ctx, "t1", "applicant", "REF Test", "public@x.test", "Public", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	secret := parsed.Query().Get("secret")
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(key, d.clock().Unix()/30, 6)
	if _, err := d.Login(ctx, "public@x.test", "a long enough password", "t1", code); err == nil {
		t.Fatal("unconfirmed registration could sign in")
	}
	if err := d.ConfirmTOTP(ctx, "public@x.test", "a long enough password", "000000"); err == nil {
		t.Fatal("wrong authenticator code activated account")
	}
	if err := d.ConfirmTOTP(ctx, "public@x.test", "a long enough password", code); err != nil {
		t.Fatal(err)
	}
	if err := d.ConfirmTOTP(ctx, "public@x.test", "a long enough password", code); err == nil {
		t.Fatal("confirmation was accepted twice")
	}
	if _, err := d.Login(ctx, "public@x.test", "a long enough password", "t1", ""); failureCode(err) != "MFA_REQUIRED" {
		t.Fatalf("login without code: %v", err)
	}
	signed, err := d.Login(ctx, "public@x.test", "a long enough password", "t1", code)
	if err != nil {
		t.Fatal(err)
	}
	if got := signed.Membership.Roles; len(got) != 1 || got[0] != "applicant" {
		t.Fatalf("unexpected self-registered roles: %v", got)
	}
	if _, err := d.RegisterTOTP(ctx, "t1", "applicant", "REF Test", "public@x.test", "Public", "a long enough password"); failureCode(err) != "CONFLICT" {
		t.Fatalf("duplicate registration: %v", err)
	}
}
