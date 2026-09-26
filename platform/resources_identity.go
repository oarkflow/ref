package platform

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/ref/intent"
)

// The identity.users resource: a built-in, SQL-backed directory of people who
// administer and use an application — users, their memberships in tenants
// (roles and an optional org unit), single-use invitations, and an audit trail
// of every administrative change.
//
// The decisions worth knowing before reading the code:
//
//   - Authority comes from the database, not from the token. An admin action
//     re-reads the caller's membership, so demoting or suspending an admin
//     takes effect on their next request, not when their token expires.
//   - Every sign-in failure looks the same. Unknown email, wrong password,
//     locked account and a pending invitation all produce INVALID_CREDENTIALS,
//     and each path performs one argon2id verification with the directory's
//     own parameters, so neither the answer nor the time it takes says whether
//     an account exists. That also makes the endpoint safe to rate-limit by
//     remote address: a limiter sees one uniform failure.
//   - A tenant always keeps an administrator. Removing, demoting or
//     suspending its last active admin is refused, inside the same
//     transaction that would have done it.
//   - Invitation tokens are stored as SHA-256 hashes and consumed with a
//     conditional UPDATE, so a token works once even when two requests race.

// Account statuses.
const (
	UserActive    = "active"
	UserSuspended = "suspended"
	UserInvited   = "invited"
)

// IdentityDirectory is the identity.users resource.
type IdentityDirectory struct {
	name        string
	db          *Database
	users       string
	memberships string
	invitations string
	audit       string
	adminRoles  []string
	globalRoles []string
	inviteTTL   time.Duration
	minPassword int
	params      Argon2idParams
	maxFailed   int
	lockout     time.Duration
	issuer      *jwtAuth
	// dummyHash is a hash of nothing anyone will present, made with this
	// directory's own parameters, so an unknown email costs one real
	// verification — the same as a known one.
	dummyHash string
	now       func() time.Time

	// mu serialises membership and status changes within this process; the
	// transactions (row locks on PostgreSQL and MySQL) serialise them across
	// replicas.
	mu sync.Mutex
}

// IdentityUser is one account as the directory stores it.
type IdentityUser struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	Status       string
	MFAEnabled   bool
	MFASecret    string
	FailedLogins int
	LockedUntil  int64
	CreatedAt    int64
	UpdatedAt    int64
	LastLogin    int64
}

// IdentityMembership is a user's place in one tenant.
type IdentityMembership struct {
	TenantID string
	UserID   string
	Roles    []string
	OrgUnit  string
}

func registerIdentityResources(r *Registry) {
	mustResource(r, "identity.users", ResourceFactoryFunc(openIdentityDirectory), ResourceKindInfo{
		Family:   "identity",
		Summary:  "Built-in user directory: argon2id passwords, tenant memberships with roles and org units, single-use invitations, an audit trail, and sign-in that issues auth.jwt tokens",
		Provides: []string{"IdentityDirectory"},
		Config: []ConfigField{
			{Name: "database", Type: "resource", Required: true, Summary: "database.sql resource (SQLite, PostgreSQL or MySQL)"},
			{Name: "table_prefix", Type: "string", Default: "identity_", Summary: "Tables: <prefix>users, memberships, invitations, audit"},
			{Name: "migrate", Type: "bool", Default: "true"},
			{Name: "token_issuer", Type: "resource", Summary: "auth.jwt resource identity.login signs tokens with"},
			{Name: "admin_roles", Type: "[]string", Default: "[admin]", Summary: "Membership roles that administer a tenant's users"},
			{Name: "global_roles", Type: "[]string", Summary: "Principal roles that administer every tenant (platform operators)"},
			{Name: "invite_ttl", Type: "duration", Default: "72h"},
			{Name: "password_min_length", Type: "int", Default: "12"},
			{Name: "argon2_memory", Type: "int", Default: "65536", Summary: "KiB"},
			{Name: "argon2_iterations", Type: "int", Default: "3"},
			{Name: "argon2_parallelism", Type: "int", Default: "4"},
			{Name: "max_failed_logins", Type: "int", Default: "10", Summary: "Consecutive failures before a temporary lock (0 disables)"},
			{Name: "lockout", Type: "duration", Default: "15m"},
			{Name: "bootstrap", Type: "object", Summary: "First administrator, created while the directory has no users: { email password|password_hash tenant roles name org_unit }"},
		},
	})
}

