package sms

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Store is the application's durable state: configuration entities, the
// ledger and the messages.
//
// The ledger and the message lifecycle share one database on purpose. Every
// transition that moves money (accept holds funds, a provider's acceptance
// captures them, a final failure releases them) happens in the same
// transaction as the message state change it belongs to, and each is a
// compare-and-set on a state column. Two workers racing, a redelivered job or a
// duplicated receipt therefore find the row already moved and do nothing: money
// moves at most once per message, whatever the queue does.
//
// SQL is portable between SQLite and PostgreSQL (? placeholders are rewritten
// for PostgreSQL; UPSERT and RETURNING are used by both).
type Store struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
}

// NewStore wraps db. dialect is "sqlite" or "postgres".
func NewStore(db *sql.DB, dialect string) (*Store, error) {
	switch dialect {
	case "sqlite", "postgres":
	default:
		return nil, fmt.Errorf("sms: unsupported database dialect %q (use sqlite or postgres)", dialect)
	}
	return &Store{db: db, postgres: dialect == "postgres", now: time.Now}, nil
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// q rewrites ? placeholders for PostgreSQL.
func (s *Store) q(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) exec(ctx context.Context, e execer, query string, args ...any) (int64, error) {
	res, err := e.ExecContext(ctx, s.q(query), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Migrate creates the schema. It is idempotent.
func (s *Store) Migrate(ctx context.Context) error {
	big := "INTEGER"
	if s.postgres {
		big = "BIGINT"
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sms_users (
			id TEXT PRIMARY KEY, tenant TEXT NOT NULL, status TEXT NOT NULL,
			api_key_hash TEXT NOT NULL DEFAULT '', body TEXT NOT NULL, updated_ms ` + big + ` NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS sms_users_key ON sms_users(api_key_hash)`,
		`CREATE TABLE IF NOT EXISTS sms_providers (
			name TEXT PRIMARY KEY, body TEXT NOT NULL, updated_ms ` + big + ` NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sms_assignments (
			id TEXT PRIMARY KEY, body TEXT NOT NULL, updated_ms ` + big + ` NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sms_rates (
			id TEXT PRIMARY KEY, body TEXT NOT NULL, updated_ms ` + big + ` NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sms_balances (
			user_id TEXT PRIMARY KEY, balance ` + big + ` NOT NULL DEFAULT 0, held ` + big + ` NOT NULL DEFAULT 0,
			currency TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sms_holds (
			message_id TEXT PRIMARY KEY, user_id TEXT NOT NULL, amount ` + big + ` NOT NULL,
			state TEXT NOT NULL, updated_ms ` + big + ` NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sms_ledger (
			user_id TEXT NOT NULL, message_id TEXT NOT NULL, kind TEXT NOT NULL,
			amount ` + big + ` NOT NULL, at_ms ` + big + ` NOT NULL,
			PRIMARY KEY (message_id, kind))`,
		`CREATE INDEX IF NOT EXISTS sms_ledger_user ON sms_ledger(user_id, at_ms)`,
		`CREATE TABLE IF NOT EXISTS sms_topups (
			ref TEXT PRIMARY KEY, user_id TEXT NOT NULL, amount ` + big + ` NOT NULL, at_ms ` + big + ` NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sms_messages (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, tenant TEXT NOT NULL,
			idem_key TEXT, payload_hash TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL, provider TEXT NOT NULL DEFAULT '', provider_msg_id TEXT NOT NULL DEFAULT '',
			plan_index INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL DEFAULT 0,
			total_attempts INTEGER NOT NULL DEFAULT 0, dispatch_seq ` + big + ` NOT NULL DEFAULT 1,
			next_run_ms ` + big + ` NOT NULL DEFAULT 0, claim_until_ms ` + big + ` NOT NULL DEFAULT 0,
			to_number TEXT NOT NULL, country TEXT NOT NULL, sender TEXT NOT NULL, segments INTEGER NOT NULL,
			price ` + big + ` NOT NULL DEFAULT 0, cost ` + big + ` NOT NULL DEFAULT 0, currency TEXT NOT NULL,
			err_code TEXT NOT NULL DEFAULT '', err_text TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL,
			created_ms ` + big + ` NOT NULL, updated_ms ` + big + ` NOT NULL,
			submitted_ms ` + big + ` NOT NULL DEFAULT 0, delivered_ms ` + big + ` NOT NULL DEFAULT 0,
			expires_ms ` + big + ` NOT NULL DEFAULT 0)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS sms_messages_idem ON sms_messages(user_id, idem_key)`,
		`CREATE INDEX IF NOT EXISTS sms_messages_provider ON sms_messages(provider, provider_msg_id)`,
		`CREATE INDEX IF NOT EXISTS sms_messages_state ON sms_messages(state, next_run_ms)`,
		`CREATE INDEX IF NOT EXISTS sms_messages_user ON sms_messages(user_id, created_ms)`,
		`CREATE TABLE IF NOT EXISTS sms_optouts (
			scope TEXT NOT NULL, number TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
			at_ms ` + big + ` NOT NULL, PRIMARY KEY (scope, number))`,
		`CREATE TABLE IF NOT EXISTS sms_attempts (
			message_id TEXT NOT NULL, n INTEGER NOT NULL, provider TEXT NOT NULL, outcome TEXT NOT NULL,
			code TEXT NOT NULL DEFAULT '', err_text TEXT NOT NULL DEFAULT '',
			latency_ms ` + big + ` NOT NULL DEFAULT 0, at_ms ` + big + ` NOT NULL,
			PRIMARY KEY (message_id, n))`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sms: migrate: %w", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Configuration entities
// ---------------------------------------------------------------------------

type entityTable struct {
	table string
	key   string
}

var (
	tblProviders   = entityTable{"sms_providers", "name"}
	tblAssignments = entityTable{"sms_assignments", "id"}
	tblRates       = entityTable{"sms_rates", "id"}
)

func (s *Store) putEntity(ctx context.Context, t entityTable, key string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, s.db, fmt.Sprintf(
		`INSERT INTO %s (%s, body, updated_ms) VALUES (?, ?, ?)
		 ON CONFLICT(%s) DO UPDATE SET body = excluded.body, updated_ms = excluded.updated_ms`,
		t.table, t.key, t.key), key, string(body), ms(s.now()))
	return err
}

func (s *Store) deleteEntity(ctx context.Context, t entityTable, key string) error {
	_, err := s.exec(ctx, s.db, fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, t.table, t.key), key)
	return err
}

func (s *Store) listEntities(ctx context.Context, t entityTable, each func(body []byte) error) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT body FROM %s ORDER BY %s`, t.table, t.key))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return err
		}
		if err := each([]byte(body)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// PutUser creates or replaces a user.
func (s *Store) PutUser(ctx context.Context, u User) error {
	body, err := json.Marshal(u)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, s.db,
		`INSERT INTO sms_users (id, tenant, status, api_key_hash, body, updated_ms) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET tenant = excluded.tenant, status = excluded.status,
		   api_key_hash = excluded.api_key_hash, body = excluded.body, updated_ms = excluded.updated_ms`,
		u.ID, u.Tenant, u.Status, u.APIKeyHash, string(body), ms(s.now()))
	return err
}

// DeleteUser removes a user. Messages and ledger entries stay for audit.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM sms_users WHERE id = ?`, id)
	return err
}

// ListUsers returns every user.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM sms_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var u User
		if err := json.Unmarshal([]byte(body), &u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ProviderRecord is a runtime-created provider.
type ProviderRecord struct {
	Name    string         `json:"name"`
	Kind    string         `json:"kind"`
	Owner   string         `json:"owner,omitempty"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// PutProvider stores a runtime-created provider.
func (s *Store) PutProvider(ctx context.Context, p ProviderRecord) error {
	return s.putEntity(ctx, tblProviders, p.Name, p)
}

// DeleteProvider removes a runtime-created provider.
func (s *Store) DeleteProvider(ctx context.Context, name string) error {
	return s.deleteEntity(ctx, tblProviders, name)
}

// ListProviders returns every runtime-created provider.
func (s *Store) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	var out []ProviderRecord
	err := s.listEntities(ctx, tblProviders, func(b []byte) error {
		var p ProviderRecord
		if err := json.Unmarshal(b, &p); err != nil {
			return err
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

// PutAssignment stores an assignment.
func (s *Store) PutAssignment(ctx context.Context, a Assignment) error {
	return s.putEntity(ctx, tblAssignments, a.ID, a)
}

// DeleteAssignment removes an assignment.
func (s *Store) DeleteAssignment(ctx context.Context, id string) error {
	return s.deleteEntity(ctx, tblAssignments, id)
}

// ListAssignments returns every assignment.
func (s *Store) ListAssignments(ctx context.Context) ([]Assignment, error) {
	var out []Assignment
	err := s.listEntities(ctx, tblAssignments, func(b []byte) error {
		var a Assignment
		if err := json.Unmarshal(b, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

// PutRate stores a rate.
func (s *Store) PutRate(ctx context.Context, r Rate) error {
	return s.putEntity(ctx, tblRates, r.ID, r)
}

// DeleteRate removes a rate.
func (s *Store) DeleteRate(ctx context.Context, id string) error {
	return s.deleteEntity(ctx, tblRates, id)
}

// ListRates returns every rate.
func (s *Store) ListRates(ctx context.Context) ([]Rate, error) {
	var out []Rate
	err := s.listEntities(ctx, tblRates, func(b []byte) error {
		var r Rate
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Ledger
// ---------------------------------------------------------------------------

// Balance is a user's funds: Balance is what they own, Held is committed to
// messages in flight, and Available is what they can still spend.
type Balance struct {
	UserID    string `json:"user_id"`
	Currency  string `json:"currency"`
	Balance   int64  `json:"balance_micros"`
	Held      int64  `json:"held_micros"`
	Available int64  `json:"available_micros"`
}

// EnsureAccount creates the user's balance row.
func (s *Store) EnsureAccount(ctx context.Context, user, currency string) error {
	_, err := s.exec(ctx, s.db,
		`INSERT INTO sms_balances (user_id, balance, held, currency) VALUES (?, 0, 0, ?) ON CONFLICT(user_id) DO NOTHING`,
		user, currency)
	return err
}

// Balance reads a user's funds.
func (s *Store) Balance(ctx context.Context, user string) (Balance, error) {
	b := Balance{UserID: user}
	err := s.db.QueryRowContext(ctx, s.q(`SELECT balance, held, currency FROM sms_balances WHERE user_id = ?`), user).
		Scan(&b.Balance, &b.Held, &b.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	b.Available = b.Balance - b.Held
	return b, err
}

// TopUp credits a user. ref makes it idempotent: replaying a top-up with the
// same reference (a payment webhook delivered twice) credits once. It reports
// whether this call applied the credit.
func (s *Store) TopUp(ctx context.Context, user, currency string, amount int64, ref string) (bool, error) {
	if amount <= 0 {
		return false, errors.New("top-up amount must be positive")
	}
	if ref == "" {
		return false, errors.New("top-up needs a reference so it can be replayed safely")
	}
	applied := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		n, err := s.exec(ctx, tx,
			`INSERT INTO sms_topups (ref, user_id, amount, at_ms) VALUES (?, ?, ?, ?) ON CONFLICT(ref) DO NOTHING`,
			ref, user, amount, ms(s.now()))
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		applied = true
		if _, err := s.exec(ctx, tx,
			`INSERT INTO sms_balances (user_id, balance, held, currency) VALUES (?, 0, 0, ?) ON CONFLICT(user_id) DO NOTHING`,
			user, currency); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, `UPDATE sms_balances SET balance = balance + ? WHERE user_id = ?`, amount, user); err != nil {
			return err
		}
		_, err = s.exec(ctx, tx,
			`INSERT INTO sms_ledger (user_id, message_id, kind, amount, at_ms) VALUES (?, ?, 'topup', ?, ?) ON CONFLICT DO NOTHING`,
			user, "topup:"+ref, amount, ms(s.now()))
		return err
	})
	return applied, err
}

// LedgerEntry is one journal row.
type LedgerEntry struct {
	UserID    string    `json:"user_id"`
	MessageID string    `json:"message_id"`
	Kind      string    `json:"kind"`
	Amount    int64     `json:"amount_micros"`
	At        time.Time `json:"at"`
}

// Ledger returns a user's journal, newest first.
func (s *Store) Ledger(ctx context.Context, user string, limit int) ([]LedgerEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.q(
		`SELECT user_id, message_id, kind, amount, at_ms FROM sms_ledger WHERE user_id = ? ORDER BY at_ms DESC LIMIT ?`), user, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		var at int64
		if err := rows.Scan(&e.UserID, &e.MessageID, &e.Kind, &e.Amount, &at); err != nil {
			return nil, err
		}
		e.At = fromMs(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) journal(ctx context.Context, tx execer, user, msgID, kind string, amount int64) error {
	_, err := s.exec(ctx, tx,
		`INSERT INTO sms_ledger (user_id, message_id, kind, amount, at_ms) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		user, msgID, kind, amount, ms(s.now()))
	return err
}

// captureHold turns a message's hold into a charge. It is a compare-and-set on
// the hold's state, so it applies at most once however often it is called.
func (s *Store) captureHold(ctx context.Context, tx execer, msgID string) (bool, error) {
	var user string
	var amount int64
	err := tx.QueryRowContext(ctx, s.q(
		`UPDATE sms_holds SET state = 'captured', updated_ms = ? WHERE message_id = ? AND state = 'hold' RETURNING user_id, amount`),
		ms(s.now()), msgID).Scan(&user, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.exec(ctx, tx, `UPDATE sms_balances SET balance = balance - ?, held = held - ? WHERE user_id = ?`, amount, amount, user); err != nil {
		return false, err
	}
	return true, s.journal(ctx, tx, user, msgID, "capture", amount)
}

// releaseHold returns a hold's funds to the user's available balance.
func (s *Store) releaseHold(ctx context.Context, tx execer, msgID string) (bool, error) {
	var user string
	var amount int64
	err := tx.QueryRowContext(ctx, s.q(
		`UPDATE sms_holds SET state = 'released', updated_ms = ? WHERE message_id = ? AND state = 'hold' RETURNING user_id, amount`),
		ms(s.now()), msgID).Scan(&user, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.exec(ctx, tx, `UPDATE sms_balances SET held = held - ? WHERE user_id = ?`, amount, user); err != nil {
		return false, err
	}
	return true, s.journal(ctx, tx, user, msgID, "release", amount)
}

// refundCapture gives back a charge that was already captured.
func (s *Store) refundCapture(ctx context.Context, tx execer, msgID string) (bool, error) {
	var user string
	var amount int64
	err := tx.QueryRowContext(ctx, s.q(
		`UPDATE sms_holds SET state = 'refunded', updated_ms = ? WHERE message_id = ? AND state = 'captured' RETURNING user_id, amount`),
		ms(s.now()), msgID).Scan(&user, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.exec(ctx, tx, `UPDATE sms_balances SET balance = balance + ? WHERE user_id = ?`, amount, user); err != nil {
		return false, err
	}
	return true, s.journal(ctx, tx, user, msgID, "refund", amount)
}

// HoldState reports the state of a message's hold: hold, captured, released,
// refunded, or "" when there is none.
func (s *Store) HoldState(ctx context.Context, msgID string) (string, error) {
	var st string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT state FROM sms_holds WHERE message_id = ?`), msgID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return st, err
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

const msgColumns = `id, user_id, tenant, COALESCE(idem_key, ''), payload_hash, state, provider, provider_msg_id,
	plan_index, attempt, total_attempts, dispatch_seq, next_run_ms, claim_until_ms, to_number, country, sender,
	segments, price, cost, currency, err_code, err_text, body, created_ms, updated_ms, submitted_ms, delivered_ms, expires_ms`

type messageBody struct {
	Text     string            `json:"text,omitempty"`
	Encoding string            `json:"encoding"`
	Type     string            `json:"type"`
	DLR      bool              `json:"dlr"`
	Plan     []PlanEntry       `json:"plan"`
	Meta     map[string]string `json:"meta,omitempty"`
}

type scanner interface{ Scan(...any) error }

func scanMessage(r scanner) (*Message, error) {
	var (
		m                                                  Message
		state, body                                        string
		next, claim, created, updated, submitted, delivery int64
		expires                                            int64
	)
	if err := r.Scan(&m.ID, &m.UserID, &m.Tenant, &m.IdempotencyKey, &m.PayloadHash, &state, &m.Provider, &m.ProviderMsgID,
		&m.PlanIndex, &m.Attempt, &m.TotalAttempts, &m.DispatchSeq, &next, &claim, &m.To, &m.Country, &m.From,
		&m.Segments, &m.PriceMicros, &m.CostMicros, &m.Currency, &m.ErrorCode, &m.Error, &body,
		&created, &updated, &submitted, &delivery, &expires); err != nil {
		return nil, err
	}
	m.State = MessageState(state)
	var b messageBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return nil, fmt.Errorf("sms: message %s has a corrupt body: %w", m.ID, err)
	}
	m.Text, m.Encoding, m.Type, m.WantDLR, m.Plan, m.Meta = b.Text, b.Encoding, b.Type, b.DLR, b.Plan, b.Meta
	m.NextRunAt, m.ClaimUntil = fromMs(next), fromMs(claim)
	m.CreatedAt, m.UpdatedAt, m.SubmittedAt, m.DeliveredAt = fromMs(created), fromMs(updated), fromMs(submitted), fromMs(delivery)
	m.ExpiresAt = fromMs(expires)
	return &m, nil
}

func encodeBody(m *Message) (string, error) {
	b, err := json.Marshal(messageBody{Text: m.Text, Encoding: m.Encoding, Type: m.Type, DLR: m.WantDLR, Plan: m.Plan, Meta: m.Meta})
	return string(b), err
}

// AcceptResult is the outcome of Accept.
type AcceptResult struct {
	Message *Message
	// Duplicate is true when the idempotency key had already been accepted: the
	// stored message is returned and nothing was charged again.
	Duplicate bool
}

// Accept stores a message and holds its price in one transaction. It is the
// pipeline's payment step: either both happen or neither does, and replaying
// the same idempotency key returns the original message without a second hold.
func (s *Store) Accept(ctx context.Context, m *Message) (AcceptResult, error) {
	now := s.now()
	m.State, m.DispatchSeq = StateQueued, 1
	m.CreatedAt, m.UpdatedAt = now, now
	if m.NextRunAt.IsZero() {
		m.NextRunAt = now
	}
	if len(m.Plan) > 0 {
		m.Provider = m.Plan[0].Provider
	}
	body, err := encodeBody(m)
	if err != nil {
		return AcceptResult{}, err
	}
	var idem any
	if m.IdempotencyKey != "" {
		idem = m.IdempotencyKey
	}

	var res AcceptResult
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if m.IdempotencyKey != "" {
			existing, err := scanMessage(tx.QueryRowContext(ctx, s.q(
				`SELECT `+msgColumns+` FROM sms_messages WHERE user_id = ? AND idem_key = ?`), m.UserID, m.IdempotencyKey))
			if err == nil {
				if existing.PayloadHash != m.PayloadHash {
					return ErrIdempotency
				}
				res = AcceptResult{Message: existing, Duplicate: true}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if _, err := s.exec(ctx, tx,
			`INSERT INTO sms_balances (user_id, balance, held, currency) VALUES (?, 0, 0, ?) ON CONFLICT(user_id) DO NOTHING`,
			m.UserID, m.Currency); err != nil {
			return err
		}
		// One statement both checks and reserves the funds, so two concurrent
		// accepts can never both spend the same balance.
		n, err := s.exec(ctx, tx,
			`UPDATE sms_balances SET held = held + ? WHERE user_id = ? AND balance - held >= ?`,
			m.PriceMicros, m.UserID, m.PriceMicros)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInsufficientFunds
		}
		if _, err := s.exec(ctx, tx,
			`INSERT INTO sms_messages (id, user_id, tenant, idem_key, payload_hash, state, provider, provider_msg_id,
				plan_index, attempt, total_attempts, dispatch_seq, next_run_ms, claim_until_ms, to_number, country, sender,
				segments, price, cost, currency, err_code, err_text, body, created_ms, updated_ms, submitted_ms, delivered_ms, expires_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '', 0, 0, 0, 1, ?, 0, ?, ?, ?, ?, ?, 0, ?, '', '', ?, ?, ?, 0, 0, ?)`,
			m.ID, m.UserID, m.Tenant, idem, m.PayloadHash, string(StateQueued), m.Provider,
			ms(m.NextRunAt), m.To, m.Country, m.From, m.Segments, m.PriceMicros, m.Currency, body,
			ms(now), ms(now), ms(m.ExpiresAt)); err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx,
			`INSERT INTO sms_holds (message_id, user_id, amount, state, updated_ms) VALUES (?, ?, ?, 'hold', ?)`,
			m.ID, m.UserID, m.PriceMicros, ms(now)); err != nil {
			return err
		}
		if err := s.journal(ctx, tx, m.UserID, m.ID, "hold", m.PriceMicros); err != nil {
			return err
		}
		res = AcceptResult{Message: m}
		return nil
	})
	if err != nil && m.IdempotencyKey != "" && isUniqueViolation(err) {
		// Two requests with one key raced; the other won. Return its message.
		existing, ferr := s.FindByIdempotency(ctx, m.UserID, m.IdempotencyKey)
		if ferr == nil {
			if existing.PayloadHash != m.PayloadHash {
				return AcceptResult{}, ErrIdempotency
			}
			return AcceptResult{Message: existing, Duplicate: true}, nil
		}
	}
	return res, err
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique") || strings.Contains(s, "duplicate key")
}

// Get reads a message.
func (s *Store) Get(ctx context.Context, id string) (*Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, s.q(`SELECT `+msgColumns+` FROM sms_messages WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// FindByIdempotency reads a user's message by idempotency key.
func (s *Store) FindByIdempotency(ctx context.Context, user, key string) (*Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, s.q(
		`SELECT `+msgColumns+` FROM sms_messages WHERE user_id = ? AND idem_key = ?`), user, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// FindByProviderID reads the message a provider reported on.
func (s *Store) FindByProviderID(ctx context.Context, provider, providerMsgID string) (*Message, error) {
	m, err := scanMessage(s.db.QueryRowContext(ctx, s.q(
		`SELECT `+msgColumns+` FROM sms_messages WHERE provider = ? AND provider_msg_id = ?`), provider, providerMsgID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// ErrBusy means another worker holds a live claim on the message.
var ErrBusy = errors.New("message is being dispatched by another worker")

// Claim takes the message for dispatching. It succeeds only for the live job
// (seq matches) of a message that is queued or whose previous claim lapsed.
// ErrStale means the job is obsolete or the message is already handled;
// ErrBusy means a live claim exists and the job should be retried later.
func (s *Store) Claim(ctx context.Context, id string, seq int64, lease time.Duration) (*Message, error) {
	now := s.now()
	n, err := s.exec(ctx, s.db,
		`UPDATE sms_messages SET state = 'dispatching', claim_until_ms = ?, attempt = attempt + 1,
		   total_attempts = total_attempts + 1, updated_ms = ?
		 WHERE id = ? AND dispatch_seq = ? AND (state = 'queued' OR (state = 'dispatching' AND claim_until_ms < ?))`,
		ms(now.Add(lease)), ms(now), id, seq, ms(now))
	if err != nil {
		return nil, err
	}
	m, gerr := s.Get(ctx, id)
	if gerr != nil {
		return nil, gerr
	}
	if n == 1 {
		return m, nil
	}
	if m.DispatchSeq != seq || m.State.Settled() {
		return m, ErrStale
	}
	return m, ErrBusy
}

// Submitted records a provider's acceptance. With capture, the hold becomes a
// charge in the same transaction; with redact the message text is erased in it.
func (s *Store) Submitted(ctx context.Context, m *Message, seq int64, providerMsgID string, capture, redact bool) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		cost := int64(0)
		if m.PlanIndex < len(m.Plan) {
			cost = m.Plan[m.PlanIndex].CostMicros
		}
		n, err := s.exec(ctx, tx,
			`UPDATE sms_messages SET state = 'submitted', provider_msg_id = ?, submitted_ms = ?, claim_until_ms = 0,
			   cost = ?, err_code = '', err_text = '', updated_ms = ?
			 WHERE id = ? AND dispatch_seq = ? AND state = 'dispatching'`,
			providerMsgID, ms(now), cost, ms(now), m.ID, seq)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrStale
		}
		if capture {
			if _, err := s.captureHold(ctx, tx, m.ID); err != nil {
				return err
			}
		}
		// Nothing needs the text once a provider has it: a retry or failover
		// happens only before acceptance.
		if redact {
			return s.redact(ctx, tx, m.ID)
		}
		return nil
	})
}

// Reschedule puts the message back in the queue for another try, on the same
// provider (planIndex unchanged) or the next one. It returns the new job
// sequence, which the caller publishes.
func (s *Store) Reschedule(ctx context.Context, id string, seq int64, planIndex int, provider string, attempt int, runAt time.Time, code, errText string) (int64, error) {
	now := s.now()
	n, err := s.exec(ctx, s.db,
		`UPDATE sms_messages SET state = 'queued', dispatch_seq = dispatch_seq + 1, plan_index = ?, provider = ?,
		   attempt = ?, next_run_ms = ?, claim_until_ms = 0, err_code = ?, err_text = ?, updated_ms = ?
		 WHERE id = ? AND dispatch_seq = ? AND state = 'dispatching'`,
		planIndex, provider, attempt, ms(runAt), code, errText, ms(now), id, seq)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrStale
	}
	return seq + 1, nil
}

// Fail ends the message and releases its hold. It works from dispatching (a
// permanent error, or every provider exhausted) and from queued (expiry).
func (s *Store) Fail(ctx context.Context, id string, seq int64, code, errText string, redact bool) error {
	now := s.now()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		n, err := s.exec(ctx, tx,
			`UPDATE sms_messages SET state = 'failed', claim_until_ms = 0, err_code = ?, err_text = ?, updated_ms = ?
			 WHERE id = ? AND dispatch_seq = ? AND state IN ('dispatching', 'queued')`,
			code, errText, ms(now), id, seq)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrStale
		}
		if _, err := s.releaseHold(ctx, tx, id); err != nil {
			return err
		}
		if redact {
			return s.redact(ctx, tx, id)
		}
		return nil
	})
}

// redact removes the message text from a finished message: an OTP does not
// need to outlive its delivery.
func (s *Store) redact(ctx context.Context, tx execer, id string) error {
	var body string
	if err := tx.QueryRowContext(ctx, s.q(`SELECT body FROM sms_messages WHERE id = ?`), id).Scan(&body); err != nil {
		return err
	}
	var b messageBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		return err
	}
	b.Text = ""
	out, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, tx, `UPDATE sms_messages SET body = ? WHERE id = ?`, string(out), id)
	return err
}

// ErrNotReady means a receipt arrived for a message that has not reached the
// state the receipt applies to; the caller retries later.
var ErrNotReady = errors.New("message is not ready for this receipt")

// DLROutcome says what ApplyDLR did.
type DLROutcome struct {
	Message *Message
	// Changed is false when the receipt was a duplicate or arrived after a
	// final state.
	Changed  bool
	Captured bool
	Refunded bool
}

// ApplyDLR applies a final delivery receipt to a submitted message.
//
// With captureOnDelivery the charge is taken now rather than at submission; a
// failed receipt then releases the hold. Otherwise a failed receipt refunds the
// capture when refund is set.
func (s *Store) ApplyDLR(ctx context.Context, id string, delivered bool, code string, captureOnDelivery, refund, redact bool) (DLROutcome, error) {
	now := s.now()
	var out DLROutcome
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		target := "failed"
		if delivered {
			target = "delivered"
		}
		// A receipt's status code is an error only when the message failed.
		errCode := ""
		if !delivered {
			errCode = code
		}
		n, err := s.exec(ctx, tx,
			`UPDATE sms_messages SET state = ?, delivered_ms = ?, err_code = ?, updated_ms = ?
			 WHERE id = ? AND state = 'submitted'`,
			target, ms(now), errCode, ms(now), id)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil // handled below, outside the write path
		}
		out.Changed = true
		if delivered {
			if captureOnDelivery {
				out.Captured, err = s.captureHold(ctx, tx, id)
			}
		} else {
			if captureOnDelivery {
				_, err = s.releaseHold(ctx, tx, id)
			} else if refund {
				out.Refunded, err = s.refundCapture(ctx, tx, id)
			}
		}
		if err != nil {
			return err
		}
		if redact {
			return s.redact(ctx, tx, id)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	m, gerr := s.Get(ctx, id)
	if gerr != nil {
		return out, gerr
	}
	out.Message = m
	if !out.Changed {
		want := StateFailed
		if delivered {
			want = StateDelivered
		}
		switch {
		case m.State == want:
			// duplicate receipt: fine
		case m.State.Final():
			// a late contradictory receipt is ignored
		default:
			return out, ErrNotReady
		}
	}
	return out, nil
}

// RecordAttempt appends to the attempt log.
func (s *Store) RecordAttempt(ctx context.Context, a AttemptRecord) error {
	_, err := s.exec(ctx, s.db,
		`INSERT INTO sms_attempts (message_id, n, provider, outcome, code, err_text, latency_ms, at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(message_id, n) DO UPDATE SET provider = excluded.provider, outcome = excluded.outcome,
		   code = excluded.code, err_text = excluded.err_text, latency_ms = excluded.latency_ms, at_ms = excluded.at_ms`,
		a.MessageID, a.N, a.Provider, a.Outcome, a.Code, truncate(a.Error, 500), a.LatencyMs, ms(a.At))
	return err
}

// Attempts returns a message's attempt log.
func (s *Store) Attempts(ctx context.Context, id string) ([]AttemptRecord, error) {
	rows, err := s.db.QueryContext(ctx, s.q(
		`SELECT message_id, n, provider, outcome, code, err_text, latency_ms, at_ms FROM sms_attempts WHERE message_id = ? ORDER BY n`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AttemptRecord
	for rows.Next() {
		var a AttemptRecord
		var at int64
		if err := rows.Scan(&a.MessageID, &a.N, &a.Provider, &a.Outcome, &a.Code, &a.Error, &a.LatencyMs, &at); err != nil {
			return nil, err
		}
		a.At = fromMs(at)
		out = append(out, a)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Stalled is a message whose job may have been lost: it is queued past its run
// time, or its worker's claim lapsed.
type Stalled struct {
	ID       string
	Provider string
	Seq      int64
	State    MessageState
}

// ListStalled finds messages that need their job (re)published. grace keeps the
// sweeper from racing a job that is merely about to run.
func (s *Store) ListStalled(ctx context.Context, grace time.Duration, limit int) ([]Stalled, error) {
	cutoff := ms(s.now().Add(-grace))
	rows, err := s.db.QueryContext(ctx, s.q(
		`SELECT id, provider, dispatch_seq, state FROM sms_messages
		 WHERE (state = 'queued' AND next_run_ms < ?) OR (state = 'dispatching' AND claim_until_ms < ? AND next_run_ms < ?)
		 ORDER BY next_run_ms LIMIT ?`), cutoff, cutoff, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stalled
	for rows.Next() {
		var st Stalled
		var state string
		if err := rows.Scan(&st.ID, &st.Provider, &st.Seq, &state); err != nil {
			return nil, err
		}
		st.State = MessageState(state)
		out = append(out, st)
	}
	return out, rows.Err()
}

// TouchRun pushes a stalled message's next run forward after the sweeper
// republished it, so the next sweep leaves it alone for a while.
func (s *Store) TouchRun(ctx context.Context, id string, seq int64, next time.Time) error {
	_, err := s.exec(ctx, s.db,
		`UPDATE sms_messages SET next_run_ms = ? WHERE id = ? AND dispatch_seq = ? AND state IN ('queued', 'dispatching')`,
		ms(next), id, seq)
	return err
}

// CountToday counts a user's messages accepted since midnight UTC.
func (s *Store) CountToday(ctx context.Context, user string) (int, error) {
	now := s.now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var n int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM sms_messages WHERE user_id = ? AND created_ms >= ?`),
		user, ms(midnight)).Scan(&n)
	return n, err
}

// ListMessages returns a user's most recent messages.
func (s *Store) ListMessages(ctx context.Context, user string, state MessageState, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	query := `SELECT ` + msgColumns + ` FROM sms_messages WHERE user_id = ?`
	args := []any{user}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, string(state))
	}
	query += ` ORDER BY created_ms DESC, id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// StateCounts counts messages by state and provider.
type StateCount struct {
	Provider string       `json:"provider"`
	State    MessageState `json:"state"`
	Count    int          `json:"count"`
}

// Counts reports message totals for operators.
func (s *Store) Counts(ctx context.Context) ([]StateCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, state, COUNT(*) FROM sms_messages GROUP BY provider, state ORDER BY provider, state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StateCount
	for rows.Next() {
		var c StateCount
		var st string
		if err := rows.Scan(&c.Provider, &st, &c.Count); err != nil {
			return nil, err
		}
		c.State = MessageState(st)
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetUser reads one user.
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	var body string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT body FROM sms_users WHERE id = ?`), id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	var u User
	return u, json.Unmarshal([]byte(body), &u)
}

func (s *Store) getEntity(ctx context.Context, t entityTable, key string, out any) error {
	var body string
	err := s.db.QueryRowContext(ctx, s.q(fmt.Sprintf(`SELECT body FROM %s WHERE %s = ?`, t.table, t.key)), key).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(body), out)
}

// GetProvider reads one runtime-created provider.
func (s *Store) GetProvider(ctx context.Context, name string) (p ProviderRecord, err error) {
	err = s.getEntity(ctx, tblProviders, name, &p)
	return
}

// GetAssignment reads one assignment.
func (s *Store) GetAssignment(ctx context.Context, id string) (a Assignment, err error) {
	err = s.getEntity(ctx, tblAssignments, id, &a)
	return
}

// GetRate reads one rate.
func (s *Store) GetRate(ctx context.Context, id string) (r Rate, err error) {
	err = s.getEntity(ctx, tblRates, id, &r)
	return
}

// ---------------------------------------------------------------------------
// Opt-outs
// ---------------------------------------------------------------------------

// GlobalScope is the opt-out scope that applies to every user.
const GlobalScope = "*"

// AddOptOut stops messages to number, for one user or (GlobalScope) everyone.
func (s *Store) AddOptOut(ctx context.Context, scope, number, reason string) error {
	_, err := s.exec(ctx, s.db,
		`INSERT INTO sms_optouts (scope, number, reason, at_ms) VALUES (?, ?, ?, ?)
		 ON CONFLICT(scope, number) DO UPDATE SET reason = excluded.reason`,
		scope, number, truncate(reason, 200), ms(s.now()))
	return err
}

// RemoveOptOut lifts an opt-out.
func (s *Store) RemoveOptOut(ctx context.Context, scope, number string) error {
	_, err := s.exec(ctx, s.db, `DELETE FROM sms_optouts WHERE scope = ? AND number = ?`, scope, number)
	return err
}

// OptedOut reports whether user may not message number, and why.
func (s *Store) OptedOut(ctx context.Context, user, number string) (bool, string, error) {
	var reason string
	err := s.db.QueryRowContext(ctx, s.q(
		`SELECT reason FROM sms_optouts WHERE number = ? AND scope IN (?, ?) LIMIT 1`), number, GlobalScope, user).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, reason, nil
}
