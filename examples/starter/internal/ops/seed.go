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
