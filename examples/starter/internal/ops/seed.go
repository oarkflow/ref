package ops

import (
	"context"
	"fmt"

	"github.com/oarkflow/ref/platform"
)

// SeedDevAdmin creates one administrator account, but only when env is not
// "production" and the users table is completely empty — never in
// production, and never a second time once anyone has registered.
//
// This exists so a fresh clone has a way in without a fixed password hash
// sitting in a BCL migration in version control: the hash is computed at
// startup from AdminPassword, and env-gating means production can never
// get this account no matter what resources/config/01_resources.bcl says.
func SeedDevAdmin(ctx context.Context, db *platform.Database, env, email, password string) error {
	if env == "production" {
		return nil
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("counting users: %w", err)
	}
	if count > 0 {
		return nil
	}
	hash, err := platform.HashPasswordArgon2id(password)
	if err != nil {
		return fmt.Errorf("hashing dev admin password: %w", err)
	}
	// database.query/exec nodes accept $N placeholders for every dialect
	// (platform rebinds them internally); calling *sql.DB directly here
	// bypasses that, so the placeholder syntax must match the real driver.
	statement := "INSERT INTO users (id, email, name, password_hash, roles, status) VALUES ('usr_admin_01', LOWER($1), 'Admin', $2, 'admin', 'active')"
	if db.Dialect != "postgres" {
		statement = "INSERT INTO users (id, email, name, password_hash, roles, status) VALUES ('usr_admin_01', LOWER(?), 'Admin', ?, 'admin', 'active')"
	}
	if _, err := db.ExecContext(ctx, statement, email, hash); err != nil {
		return fmt.Errorf("inserting dev admin: %w", err)
	}
	return nil
}

// devRoleAccount is one fixed demo login SeedDevTodoRoleAccounts creates.
type devRoleAccount struct {
	id, email, name, role string
}

// SeedDevTodoRoleAccounts creates one demo login per role the todo workflow
// example's process (resources/config/12_todo_workflow_example.bcl) actually
// gates on — "user", "reviewer" and "approver" — so a fresh clone can walk
// the whole draft -> review -> approval flow by logging out and back in as
// each one, instead of hand-editing the users table to promote a role
// (which is the only other way in: registration always assigns "user", by
// design — see resources/config/03_intents.bcl's auth.register).
//
// Same safety gates as SeedDevAdmin: never in production, and each account
// is only inserted if its email is not already taken, so re-running this on
// every boot is a no-op once the accounts exist (or once a real clone has
// renamed/removed them).
func SeedDevTodoRoleAccounts(ctx context.Context, db *platform.Database, env, password string) error {
	if env == "production" {
		return nil
	}
	hash, err := platform.HashPasswordArgon2id(password)
	if err != nil {
		return fmt.Errorf("hashing dev role account password: %w", err)
	}
	accounts := []devRoleAccount{
		{"usr_demo_owner_01", "owner@example.com", "Demo Owner", "user"},
		{"usr_demo_reviewer_01", "reviewer@example.com", "Demo Reviewer", "reviewer"},
		{"usr_demo_approver_01", "approver@example.com", "Demo Approver", "approver"},
	}
	countStatement := "SELECT COUNT(*) FROM users WHERE email = LOWER($1)"
	if db.Dialect != "postgres" {
		countStatement = "SELECT COUNT(*) FROM users WHERE email = LOWER(?)"
	}
	for _, a := range accounts {
		var count int
		if err := db.QueryRowContext(ctx, countStatement, a.email).Scan(&count); err != nil {
			return fmt.Errorf("counting existing %s: %w", a.email, err)
		}
		if count > 0 {
			continue
		}
		statement := "INSERT INTO users (id, email, name, password_hash, roles, status) VALUES ($1, LOWER($2), $3, $4, $5, 'active')"
		if db.Dialect != "postgres" {
			statement = "INSERT INTO users (id, email, name, password_hash, roles, status) VALUES (?, LOWER(?), ?, ?, ?, 'active')"
		}
		if _, err := db.ExecContext(ctx, statement, a.id, a.email, a.name, hash, a.role); err != nil {
			return fmt.Errorf("inserting %s: %w", a.email, err)
		}
	}
	return nil
}
