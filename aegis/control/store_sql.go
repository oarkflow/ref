package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// SQLStore is a Store backed by a SQL database, for a control server that
// must survive a restart — the gap phase 4 of
// docs/aegis-protection-plan.md calls out: MemoryStore forgets every
// package, grant, device and revocation the moment the process exits.
//
// It is written against plain database/sql with portable SQL (no
// driver-specific syntax), so the same code works with any driver the
// caller opens; OpenSQLite is a convenience for the common case of a local
// file, matching every other sqlite use in this repository
// (modernc.org/sqlite, no cgo).
type SQLStore struct {
	db *sql.DB
}

// OpenSQLite opens (creating if necessary) a SQLite-backed Store at path.
// Use ":memory:" only for tests that want a fresh SQL store without a file
// — it is not the same as NewMemoryStore and exists so the same schema and
// queries this type uses in production are exercised in tests too.
func OpenSQLite(path string) (*SQLStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("control: open sqlite store: %w", err)
	}
	db.SetMaxOpenConns(1) // modernc.org/sqlite serializes writes anyway; avoid "database is locked"
	return NewSQLStore(db)
}

// NewSQLStore wraps an already-open *sql.DB (any driver) as a Store,
// creating its tables if they do not exist.
func NewSQLStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("control: sql store needs a non-nil *sql.DB")
	}
	s := &SQLStore{db: db}
	if err := s.migrate(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

func (s *SQLStore) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS control_packages (
			package_id TEXT PRIMARY KEY,
			application_id TEXT,
			version TEXT,
			build_id TEXT,
			manifest_json TEXT,
			revoked INTEGER NOT NULL DEFAULT 0,
			revoked_at TEXT,
			revoke_reason TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS control_master_keys (
			package_id TEXT PRIMARY KEY,
			key_hex TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS control_grants (
			grant_id TEXT PRIMARY KEY,
			session_id TEXT,
			package_id TEXT,
			application_id TEXT,
			device_id TEXT,
			key_hex TEXT,
			issued_at TEXT,
			expires_at TEXT,
			revoked INTEGER NOT NULL DEFAULT 0,
			revoked_at TEXT,
			revoke_reason TEXT,
			conditions_json TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS control_devices (
			device_id TEXT PRIMARY KEY,
			platform TEXT,
			registered_at TEXT,
			last_seen_at TEXT,
			revoked INTEGER NOT NULL DEFAULT 0,
			revoked_at TEXT,
			revoke_reason TEXT,
			attestation_evidence TEXT,
			metadata_json TEXT
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("control: migrate: %w", err)
		}
	}
	return nil
}

const timeLayout = time.RFC3339Nano

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func formatTimePtr(t *time.Time) sql.NullString {
	if t == nil || t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*t), Valid: true}
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeLayout, s)
}

func parseTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// StorePackage persists or updates package metadata.
func (s *SQLStore) StorePackage(ctx context.Context, meta PackageRecord) error {
	if meta.PackageID == "" {
		return errors.New("control: package id cannot be empty")
	}
	now := time.Now().UTC()
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	meta.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO control_packages (package_id, application_id, version, build_id, manifest_json, revoked, revoked_at, revoke_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(package_id) DO UPDATE SET
			application_id=excluded.application_id, version=excluded.version, build_id=excluded.build_id,
			manifest_json=excluded.manifest_json, revoked=excluded.revoked, revoked_at=excluded.revoked_at,
			revoke_reason=excluded.revoke_reason, updated_at=excluded.updated_at`,
		meta.PackageID, meta.ApplicationID, meta.Version, meta.BuildID, string(meta.ManifestJSON),
		boolToInt(meta.Revoked), formatTimePtr(meta.RevokedAt), meta.RevokeReason,
		formatTime(meta.CreatedAt), formatTime(meta.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("control: store package: %w", err)
	}
	return nil
}

// GetPackage retrieves package metadata by unique package ID.
func (s *SQLStore) GetPackage(ctx context.Context, packageID string) (*PackageRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT package_id, application_id, version, build_id, manifest_json, revoked, revoked_at, revoke_reason, created_at, updated_at
		FROM control_packages WHERE package_id = ?`, packageID)
	var (
		rec        PackageRecord
		manifest   string
		revokedInt int
		revokedAt  sql.NullString
		createdAt  string
		updatedAt  string
	)
	if err := row.Scan(&rec.PackageID, &rec.ApplicationID, &rec.Version, &rec.BuildID, &manifest,
		&revokedInt, &revokedAt, &rec.RevokeReason, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %q", ErrPackageNotFound, packageID)
		}
		return nil, fmt.Errorf("control: get package: %w", err)
	}
	if manifest != "" {
		rec.ManifestJSON = []byte(manifest)
	}
	rec.Revoked = revokedInt != 0
	var err error
	if rec.RevokedAt, err = parseTimePtr(revokedAt); err != nil {
		return nil, fmt.Errorf("control: get package: revoked_at: %w", err)
	}
	if rec.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("control: get package: created_at: %w", err)
	}
	if rec.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("control: get package: updated_at: %w", err)
	}
	return &rec, nil
}