func openIdentityDirectory(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("identity.users", spec.Config,
		"database", "table_prefix", "migrate", "token_issuer", "admin_roles", "global_roles", "invite_ttl",
		"password_min_length", "argon2_memory", "argon2_iterations", "argon2_parallelism",
		"max_failed_logins", "lockout", "bootstrap"); err != nil {
		return nil, nil, err
	}
	db, err := requireSQLHandle(spec, "database")
	if err != nil {
		return nil, nil, err
	}
	prefix := configString(spec.Config, "table_prefix", "identity_")
	d := &IdentityDirectory{name: spec.Name, db: db, now: time.Now}
	for _, t := range []struct {
		dst  *string
		name string
	}{{&d.users, "users"}, {&d.memberships, "memberships"}, {&d.invitations, "invitations"}, {&d.audit, "audit"}} {
		if *t.dst, err = safeIdentifier(prefix + t.name); err != nil {
			return nil, nil, fmt.Errorf("identity.users %q: table_prefix: %w", spec.Name, err)
		}
	}
	d.adminRoles = configStrings(spec.Config, "admin_roles")
	if len(d.adminRoles) == 0 {
		d.adminRoles = []string{"admin"}
	}
	d.globalRoles = configStrings(spec.Config, "global_roles")
	if d.inviteTTL, err = configDuration(spec.Config, "invite_ttl", 72*time.Hour); err != nil {
		return nil, nil, err
	}
	if d.lockout, err = configDuration(spec.Config, "lockout", 15*time.Minute); err != nil {
		return nil, nil, err
	}
	if d.minPassword, err = configInt(spec.Config, "password_min_length", 12); err != nil {
		return nil, nil, err
	}
	if d.maxFailed, err = configInt(spec.Config, "max_failed_logins", 10); err != nil {
		return nil, nil, err
	}
	defaults := DefaultArgon2idParams()
	memory, err := configInt(spec.Config, "argon2_memory", int(defaults.Memory))
	if err != nil {
		return nil, nil, err
	}
	iterations, err := configInt(spec.Config, "argon2_iterations", int(defaults.Iterations))
	if err != nil {
		return nil, nil, err
	}
	parallelism, err := configInt(spec.Config, "argon2_parallelism", int(defaults.Parallelism))
	if err != nil {
		return nil, nil, err
	}
	if memory < 8*1024 || iterations < 1 || parallelism < 1 || parallelism > 255 {
		return nil, nil, fmt.Errorf("identity.users %q: argon2 needs memory >= 8192 KiB, iterations >= 1 and parallelism 1-255", spec.Name)
	}
	d.params = Argon2idParams{Memory: uint32(memory), Iterations: uint32(iterations), Parallelism: uint8(parallelism), SaltLength: 16, KeyLength: 32}
	if d.dummyHash, err = HashPasswordArgon2id(newToken(24), d.params); err != nil {
		return nil, nil, err
	}
	if name := configString(spec.Config, "token_issuer", ""); name != "" {
		issuer, ok := spec.resolved[name].(*jwtAuth)
		if !ok {
			return nil, nil, fmt.Errorf("identity.users %q: token_issuer %q is not an auth.jwt resource", spec.Name, name)
		}
		d.issuer = issuer
	}
	if configBool(spec.Config, "migrate", true) {
		if err := d.migrate(ctx); err != nil {
			return nil, nil, fmt.Errorf("identity.users %q: migrate: %w", spec.Name, err)
		}
	}
	if boot := configMap(spec.Config, "bootstrap"); len(boot) > 0 {
		if err := d.bootstrap(ctx, boot); err != nil {
			return nil, nil, fmt.Errorf("identity.users %q: bootstrap: %w", spec.Name, err)
		}
	}
	return d, nil, nil
}

func (d *IdentityDirectory) migrate(ctx context.Context) error {
	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id            VARCHAR(64)  NOT NULL PRIMARY KEY,
			email         VARCHAR(191) NOT NULL UNIQUE,
			name          TEXT,
			password_hash TEXT,
			status        VARCHAR(16)  NOT NULL,
			mfa_enabled   INTEGER      NOT NULL DEFAULT 0,
			mfa_secret    TEXT,
			failed_logins INTEGER      NOT NULL DEFAULT 0,
			locked_until  BIGINT       NOT NULL DEFAULT 0,
			created_at    BIGINT       NOT NULL,
			updated_at    BIGINT       NOT NULL,
			last_login    BIGINT       NOT NULL DEFAULT 0)`, d.users),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			tenant_id  VARCHAR(191) NOT NULL,
			user_id    VARCHAR(64)  NOT NULL,
			roles      TEXT,
			org_unit   VARCHAR(191) NOT NULL DEFAULT '',
			created_at BIGINT       NOT NULL,
			updated_at BIGINT       NOT NULL,
			PRIMARY KEY (tenant_id, user_id))`, d.memberships),
		fmt.Sprintf(`CREATE INDEX %s %s_user_idx ON %s (user_id)`, d.ifNotExists(), d.memberships, d.memberships),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			token_hash  VARCHAR(64)  NOT NULL PRIMARY KEY,
			email       VARCHAR(191) NOT NULL,
			user_id     VARCHAR(64)  NOT NULL,
			tenant_id   VARCHAR(191) NOT NULL,
			roles       TEXT,
			org_unit    VARCHAR(191) NOT NULL DEFAULT '',
			invited_by  VARCHAR(191) NOT NULL DEFAULT '',
			created_at  BIGINT       NOT NULL,
			expires_at  BIGINT       NOT NULL,
			accepted_at BIGINT       NOT NULL DEFAULT 0)`, d.invitations),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id        VARCHAR(64)  NOT NULL PRIMARY KEY,
			at        BIGINT       NOT NULL,
			actor     VARCHAR(191) NOT NULL DEFAULT '',
			tenant_id VARCHAR(191) NOT NULL DEFAULT '',
			action    VARCHAR(64)  NOT NULL,
			target    VARCHAR(191) NOT NULL DEFAULT '',
			detail    TEXT)`, d.audit),
		fmt.Sprintf(`CREATE INDEX %s %s_tenant_idx ON %s (tenant_id, at)`, d.ifNotExists(), d.audit, d.audit),
	}
	for _, statement := range statements {
		if _, err := d.db.ExecContext(ctx, statement); err != nil {
			// MySQL has no CREATE INDEX IF NOT EXISTS; a second start finds the
			// index already there.
			if indexExists(d.db.Dialect, err) {
				continue
			}
			return err
		}
	}
	return nil
}

