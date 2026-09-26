package platform

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/oarkflow/ref/intent"
)

// ---------------------------------------------------------------------------
// Directory
// ---------------------------------------------------------------------------

func openTestDirectory(t *testing.T) *IdentityDirectory {
	t.Helper()
	ctx := context.Background()
	dbRes, closer, err := openDatabase(ctx, ResourceSpec{Name: "db", Kind: "database.sql", Config: map[string]any{
		"driver": "sqlite", "dsn": "file:" + filepath.Join(t.TempDir(), "id.db") + "?_pragma=busy_timeout(5000)"}})
	if err != nil {
		t.Fatal(err)
	}
	if closer != nil {
		t.Cleanup(func() { _ = closer.Close() })
	}
	res, _, err := openIdentityDirectory(ctx, ResourceSpec{Name: "users", Kind: "identity.users",
		Config: map[string]any{
			"database": "db", "argon2_memory": 8192, "argon2_iterations": 1, "argon2_parallelism": 1,
			"max_failed_logins": 3, "global_roles": []any{"platform_admin"},
			"bootstrap": map[string]any{"email": "root@x.test", "password": "root password 123", "tenant": "t1"},
		},
		resolved: map[string]Resource{"db": dbRes}})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*IdentityDirectory)
}

func failureCode(err error) string {
	var f intent.Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}

func TestInvitationIsSingleUseUnderConcurrency(t *testing.T) {
	d := openTestDirectory(t)
	ctx := context.Background()
	root, _ := d.UserByEmail(ctx, "root@x.test")
	actor := Principal{ID: root.ID, TenantID: "t1"}
	inv, err := d.Invite(ctx, actor, "t1", "new@x.test", "", []string{"viewer"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := d.AcceptInvite(ctx, inv.Token, "a long enough password", ""); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			} else if code := failureCode(err); code != "INVALID_INVITATION" && code != "CONFLICT" {
				t.Errorf("unexpected failure: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d acceptances of a single-use invitation", wins)
	}
}

func TestLoginLockoutAndTenantChoice(t *testing.T) {
	d := openTestDirectory(t)
	ctx := context.Background()
	root, _ := d.UserByEmail(ctx, "root@x.test")
	if _, err := d.AddMembership(ctx, Principal{ID: "op", Roles: []string{"platform_admin"}}, "t2", root.ID, "", []string{"viewer"}, "unit-9"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Login(ctx, "root@x.test", "root password 123", "", ""); failureCode(err) != "TENANT_REQUIRED" {
		t.Fatalf("two tenants without a choice: %v", err)
	}
	res, err := d.Login(ctx, "root@x.test", "root password 123", "t2", "")
	if err != nil || res.Principal.TenantID != "t2" || res.Principal.Roles[0] != "viewer" {
		t.Fatalf("login to t2: %v %+v", err, res)
	}
	if _, err := d.Login(ctx, "root@x.test", "root password 123", "t3", ""); failureCode(err) != "PERMISSION_DENIED" {
		t.Fatalf("login to a foreign tenant: %v", err)
	}
	// Three failures lock the account; the lock looks like any failure.
	for range 3 {
		if _, err := d.Login(ctx, "root@x.test", "wrong wrong wrong", "t1", ""); failureCode(err) != "INVALID_CREDENTIALS" {
			t.Fatalf("wrong password: %v", err)
		}
	}
	if _, err := d.Login(ctx, "root@x.test", "root password 123", "t1", ""); failureCode(err) != "INVALID_CREDENTIALS" {
		t.Fatalf("locked account: %v", err)
	}
	d.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if _, err := d.Login(ctx, "root@x.test", "root password 123", "t1", ""); err != nil {
		t.Fatalf("after the lock expired: %v", err)
	}
}

func TestLoginUnknownUserCostsAVerification(t *testing.T) {
	d := openTestDirectory(t)
	ctx := context.Background()
	measure := func(email string) time.Duration {
		start := time.Now()
		for range 5 {
			_, _ = d.Login(ctx, email, "not the right password", "t1", "")
		}
		return time.Since(start)
	}
	d.maxFailed = 0 // no lockout while measuring
	known, unknown := measure("root@x.test"), measure("ghost@x.test")
	// The unknown path runs the same argon2id verification; a missing one
	// would make it orders of magnitude faster.
	if unknown < known/5 {
		t.Fatalf("unknown user took %v, known user %v: the unknown path skips the hash", unknown, known)
	}
}