// RevokePackage marks a package version as revoked with an explanatory reason.
func (s *SQLStore) RevokePackage(ctx context.Context, packageID, reason string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE control_packages SET revoked=1, revoked_at=?, revoke_reason=?, updated_at=? WHERE package_id=?`,
		formatTime(now), reason, formatTime(now), packageID)
	if err != nil {
		return fmt.Errorf("control: revoke package: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Mirror MemoryStore: revoking an unregistered package creates a stub.
		return s.StorePackage(ctx, PackageRecord{PackageID: packageID, Revoked: true, RevokedAt: &now, RevokeReason: reason, CreatedAt: now})
	}
	return nil
}

// IsPackageRevoked checks if a package is revoked, returning revocation status and reason.
func (s *SQLStore) IsPackageRevoked(ctx context.Context, packageID string) (bool, string, error) {
	var revokedInt int
	var reason string
	err := s.db.QueryRowContext(ctx, `SELECT revoked, revoke_reason FROM control_packages WHERE package_id = ?`, packageID).Scan(&revokedInt, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("control: check package revocation: %w", err)
	}
	return revokedInt != 0, reason, nil
}

// StoreMasterKey associates a 32-byte master encryption key with a package ID.
func (s *SQLStore) StoreMasterKey(ctx context.Context, packageID string, key []byte) error {
	if packageID == "" {
		return errors.New("control: package id cannot be empty")
	}
	if len(key) != 32 {
		return fmt.Errorf("control: master key must be 32 bytes, got %d", len(key))
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO control_master_keys (package_id, key_hex) VALUES (?, ?)
		ON CONFLICT(package_id) DO UPDATE SET key_hex=excluded.key_hex`,
		packageID, hex.EncodeToString(key))
	if err != nil {
		return fmt.Errorf("control: store master key: %w", err)
	}
	return nil
}

// GetMasterKey retrieves the master encryption key associated with a package ID.
func (s *SQLStore) GetMasterKey(ctx context.Context, packageID string) ([]byte, error) {
	var keyHex string
	err := s.db.QueryRowContext(ctx, `SELECT key_hex FROM control_master_keys WHERE package_id = ?`, packageID).Scan(&keyHex)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrMasterKeyNotFound, packageID)
	}
	if err != nil {
		return nil, fmt.Errorf("control: get master key: %w", err)
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("control: get master key: stored value is not hex: %w", err)
	}
	return key, nil
}