func (d *IdentityDirectory) ifNotExists() string {
	if d.db.Dialect == "mysql" {
		return ""
	}
	return "IF NOT EXISTS"
}

// bootstrap creates the first administrator while the directory is empty, so
// a fresh deployment has someone who can invite everyone else.
func (d *IdentityDirectory) bootstrap(ctx context.Context, config map[string]any) error {
	email := normalizeEmail(configString(config, "email", ""))
	tenant := configString(config, "tenant", configString(config, "tenant_id", ""))
	if email == "" || tenant == "" {
		return errors.New("bootstrap needs an email and a tenant")
	}
	var count int64
	if err := d.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", d.users)).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	hash := configString(config, "password_hash", "")
	if hash == "" {
		password := configString(config, "password", "")
		if len(password) < d.minPassword {
			return fmt.Errorf("the bootstrap password must contain at least %d characters (or set password_hash)", d.minPassword)
		}
		var err error
		if hash, err = HashPasswordArgon2id(password, d.params); err != nil {
			return err
		}
	}
	roles := configStrings(config, "roles")
	if len(roles) == 0 {
		roles = []string{d.adminRoles[0]}
	}
	now := d.clock().UnixMilli()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	id := newPrefixedID("usr")
	if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO %[1]s (id, email, name, password_hash, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, d.users), id, email, configString(config, "name", ""), hash, UserActive, now, now); err != nil {
		return err
	}
	if err := d.insertMembership(ctx, tx, IdentityMembership{TenantID: tenant, UserID: id, Roles: roles, OrgUnit: configString(config, "org_unit", "")}, now); err != nil {
		return err
	}
	if err := d.record(ctx, tx, "system", tenant, "user.bootstrapped", id, map[string]any{"email": email, "roles": roles}); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (d *IdentityDirectory) clock() time.Time { return d.now().UTC() }

// q formats a statement with the table names and rebinds its placeholders.
func (d *IdentityDirectory) q(format string, tables ...any) string {
	return rebind(d.db.Dialect, fmt.Sprintf(format, tables...))
}

// forUpdate locks the rows a guard reads, where the dialect can.
func (d *IdentityDirectory) forUpdate() string {
	if d.db.Dialect == "sqlite" {
		return ""
	}
	return " FOR UPDATE"
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func validEmail(email string) bool {
	at := strings.IndexByte(email, '@')
	return at > 0 && at < len(email)-1 && !strings.ContainsAny(email, " \t\r\n") && strings.Count(email, "@") == 1
}

func hashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	}
	f, _ := ToFloat(v)
	return int64(f)
}

func encodeRoles(roles []string) string {
	clean := make([]string, 0, len(roles))
	for _, r := range roles {
		if r = strings.TrimSpace(r); r != "" && !slices.Contains(clean, r) {
			clean = append(clean, r)
		}
	}
	sort.Strings(clean)
	raw, _ := json.Marshal(clean)
	return string(raw)
}

func decodeRoles(v any) []string {
	text := Stringify(v)
	if text == "" {
		return []string{}
	}
	var roles []string
	if json.Unmarshal([]byte(text), &roles) != nil {
		return stringList(text)
	}
	return roles
}

