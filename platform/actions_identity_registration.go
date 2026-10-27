package platform

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"fmt"
	"net/url"
	"strings"

	"github.com/oarkflow/ref/intent"
)

// Self-registration is deliberately bound to one tenant and one role at build
// time. A request cannot grant itself staff or administrator permissions.
func registerIdentityRegistrationActions(r *Registry) {
	mustAction(r, "identity.register_totp", ActionFactoryFunc(buildIdentityRegisterTOTP), ActionInfo{
		Family: "identity", ResourceKind: "identity", Kind: "effect",
		Summary:  "Register a public account and return an authenticator-app URI; confirmation is required before sign-in",
		Provides: "{email, uri}",
		Config: []ConfigField{
			{Name: "tenant", Type: "string", Required: true},
			{Name: "role", Type: "string", Required: true},
			{Name: "issuer", Type: "string", Required: true},
		},
	})
	mustAction(r, "identity.confirm_totp", ActionFactoryFunc(buildIdentityConfirmTOTP), ActionInfo{
		Family: "identity", ResourceKind: "identity", Kind: "effect",
		Summary:  "Confirm an authenticator-app code and activate a pending public account",
		Provides: "{activated}",
	})
}

func buildIdentityRegisterTOTP(build BuildContext, spec NodeSpec) (Action, error) {
	d, err := identityDirectory(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("identity.register_totp", spec.Config, "tenant", "role", "issuer"); err != nil {
		return nil, err
	}
	tenant, err := requiredString(spec.Config, "tenant")
	if err != nil {
		return nil, err
	}
	role, err := requiredString(spec.Config, "role")
	if err != nil {
		return nil, err
	}
	issuer, err := requiredString(spec.Config, "issuer")
	if err != nil {
		return nil, err
	}
	if d.isAdmin([]string{role}) || len(strings.TrimSpace(role)) == 0 {
		return nil, fmt.Errorf("node %q: a public registration role cannot be an admin role", spec.Name)
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		email := optionalFact(ctx, "input.email")
		name := optionalFact(ctx, "input.name")
		password := rawFact(ctx, "input.password")
		uri, err := d.RegisterTOTP(ctx.Context, tenant, role, issuer, email, name, password)
		if err != nil {
			return ActionResult{}, err
		}
		return singleOutput(spec, map[string]any{"email": normalizeEmail(email), "uri": uri}), nil
	}), nil
}

func buildIdentityConfirmTOTP(build BuildContext, spec NodeSpec) (Action, error) {
	d, err := identityDirectory(build, spec)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknownConfig("identity.confirm_totp", spec.Config); err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		err := d.ConfirmTOTP(ctx.Context, optionalFact(ctx, "input.email"), rawFact(ctx, "input.password"), optionalFact(ctx, "input.code"))
		if err != nil {
			return ActionResult{}, err
		}
		return singleOutput(spec, map[string]any{"activated": true}), nil
	}), nil
}

func (d *IdentityDirectory) RegisterTOTP(ctx context.Context, tenant, role, issuer, email, name, password string) (string, error) {
	email = normalizeEmail(email)
	if !validEmail(email) {
		return "", invalidInput("a valid email address is required")
	}
	if tenant == "" || role == "" || issuer == "" {
		return "", invalidInput("tenant, role and issuer are required")
	}
	if d.isAdmin([]string{role}) {
		return "", invalidInput("public registration cannot grant an admin role")
	}
	if len(password) < d.minPassword {
		return "", weakPassword(d.minPassword)
	}
	hash, err := HashPasswordArgon2id(password, d.params)
	if err != nil {
		return "", err
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	d.mu.Lock()
	defer d.mu.Unlock()
	existing, err := d.UserByEmail(ctx, email)
	if err != nil {
		return "", databaseFailure(err)
	}
	if existing != nil {
		return "", conflict("that account already exists")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	id := newPrefixedID("usr")
	now := d.clock().UnixMilli()
	if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO %s (id, email, name, password_hash, status, mfa_enabled, mfa_secret, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8)`, d.users), id, email, name, hash, UserInvited, secret, now, now); err != nil {
		return "", conflict("that account already exists")
	}
	if err := d.insertMembership(ctx, tx, IdentityMembership{TenantID: tenant, UserID: id, Roles: []string{role}}, now); err != nil {
		return "", databaseFailure(err)
	}
	if err := d.record(ctx, tx, id, tenant, "user.self_registered", id, nil); err != nil {
		return "", databaseFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return "", databaseFailure(err)
	}
	label := url.PathEscape(issuer + ":" + email)
	return "otpauth://totp/" + label + "?secret=" + secret + "&issuer=" + url.QueryEscape(issuer) + "&algorithm=SHA1&digits=6&period=30", nil
}

func (d *IdentityDirectory) ConfirmTOTP(ctx context.Context, email, password, code string) error {
	user, err := d.UserByEmail(ctx, email)
	if err != nil {
		return databaseFailure(err)
	}
	if user == nil || user.Status != UserInvited || user.MFASecret == "" {
		_, _ = VerifyPassword(password, d.dummyHash)
		return errInvalidCredentials
	}
	now := d.clock()
	if user.LockedUntil > now.UnixMilli() {
		_, _ = VerifyPassword(password, d.dummyHash)
		return errInvalidCredentials
	}
	match, err := VerifyPassword(password, user.PasswordHash)
	if err != nil || !match || !verifyTOTP(user.MFASecret, code, now) {
		d.recordFailure(ctx, user, now)
		return errInvalidCredentials
	}
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return databaseFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, d.q("UPDATE %s SET status = $1, mfa_enabled = 1, failed_logins = 0, locked_until = 0, updated_at = $2 WHERE id = $3 AND status = $4 AND mfa_enabled = 0", d.users), UserActive, now.UnixMilli(), user.ID, UserInvited)
	if err != nil {
		return databaseFailure(err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return intent.Failure{Code: "ALREADY_CONFIRMED", Category: intent.CategoryConflict, Message: "this registration is no longer pending"}
	}
	if err := d.record(ctx, tx, user.ID, "", "user.totp_confirmed", user.ID, nil); err != nil {
		return databaseFailure(err)
	}
	return databaseFailure(tx.Commit())
}