// RecordGrant persists an issued authorization grant record.
func (s *SQLStore) RecordGrant(ctx context.Context, grant GrantRecord) error {
	if grant.GrantID == "" && grant.SessionID != "" {
		grant.GrantID = grant.SessionID
	}
	if grant.GrantID == "" {
		return errors.New("control: grant id cannot be empty")
	}
	conditionsJSON, err := json.Marshal(grant.Conditions)
	if err != nil {
		return fmt.Errorf("control: record grant: marshal conditions: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO control_grants (grant_id, session_id, package_id, application_id, device_id, key_hex, issued_at, expires_at, revoked, revoked_at, revoke_reason, conditions_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(grant_id) DO UPDATE SET
			session_id=excluded.session_id, package_id=excluded.package_id, application_id=excluded.application_id,
			device_id=excluded.device_id, key_hex=excluded.key_hex, issued_at=excluded.issued_at, expires_at=excluded.expires_at,
			revoked=excluded.revoked, revoked_at=excluded.revoked_at, revoke_reason=excluded.revoke_reason, conditions_json=excluded.conditions_json`,
		grant.GrantID, grant.SessionID, grant.PackageID, grant.ApplicationID, grant.DeviceID,
		hex.EncodeToString(grant.Key), formatTime(grant.IssuedAt), formatTime(grant.ExpiresAt),
		boolToInt(grant.Revoked), formatTimePtr(grant.RevokedAt), grant.RevokeReason, string(conditionsJSON),
	)
	if err != nil {
		return fmt.Errorf("control: record grant: %w", err)
	}
	return nil
}

// RevokeGrant revokes an active grant before its scheduled expiration.
func (s *SQLStore) RevokeGrant(ctx context.Context, grantID string) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE control_grants SET revoked=1, revoked_at=? WHERE grant_id=?`, formatTime(now), grantID)
	if err != nil {
		return fmt.Errorf("control: revoke grant: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %q", ErrGrantNotFound, grantID)
	}
	return nil
}

// IsGrantRevoked checks if a grant has been revoked.
func (s *SQLStore) IsGrantRevoked(ctx context.Context, grantID string) (bool, error) {
	var revokedInt int
	err := s.db.QueryRowContext(ctx, `SELECT revoked FROM control_grants WHERE grant_id = ?`, grantID).Scan(&revokedInt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: %q", ErrGrantNotFound, grantID)
	}
	if err != nil {
		return false, fmt.Errorf("control: check grant revocation: %w", err)
	}
	return revokedInt != 0, nil
}

// RecordDevice registers or updates a client device record.
func (s *SQLStore) RecordDevice(ctx context.Context, device DeviceRecord) error {
	if device.DeviceID == "" {
		return errors.New("control: device id cannot be empty")
	}
	now := time.Now().UTC()
	if device.RegisteredAt.IsZero() {
		device.RegisteredAt = now
	}
	if device.LastSeenAt.IsZero() {
		device.LastSeenAt = now
	}
	metaJSON, err := json.Marshal(device.Metadata)
	if err != nil {
		return fmt.Errorf("control: record device: marshal metadata: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO control_devices (device_id, platform, registered_at, last_seen_at, revoked, revoked_at, revoke_reason, attestation_evidence, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			platform=excluded.platform, last_seen_at=excluded.last_seen_at, revoked=excluded.revoked,
			revoked_at=excluded.revoked_at, revoke_reason=excluded.revoke_reason,
			attestation_evidence=excluded.attestation_evidence, metadata_json=excluded.metadata_json`,
		device.DeviceID, device.Platform, formatTime(device.RegisteredAt), formatTime(device.LastSeenAt),
		boolToInt(device.Revoked), formatTimePtr(device.RevokedAt), device.RevokeReason,
		string(device.AttestationEvidence), string(metaJSON),
	)
	if err != nil {
		return fmt.Errorf("control: record device: %w", err)
	}
	return nil
}

// IsDeviceRevoked checks if a client device has been quarantined or blacklisted.
func (s *SQLStore) IsDeviceRevoked(ctx context.Context, deviceID string) (bool, error) {
	var revokedInt int
	err := s.db.QueryRowContext(ctx, `SELECT revoked FROM control_devices WHERE device_id = ?`, deviceID).Scan(&revokedInt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // mirrors MemoryStore: an unknown device is not revoked
	}
	if err != nil {
		return false, fmt.Errorf("control: check device revocation: %w", err)
	}
	return revokedInt != 0, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var _ Store = (*SQLStore)(nil)