func millisTime(ms int64) any {
	if ms == 0 {
		return nil
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

func (d *IdentityDirectory) isAdmin(roles []string) bool {
	for _, r := range roles {
		if slices.Contains(d.adminRoles, r) {
			return true
		}
	}
	return false
}

func (d *IdentityDirectory) isGlobal(p Principal) bool {
	for _, r := range p.Roles {
		if slices.Contains(d.globalRoles, r) {
			return true
		}
	}
	return false
}

const identityUserColumns = "id, email, name, password_hash, status, mfa_enabled, mfa_secret, failed_logins, locked_until, created_at, updated_at, last_login"

func userFromRow(row map[string]any) *IdentityUser {
	return &IdentityUser{
		ID:           Stringify(row["id"]),
		Email:        Stringify(row["email"]),
		Name:         Stringify(row["name"]),
		PasswordHash: Stringify(row["password_hash"]),
		Status:       Stringify(row["status"]),
		MFAEnabled:   asInt64(row["mfa_enabled"]) != 0,
		MFASecret:    Stringify(row["mfa_secret"]),
		FailedLogins: int(asInt64(row["failed_logins"])),
		LockedUntil:  asInt64(row["locked_until"]),
		CreatedAt:    asInt64(row["created_at"]),
		UpdatedAt:    asInt64(row["updated_at"]),
		LastLogin:    asInt64(row["last_login"]),
	}
}

// View renders a user without secrets.
func (u *IdentityUser) View() map[string]any {
	return map[string]any{
		"id": u.ID, "email": u.Email, "name": u.Name, "status": u.Status,
		"mfa_enabled": u.MFAEnabled, "created_at": millisTime(u.CreatedAt), "last_login": millisTime(u.LastLogin),
	}
}

func (d *IdentityDirectory) userBy(ctx context.Context, db execer, column string, value any) (*IdentityUser, error) {
	rows, err := queryRows(ctx, db, d.q("SELECT "+identityUserColumns+" FROM %s WHERE "+column+" = $1", d.users), []any{value})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return userFromRow(rows[0]), nil
}

// UserByEmail looks a user up by email (nil when there is none).
func (d *IdentityDirectory) UserByEmail(ctx context.Context, email string) (*IdentityUser, error) {
	return d.userBy(ctx, d.db.DB, "email", normalizeEmail(email))
}

// UserByID looks a user up by id (nil when there is none).
func (d *IdentityDirectory) UserByID(ctx context.Context, id string) (*IdentityUser, error) {
	return d.userBy(ctx, d.db.DB, "id", id)
}

func membershipFromRow(row map[string]any) IdentityMembership {
	return IdentityMembership{
		TenantID: Stringify(row["tenant_id"]),
		UserID:   Stringify(row["user_id"]),
		Roles:    decodeRoles(row["roles"]),
		OrgUnit:  Stringify(row["org_unit"]),
	}
}

// Memberships lists a user's tenants.
func (d *IdentityDirectory) Memberships(ctx context.Context, db execer, userID string) ([]IdentityMembership, error) {
	rows, err := queryRows(ctx, db, d.q("SELECT tenant_id, user_id, roles, org_unit FROM %s WHERE user_id = $1 ORDER BY tenant_id", d.memberships), []any{userID})
	if err != nil {
		return nil, err
	}
	out := make([]IdentityMembership, 0, len(rows))
	for _, row := range rows {
		out = append(out, membershipFromRow(row))
	}
	return out, nil
}

func (d *IdentityDirectory) membership(ctx context.Context, db execer, tenant, userID string) (*IdentityMembership, error) {
	rows, err := queryRows(ctx, db, d.q("SELECT tenant_id, user_id, roles, org_unit FROM %s WHERE tenant_id = $1 AND user_id = $2", d.memberships), []any{tenant, userID})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	m := membershipFromRow(rows[0])
	return &m, nil
}

func (d *IdentityDirectory) insertMembership(ctx context.Context, db execer, m IdentityMembership, now int64) error {
	_, err := db.ExecContext(ctx, d.q(`INSERT INTO %s (tenant_id, user_id, roles, org_unit, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, d.memberships), m.TenantID, m.UserID, encodeRoles(m.Roles), m.OrgUnit, now, now)
	return err
}

// record appends an audit entry inside the caller's transaction, so the trail
// and the change it describes commit together.
func (d *IdentityDirectory) record(ctx context.Context, db execer, actor, tenant, action, target string, detail map[string]any) error {
	var encoded any
	if len(detail) > 0 {
		raw, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		encoded = string(raw)
	}
	_, err := db.ExecContext(ctx, d.q(`INSERT INTO %s (id, at, actor, tenant_id, action, target, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, d.audit),
		newPrefixedID("aud"), d.clock().UnixMilli(), actor, tenant, action, target, encoded)
	return err
}

// ---------------------------------------------------------------------------
// Failures
// ---------------------------------------------------------------------------

var (
	errAccountSuspended = intent.Failure{Code: "ACCOUNT_SUSPENDED", Category: intent.CategoryPermission, Message: "this account is suspended"}
	errInvalidInvite    = intent.Failure{Code: "INVALID_INVITATION", Category: intent.CategoryInvalidInput, Message: "this invitation is not valid: it may have expired or already been used"}
	errLastAdmin        = intent.Failure{Code: "LAST_ADMIN", Category: intent.CategoryConflict, Message: "a tenant must keep at least one active administrator"}
	errMFARequired      = intent.Failure{Code: "MFA_REQUIRED", Category: intent.CategoryAuth, Message: "a one-time code is required"}
)

func weakPassword(min int) error {
	return intent.Failure{Code: "WEAK_PASSWORD", Category: intent.CategoryInvalidInput,
		Message: fmt.Sprintf("the password must contain at least %d characters", min)}
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

// LoginResult is a successful sign-in.
type LoginResult struct {
	User       *IdentityUser
	Membership IdentityMembership
	Principal  Principal
}

// Login verifies a password and resolves the membership for tenant (or the
// user's only tenant when tenant is empty).
func (d *IdentityDirectory) Login(ctx context.Context, email, password, tenant, code string) (*LoginResult, error) {
	user, err := d.UserByEmail(ctx, email)
	if err != nil {
		return nil, databaseFailure(err)
	}
	now := d.clock()
	if user == nil || user.PasswordHash == "" || password == "" || user.LockedUntil > now.UnixMilli() {
		// One verification on every failure path, with this directory's own
		// parameters: unknown, invited and locked accounts cost the same as a
		// wrong password.
		_, _ = VerifyPassword(password, d.dummyHash)
		return nil, errInvalidCredentials
	}
	match, err := VerifyPassword(password, user.PasswordHash)
	if err != nil || !match {
		d.recordFailure(ctx, user, now)
		return nil, errInvalidCredentials
	}
	if user.Status == UserSuspended {
		// The caller proved who they are, so the truth is safe to tell and
		// more useful than "invalid credentials".
		return nil, errAccountSuspended
	}
	if user.Status != UserActive {
		return nil, errInvalidCredentials
	}
	if user.MFAEnabled {
		if code == "" {
			return nil, errMFARequired
		}
		if !verifyTOTP(user.MFASecret, code, now) {
			d.recordFailure(ctx, user, now)
			return nil, errInvalidCredentials
		}
	}
	memberships, err := d.Memberships(ctx, d.db.DB, user.ID)
	if err != nil {
		return nil, databaseFailure(err)
	}
	var chosen *IdentityMembership
	for i := range memberships {
		if tenant == "" && len(memberships) == 1 || memberships[i].TenantID == tenant && tenant != "" {
			chosen = &memberships[i]
			break
		}
	}
	if chosen == nil {
		if tenant == "" && len(memberships) > 1 {
			tenants := make([]string, len(memberships))
			for i, m := range memberships {
				tenants[i] = m.TenantID
			}
			return nil, intent.Failure{Code: "TENANT_REQUIRED", Category: intent.CategoryInvalidInput,
				Message: "choose a tenant: " + strings.Join(tenants, ", ")}
		}
		return nil, permissionDenied("you are not a member of that tenant")
	}
	if _, err := d.db.ExecContext(ctx, d.q("UPDATE %s SET last_login = $1, failed_logins = 0, locked_until = 0 WHERE id = $2", d.users),
		now.UnixMilli(), user.ID); err != nil {
		return nil, databaseFailure(err)
	}
	user.LastLogin, user.FailedLogins = now.UnixMilli(), 0
	_ = d.record(ctx, d.db.DB, user.ID, chosen.TenantID, "user.login", user.ID, nil)

	principal := Principal{
		ID:       user.ID,
		Username: user.Email,
		Email:    user.Email,
		TenantID: chosen.TenantID,
		Roles:    slices.Clone(chosen.Roles),
		Claims:   map[string]any{"name": user.Name},
	}
	if chosen.OrgUnit != "" {
		// org_units is org.hierarchy's default assignment claim, so a
		// membership's unit scopes the user there without further wiring.
		principal.Claims["org_units"] = []string{chosen.OrgUnit}
	}
	return &LoginResult{User: user, Membership: *chosen, Principal: principal}, nil
}

// recordFailure counts a failed attempt and locks the account for a while
// after too many in a row. The lock is silent (the caller keeps seeing
// INVALID_CREDENTIALS), so it cannot be used to discover accounts.
func (d *IdentityDirectory) recordFailure(ctx context.Context, user *IdentityUser, now time.Time) {
	failed := user.FailedLogins + 1
	var locked int64
	if d.maxFailed > 0 && failed >= d.maxFailed {
		locked, failed = now.Add(d.lockout).UnixMilli(), 0
	}
	_, _ = d.db.ExecContext(ctx, d.q("UPDATE %s SET failed_logins = $1, locked_until = $2 WHERE id = $3", d.users), failed, locked, user.ID)
}

func verifyTOTP(secret, code string, now time.Time) bool {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil || len(key) == 0 {
		return false
	}
	step := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		if totpCode(key, step+offset, 6) == code {
			return true
		}
	}
	return false
}

// ChangePassword replaces a signed-in user's password after checking the
// current one.
func (d *IdentityDirectory) ChangePassword(ctx context.Context, userID, current, next string) error {
	user, err := d.UserByID(ctx, userID)
	if err != nil {
		return databaseFailure(err)
	}
	if user == nil || user.PasswordHash == "" {
		_, _ = VerifyPassword(current, d.dummyHash)
		return errInvalidCredentials
	}
	if match, err := VerifyPassword(current, user.PasswordHash); err != nil || !match {
		return errInvalidCredentials
	}
	if user.Status != UserActive {
		return errAccountSuspended
	}
	if len(next) < d.minPassword {
		return weakPassword(d.minPassword)
	}
	hash, err := HashPasswordArgon2id(next, d.params)
	if err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, d.q("UPDATE %s SET password_hash = $1, updated_at = $2, failed_logins = 0, locked_until = 0 WHERE id = $3", d.users),
		hash, d.clock().UnixMilli(), user.ID); err != nil {
		return databaseFailure(err)
	}
	if err := d.record(ctx, tx, user.ID, "", "user.password_changed", user.ID, nil); err != nil {
		return databaseFailure(err)
	}
	return databaseFailure(tx.Commit())
}

// ---------------------------------------------------------------------------
// Administration
// ---------------------------------------------------------------------------

// Authorize checks that actor administers tenant: a global role, or an admin
// role in the actor's current membership of that tenant (read from the
// database, so a demotion takes effect immediately).
func (d *IdentityDirectory) Authorize(ctx context.Context, actor Principal, tenant string) error {
	if actor.ID == "" {
		return errUnauthenticated
	}
	if d.isGlobal(actor) {
		return nil
	}
	if tenant == "" {
		return invalidInput("a tenant is required")
	}
	m, err := d.membership(ctx, d.db.DB, tenant, actor.ID)
	if err != nil {
		return databaseFailure(err)
	}
	if m == nil || !d.isAdmin(m.Roles) {
		return permissionDenied("you do not administer this tenant")
	}
	user, err := d.UserByID(ctx, actor.ID)
	if err != nil {
		return databaseFailure(err)
	}
	if user == nil || user.Status != UserActive {
		return permissionDenied("you do not administer this tenant")
	}
	return nil
}

// Invitation is a freshly minted invitation. Token is the only copy of the
// plaintext: deliver it and drop it.
type Invitation struct {
	Token     string
	Email     string
	UserID    string
	TenantID  string
	Roles     []string
	OrgUnit   string
	ExpiresAt time.Time
}

// Invite creates (or reuses) an account for email and an invitation to join
// tenant with roles.
func (d *IdentityDirectory) Invite(ctx context.Context, actor Principal, tenant, email, name string, roles []string, orgUnit string) (*Invitation, error) {
	email = normalizeEmail(email)
	if !validEmail(email) {
		return nil, invalidInput("a valid email address is required")
	}
	if tenant == "" {
		return nil, invalidInput("a tenant is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := d.clock()
	user, err := d.userBy(ctx, tx, "email", email)
	if err != nil {
		return nil, databaseFailure(err)
	}
	if user == nil {
		user = &IdentityUser{ID: newPrefixedID("usr"), Email: email, Name: name, Status: UserInvited}
		if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO %s (id, email, name, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, d.users), user.ID, email, name, UserInvited, now.UnixMilli(), now.UnixMilli()); err != nil {
			return nil, databaseFailure(err)
		}
	} else {
		if user.Status == UserSuspended {
			return nil, conflict("that account is suspended")
		}
		if m, err := d.membership(ctx, tx, tenant, user.ID); err != nil {
			return nil, databaseFailure(err)
		} else if m != nil {
			return nil, conflict("%s is already a member of this tenant", email)
		}
	}
	token := newToken(32)
	inv := &Invitation{Token: token, Email: email, UserID: user.ID, TenantID: tenant, Roles: decodeRoles(encodeRoles(roles)),
		OrgUnit: orgUnit, ExpiresAt: now.Add(d.inviteTTL).Truncate(time.Millisecond)}
	if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO %s (token_hash, email, user_id, tenant_id, roles, org_unit, invited_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, d.invitations),
		hashInviteToken(token), email, user.ID, tenant, encodeRoles(roles), orgUnit, actor.ID, now.UnixMilli(), inv.ExpiresAt.UnixMilli()); err != nil {
		return nil, databaseFailure(err)
	}
	if err := d.record(ctx, tx, actor.ID, tenant, "user.invited", user.ID, map[string]any{"email": email, "roles": inv.Roles, "org_unit": orgUnit}); err != nil {
		return nil, databaseFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseFailure(err)
	}
	return inv, nil
}

// AcceptInvite consumes an invitation. A new account sets its password here;
// an existing account proves itself with its current password instead, so an
// invitation can add a membership but never take an account over.
func (d *IdentityDirectory) AcceptInvite(ctx context.Context, token, password, name string) (*IdentityUser, *IdentityMembership, error) {
	if token == "" {
		return nil, nil, errInvalidInvite
	}
	digest := hashInviteToken(token)
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := d.clock()
	rows, err := queryRows(ctx, tx, d.q(`SELECT email, user_id, tenant_id, roles, org_unit, invited_by, expires_at, accepted_at
		FROM %s WHERE token_hash = $1`, d.invitations), []any{digest})
	if err != nil {
		return nil, nil, databaseFailure(err)
	}
	if len(rows) == 0 || asInt64(rows[0]["accepted_at"]) != 0 || asInt64(rows[0]["expires_at"]) <= now.UnixMilli() {
		return nil, nil, errInvalidInvite
	}
	inv := rows[0]
	user, err := d.userBy(ctx, tx, "id", Stringify(inv["user_id"]))
	if err != nil {
		return nil, nil, databaseFailure(err)
	}
	if user == nil {
		return nil, nil, errInvalidInvite
	}
	switch {
	case user.Status == UserSuspended:
		return nil, nil, errAccountSuspended
	case user.PasswordHash == "":
		if len(password) < d.minPassword {
			return nil, nil, weakPassword(d.minPassword)
		}
		hash, err := HashPasswordArgon2id(password, d.params)
		if err != nil {
			return nil, nil, err
		}
		if name == "" {
			name = user.Name
		}
		if _, err := tx.ExecContext(ctx, d.q("UPDATE %s SET password_hash = $1, name = $2, status = $3, updated_at = $4 WHERE id = $5", d.users),
			hash, name, UserActive, now.UnixMilli(), user.ID); err != nil {
			return nil, nil, databaseFailure(err)
		}
		user.PasswordHash, user.Name, user.Status = hash, name, UserActive
	default:
		if match, err := VerifyPassword(password, user.PasswordHash); err != nil || !match {
			return nil, nil, errInvalidCredentials
		}
	}
	// The conditional update is what makes the token single-use: of two
	// requests racing with it, exactly one changes a row.
	result, err := tx.ExecContext(ctx, d.q("UPDATE %s SET accepted_at = $1 WHERE token_hash = $2 AND accepted_at = 0 AND expires_at > $3", d.invitations),
		now.UnixMilli(), digest, now.UnixMilli())
	if err != nil {
		return nil, nil, databaseFailure(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return nil, nil, errInvalidInvite
	}
	m := IdentityMembership{TenantID: Stringify(inv["tenant_id"]), UserID: user.ID, Roles: decodeRoles(inv["roles"]), OrgUnit: Stringify(inv["org_unit"])}
	if existing, err := d.membership(ctx, tx, m.TenantID, user.ID); err != nil {
		return nil, nil, databaseFailure(err)
	} else if existing != nil {
		return nil, nil, conflict("you are already a member of this tenant")
	}
	if err := d.insertMembership(ctx, tx, m, now.UnixMilli()); err != nil {
		return nil, nil, databaseFailure(err)
	}
	if err := d.record(ctx, tx, user.ID, m.TenantID, "invitation.accepted", user.ID, map[string]any{"invited_by": Stringify(inv["invited_by"]), "roles": m.Roles}); err != nil {
		return nil, nil, databaseFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, databaseFailure(err)
	}
	return user, &m, nil
}

// MemberView is a user together with their membership in one tenant.
func memberView(u *IdentityUser, m *IdentityMembership) map[string]any {
	view := u.View()
	if m != nil {
		view["tenant_id"], view["roles"], view["org_unit"] = m.TenantID, m.Roles, m.OrgUnit
	}
	return view
}

// ListMembers returns a tenant's members.
func (d *IdentityDirectory) ListMembers(ctx context.Context, tenant string) ([]any, error) {
	rows, err := queryRows(ctx, d.db.DB, d.q(`SELECT u.id, u.email, u.name, u.status, u.mfa_enabled, u.created_at, u.last_login,
		m.tenant_id, m.user_id, m.roles, m.org_unit
		FROM %s m JOIN %s u ON u.id = m.user_id WHERE m.tenant_id = $1 ORDER BY u.email`, d.memberships, d.users), []any{tenant})
	if err != nil {
		return nil, databaseFailure(err)
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		m := membershipFromRow(row)
		out = append(out, memberView(userFromRow(row), &m))
	}
	return out, nil
}

// ListInvitations returns a tenant's pending invitations (never their tokens).
func (d *IdentityDirectory) ListInvitations(ctx context.Context, tenant string) ([]any, error) {
	rows, err := queryRows(ctx, d.db.DB, d.q(`SELECT email, user_id, roles, org_unit, invited_by, created_at, expires_at
		FROM %s WHERE tenant_id = $1 AND accepted_at = 0 AND expires_at > $2 ORDER BY created_at`, d.invitations),
		[]any{tenant, d.clock().UnixMilli()})
	if err != nil {
		return nil, databaseFailure(err)
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"email": Stringify(row["email"]), "user_id": Stringify(row["user_id"]), "roles": decodeRoles(row["roles"]),
			"org_unit": Stringify(row["org_unit"]), "invited_by": Stringify(row["invited_by"]),
			"created_at": millisTime(asInt64(row["created_at"])), "expires_at": millisTime(asInt64(row["expires_at"])),
		})
	}
	return out, nil
}

// GetMember returns one member of tenant (a global actor also sees every
// membership the user holds).
func (d *IdentityDirectory) GetMember(ctx context.Context, actor Principal, tenant, userID string) (map[string]any, error) {
	user, err := d.UserByID(ctx, userID)
	if err != nil {
		return nil, databaseFailure(err)
	}
	var m *IdentityMembership
	if user != nil {
		if m, err = d.membership(ctx, d.db.DB, tenant, userID); err != nil {
			return nil, databaseFailure(err)
		}
	}
	global := d.isGlobal(actor)
	if user == nil || (m == nil && !global) {
		return nil, notFound("user", userID)
	}
	view := memberView(user, m)
	if global {
		all, err := d.Memberships(ctx, d.db.DB, userID)
		if err != nil {
			return nil, databaseFailure(err)
		}
		list := make([]any, 0, len(all))
		for _, x := range all {
			list = append(list, map[string]any{"tenant_id": x.TenantID, "roles": x.Roles, "org_unit": x.OrgUnit})
		}
		view["memberships"] = list
	}
	return view, nil
}

// guardLastAdmin refuses a change that would leave tenant without an active
// administrator. It reads (and, where the dialect can, locks) every
// membership of the tenant, so two concurrent demotions serialise.
func (d *IdentityDirectory) guardLastAdmin(ctx context.Context, tx *sql.Tx, tenant, userID string, remaining func(IdentityMembership, string) bool) error {
	rows, err := queryRows(ctx, tx, d.q(`SELECT m.tenant_id, m.user_id, m.roles, m.org_unit, u.status
		FROM %s m JOIN %s u ON u.id = m.user_id WHERE m.tenant_id = $1`+d.forUpdate(), d.memberships, d.users), []any{tenant})
	if err != nil {
		return databaseFailure(err)
	}
	before, after := 0, 0
	for _, row := range rows {
		m := membershipFromRow(row)
		status := Stringify(row["status"])
		if status == UserActive && d.isAdmin(m.Roles) {
			before++
		}
		if m.UserID == userID {
			if remaining(m, status) {
				after++
			}
			continue
		}
		if status == UserActive && d.isAdmin(m.Roles) {
			after++
		}
	}
	if before > 0 && after == 0 {
		return errLastAdmin
	}
	return nil
}

// AddMembership adds an existing user to tenant.
func (d *IdentityDirectory) AddMembership(ctx context.Context, actor Principal, tenant, userID, email string, roles []string, orgUnit string) (map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	var user *IdentityUser
	if userID != "" {
		user, err = d.userBy(ctx, tx, "id", userID)
	} else {
		user, err = d.userBy(ctx, tx, "email", normalizeEmail(email))
	}
	if err != nil {
		return nil, databaseFailure(err)
	}
	if user == nil {
		return nil, notFound("user", firstNonEmpty(userID, email))
	}
	if existing, err := d.membership(ctx, tx, tenant, user.ID); err != nil {
		return nil, databaseFailure(err)
	} else if existing != nil {
		return nil, conflict("%s is already a member of this tenant", user.Email)
	}
	m := IdentityMembership{TenantID: tenant, UserID: user.ID, Roles: decodeRoles(encodeRoles(roles)), OrgUnit: orgUnit}
	if err := d.insertMembership(ctx, tx, m, d.clock().UnixMilli()); err != nil {
		return nil, databaseFailure(err)
	}
	if err := d.record(ctx, tx, actor.ID, tenant, "membership.added", user.ID, map[string]any{"roles": m.Roles, "org_unit": orgUnit}); err != nil {
		return nil, databaseFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseFailure(err)
	}
	return memberView(user, &m), nil
}

// ChangeMembership replaces a member's roles and/or org unit (nil roles or a
// nil org unit keep the current value).
func (d *IdentityDirectory) ChangeMembership(ctx context.Context, actor Principal, tenant, userID string, roles []string, orgUnit *string) (map[string]any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := d.membership(ctx, tx, tenant, userID)
	if err != nil {
		return nil, databaseFailure(err)
	}
	if current == nil {
		return nil, notFound("membership", userID)
	}
	next := *current
	if roles != nil {
		next.Roles = decodeRoles(encodeRoles(roles))
	}
	if orgUnit != nil {
		next.OrgUnit = *orgUnit
	}
	if err := d.guardLastAdmin(ctx, tx, tenant, userID, func(_ IdentityMembership, status string) bool {
		return status == UserActive && d.isAdmin(next.Roles)
	}); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, d.q("UPDATE %s SET roles = $1, org_unit = $2, updated_at = $3 WHERE tenant_id = $4 AND user_id = $5", d.memberships),
		encodeRoles(next.Roles), next.OrgUnit, d.clock().UnixMilli(), tenant, userID); err != nil {
		return nil, databaseFailure(err)
	}
	if err := d.record(ctx, tx, actor.ID, tenant, "membership.changed", userID, map[string]any{
		"roles": next.Roles, "previous_roles": current.Roles, "org_unit": next.OrgUnit}); err != nil {
		return nil, databaseFailure(err)
	}
	user, err := d.userBy(ctx, tx, "id", userID)
	if err != nil {
		return nil, databaseFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseFailure(err)
	}
	return memberView(user, &next), nil
}

