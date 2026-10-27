package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/platform"
)

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// runUser manages login accounts for any application whose users live in a
// SQL table. Table and column names are flags, so the command does not assume
// a particular schema.
//
//	ref user create -email a@b.c -password '...' -roles admin <app dir>
//	ref user list <app dir>
func runUser(ctx context.Context, args []string) error {
	if len(args) == 0 || (args[0] != "create" && args[0] != "list") {
		return fmt.Errorf("usage: ref user create|list [flags] <application directory>")
	}
	sub := args[0]
	fs := flag.NewFlagSet("user "+sub, flag.ContinueOnError)
	table := fs.String("table", "users", "users table")
	idCol := fs.String("id-column", "id", "primary key column")
	emailCol := fs.String("email-column", "email", "login column")
	nameCol := fs.String("name-column", "name", "display name column (empty to skip)")
	hashCol := fs.String("hash-column", "password_hash", "password hash column")
	rolesCol := fs.String("roles-column", "roles", "roles column (empty to skip)")
	statusCol := fs.String("status-column", "status", "status column (empty to skip)")
	email := fs.String("email", "", "login email")
	name := fs.String("name", "", "display name")
	password := fs.String("password", os.Getenv("REF_USER_PASSWORD"), "password (default $REF_USER_PASSWORD)")
	roles := fs.String("roles", "", "comma-separated roles")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ref user %s [flags] <application directory>", sub)
	}
	for _, id := range []string{*table, *idCol, *emailCol, *nameCol, *hashCol, *rolesCol, *statusCol} {
		if id != "" && !identifier.MatchString(id) {
			return fmt.Errorf("invalid identifier %q", id)
		}
	}
	appDir, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	for _, name := range []string{".env", ".env.example"} {
		if _, err := os.Stat(filepath.Join(appDir, name)); err == nil {
			if err := config.LoadDotenv(filepath.Join(appDir, name)); err != nil {
				return err
			}
			break
		}
	}
	if err := os.Chdir(appDir); err != nil {
		return err
	}
	driver := config.Env("DB_DRIVER", "sqlite")
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, config.Env("DB_DSN", "file:.data/app.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	if sub == "list" {
		cols := []string{*idCol, *emailCol}
		for _, c := range []string{*nameCol, *rolesCol, *statusCol} {
			if c != "" {
				cols = append(cols, c)
			}
		}
		rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", strings.Join(cols, ", "), *table, *emailCol))
		if err != nil {
			return err
		}
		defer rows.Close()
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, strings.ToUpper(strings.Join(cols, "\t")))
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return err
			}
			out := make([]string, len(vals))
			for i, v := range vals {
				out[i] = v.String
			}
			fmt.Fprintln(w, strings.Join(out, "\t"))
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return w.Flush()
	}

	if *email == "" || *password == "" {
		return fmt.Errorf("-email and a password (-password or $REF_USER_PASSWORD) are required")
	}
	if len(*password) < 12 {
		return fmt.Errorf("the password must contain at least 12 characters")
	}
	hash, err := platform.HashPasswordArgon2id(*password)
	if err != nil {
		return err
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	id := "usr_" + hex.EncodeToString(buf)
	cols := []string{*idCol, *emailCol, *hashCol}
	vals := []any{id, *email, hash}
	for _, c := range []struct{ col, val string }{{*nameCol, *name}, {*rolesCol, *roles}, {*statusCol, "active"}} {
		if c.col != "" && c.val != "" {
			cols = append(cols, c.col)
			vals = append(vals, c.val)
		}
	}
	marks := make([]string, len(cols))
	for i := range marks {
		if driver == "pgx" {
			marks[i] = fmt.Sprintf("$%d", i+1)
		} else {
			marks[i] = "?"
		}
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", *table, strings.Join(cols, ", "), strings.Join(marks, ", "))
	if _, err := db.ExecContext(ctx, query, vals...); err != nil {
		return err
	}
	fmt.Printf("created user %s (%s)\n", *email, id)
	return nil
}
