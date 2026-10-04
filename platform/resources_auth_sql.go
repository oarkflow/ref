package platform

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
)

// auth.api_key_sql authenticates API keys that live in a table.
//
// auth.api_key holds a fixed set of keys in the configuration, which is right
// for a service-to-service call and wrong for customers: keys are issued and
// revoked at run time. This kind keeps only a digest of each key in the
// database and runs the application's own query to find its owner:
//
//	resource "customer_auth" {
//	  kind "auth.api_key_sql"
//	  config {
//	    database "db"
//	    query "SELECT id, tenant, name FROM users WHERE api_key_hash = $1 AND status <> 'deleted'"
//	    roles ["sender"]
//	  }
//	}
//
// $1 is the SHA-256 (hex) of the presented key. The first column is the
// principal id; the others become claims (principal.claims.tenant, …), and
// a column named roles (comma separated) adds roles. The comparison is a query
// on a digest, never on the key, and every failure is the same unauthenticated
// answer.

type sqlKeyAuth struct {
	db     *Database
	query  string
	roles  []string
	hasher func(string) string
}

func registerSQLKeyAuth(r *Registry) {
	mustResource(r, "auth.api_key_sql", ResourceFactoryFunc(openSQLKeyAuth), ResourceKindInfo{
		Family:   "auth",
		Summary:  "API keys stored as digests in a table and looked up by the application's own query.",
		Provides: []string{"Authenticator"},
		Config: []ConfigField{
			{Name: "database", Type: "resource", Required: true},
			{Name: "query", Type: "sql", Required: true, Summary: "Takes the key's SHA-256 hex as $1; first column is the principal id, the rest are claims"},
			{Name: "roles", Type: "[]string", Summary: "Roles every authenticated principal gets"},
		},
	})
}

func openSQLKeyAuth(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("auth.api_key_sql", spec.Config, "database", "query", "roles"); err != nil {
		return nil, nil, err
	}
	db, err := requireSQLHandle(spec, "database")
	if err != nil {
		return nil, nil, err
	}
	query, err := requiredString(spec.Config, "query")
	if err != nil {
		return nil, nil, fmt.Errorf("resource %q: %w", spec.Name, err)
	}
	if err := db.CheckStatement(query); err != nil {
		return nil, nil, fmt.Errorf("resource %q: %w", spec.Name, err)
	}
	return &sqlKeyAuth{db: db, query: query, roles: configStrings(spec.Config, "roles"), hasher: hashKey}, nil, nil
}

// Authenticate implements spi.Authenticator.
func (a *sqlKeyAuth) Authenticate(ctx context.Context, creds Credentials) (Principal, error) {
	presented := creds.APIKey
	if presented == "" {
		presented = creds.BearerToken
	}
	if strings.TrimSpace(presented) == "" {
		return Principal{}, errUnauthenticated
	}
	rows, err := a.db.QueryContext(ctx, a.query, a.hasher(presented))
	if err != nil {
		return Principal{}, errUnauthenticated
	}
	defer rows.Close()
	if !rows.Next() {
		return Principal{}, errUnauthenticated
	}
	columns, err := rows.Columns()
	if err != nil || len(columns) == 0 {
		return Principal{}, errUnauthenticated
	}
	values := make([]any, len(columns))
	dest := make([]any, len(columns))
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return Principal{}, errUnauthenticated
	}
	p := Principal{ID: Stringify(scanValue(values[0])), Roles: append([]string(nil), a.roles...), Claims: map[string]any{}}
	if p.ID == "" {
		return Principal{}, errUnauthenticated
	}
	for i := 1; i < len(columns); i++ {
		v := scanValue(values[i])
		switch strings.ToLower(columns[i]) {
		case "roles":
			for _, role := range strings.Split(Stringify(v), ",") {
				if role = strings.TrimSpace(role); role != "" {
					p.Roles = append(p.Roles, role)
				}
			}
		case "username", "name":
			p.Username = Stringify(v)
			p.Claims[columns[i]] = v
		case "email":
			p.Email = Stringify(v)
			p.Claims[columns[i]] = v
		default:
			p.Claims[columns[i]] = v
		}
	}
	return p, nil
}

func scanValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

var _ = sql.ErrNoRows