// RemoveMembership removes a user from tenant.
func (d *IdentityDirectory) RemoveMembership(ctx context.Context, actor Principal, tenant, userID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := d.membership(ctx, tx, tenant, userID)
	if err != nil {
		return databaseFailure(err)
	}
	if current == nil {
		return notFound("membership", userID)
	}
	if err := d.guardLastAdmin(ctx, tx, tenant, userID, func(IdentityMembership, string) bool { return false }); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, d.q("DELETE FROM %s WHERE tenant_id = $1 AND user_id = $2", d.memberships), tenant, userID); err != nil {
		return databaseFailure(err)
	}
	if err := d.record(ctx, tx, actor.ID, tenant, "membership.removed", userID, map[string]any{"roles": current.Roles}); err != nil {
		return databaseFailure(err)
	}
	return databaseFailure(tx.Commit())
}

// SetStatus suspends or reactivates an account. Suspension is account-wide,
// so a tenant administrator may only suspend someone who belongs to no other
// tenant; anyone else needs a global role.
func (d *IdentityDirectory) SetStatus(ctx context.Context, actor Principal, tenant, userID, status string) (map[string]any, error) {
	if status != UserActive && status != UserSuspended {
		return nil, invalidInput("status must be active or suspended")
	}
	if userID == actor.ID {
		return nil, permissionDenied("you cannot change the status of your own account")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	user, err := d.userBy(ctx, tx, "id", userID)
	if err != nil {
		return nil, databaseFailure(err)
	}
	memberships, err := d.Memberships(ctx, tx, userID)
	if err != nil {
		return nil, databaseFailure(err)
	}
	global := d.isGlobal(actor)
	member := slices.ContainsFunc(memberships, func(m IdentityMembership) bool { return m.TenantID == tenant })
	if user == nil || (!member && !global) {
		return nil, notFound("user", userID)
	}
	if !global && slices.ContainsFunc(memberships, func(m IdentityMembership) bool { return m.TenantID != tenant }) {
		return nil, permissionDenied("this user belongs to other tenants too; only a global administrator can change their account status")
	}
	switch {
	case user.Status == status:
		return memberView(user, nil), nil
	case user.Status == UserInvited:
		return nil, conflict("the user has not accepted their invitation yet")
	}
	if status == UserSuspended {
		for _, m := range memberships {
			if err := d.guardLastAdmin(ctx, tx, m.TenantID, userID, func(IdentityMembership, string) bool { return false }); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, d.q("UPDATE %s SET status = $1, updated_at = $2, failed_logins = 0, locked_until = 0 WHERE id = $3", d.users),
		status, d.clock().UnixMilli(), userID); err != nil {
		return nil, databaseFailure(err)
	}
	action := "user.reactivated"
	if status == UserSuspended {
		action = "user.suspended"
	}
	if err := d.record(ctx, tx, actor.ID, tenant, action, userID, nil); err != nil {
		return nil, databaseFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, databaseFailure(err)
	}
	user.Status = status
	var m *IdentityMembership
	for i := range memberships {
		if memberships[i].TenantID == tenant {
			m = &memberships[i]
		}
	}
	return memberView(user, m), nil
}

// AuditTrail returns a tenant's most recent audit entries, newest first.
func (d *IdentityDirectory) AuditTrail(ctx context.Context, tenant string, limit int) ([]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := queryRows(ctx, d.db.DB, d.q(`SELECT id, at, actor, tenant_id, action, target, detail FROM %s
		WHERE tenant_id = $1 ORDER BY at DESC, id DESC LIMIT `+strconv.Itoa(limit), d.audit), []any{tenant})
	if err != nil {
		return nil, databaseFailure(err)
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		entry := map[string]any{
			"id": Stringify(row["id"]), "at": millisTime(asInt64(row["at"])), "actor": Stringify(row["actor"]),
			"tenant_id": Stringify(row["tenant_id"]), "action": Stringify(row["action"]), "target": Stringify(row["target"]),
		}
		if detail := decodeJSONObject(row["detail"]); detail != nil {
			entry["detail"] = detail
		}
		out = append(out, entry)
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
