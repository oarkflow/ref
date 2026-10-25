// Package chat is REF's messaging store: channels, direct messages, history,
// read receipts and typing, kept in a SQL database and announced through a
// realtime hub.
//
// The store is the source of truth and the hub is only the announcement: a
// message is committed first and published second, so a client that missed the
// announcement still finds it in History. Every operation takes the acting
// user and refuses anyone who is not a member of the channel, in the channel's
// tenant.
package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors.
var (
	ErrNotFound  = errors.New("chat: not found")
	ErrForbidden = errors.New("chat: not a member of this channel")
	ErrInvalid   = errors.New("chat: invalid request")
)

// MaxBody is the longest message body in bytes.
const MaxBody = 8 << 10

// Notifier announces events. The realtime hub satisfies it through Announce.
type Notifier interface {
	Notify(ctx context.Context, topic, event string, data json.RawMessage, from string) error
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(ctx context.Context, topic, event string, data json.RawMessage, from string) error

// Notify implements Notifier.
func (f NotifierFunc) Notify(ctx context.Context, topic, event string, data json.RawMessage, from string) error {
	return f(ctx, topic, event, data, from)
}

// Actor is the user performing an operation.
type Actor struct {
	ID     string
	Tenant string
}

// Channel is a conversation: a named group channel or a two-person DM.
type Channel struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // "channel" or "dm"
	Name      string    `json:"name,omitempty"`
	Tenant    string    `json:"tenant,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Summary is a channel as one member sees it.
type Summary struct {
	Channel
	Role    string   `json:"role"`
	Unread  int      `json:"unread"`
	LastSeq int64    `json:"last_seq"`
	Members []string `json:"members,omitempty"`
}

// Message is one chat message.
type Message struct {
	ID        string          `json:"id"`
	ChannelID string          `json:"channel_id"`
	Seq       int64           `json:"seq"`
	Sender    string          `json:"sender"`
	Body      string          `json:"body"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// Store is the chat store.
type Store struct {
	db     *sql.DB
	rebind func(string) string
	notify Notifier
	now    func() time.Time
	ignore string   // dialect's "insert, ignoring duplicates" prefix
	suffix string   // ... and its trailing clause
	locks  sync.Map // channel id -> *sync.Mutex
}

// lock serializes senders to one channel within this process, so the common
// case never collides; the unique (channel, seq) key and the retry below keep
// several processes correct.
func (s *Store) lock(channelID string) func() {
	m, _ := s.locks.LoadOrStore(channelID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Options configure a Store.
type Options struct {
	// Dialect is "postgres", "mysql" or "sqlite".
	Dialect string
	// Rebind converts $n placeholders for the dialect. Nil leaves them as written
	// (right for postgres and sqlite).
	Rebind func(string) string
	// Notifier announces events; nil disables realtime delivery.
	Notifier Notifier
	// Now is the clock, for tests.
	Now func() time.Time
}

// Topic is the realtime topic of a channel.
func Topic(channelID string) string { return "chat:" + channelID }

// ChannelOfTopic extracts the channel id from a chat topic.
func ChannelOfTopic(topic string) (string, bool) {
	return strings.CutPrefix(topic, "chat:")
}

// DMID is the id of the direct conversation between two users, the same
// whichever way round they are given.
func DMID(a, b string) string {
	pair := []string{a, b}
	sort.Strings(pair)
	sum := sha256.Sum256([]byte(pair[0] + "\x00" + pair[1]))
	return "dm_" + hex.EncodeToString(sum[:10])
}

// Open prepares the schema and returns a store. It is safe to run on every
// start.
func Open(ctx context.Context, db *sql.DB, o Options) (*Store, error) {
	s := &Store{db: db, rebind: o.Rebind, notify: o.Notifier, now: o.Now}
	if s.rebind == nil {
		s.rebind = func(q string) string { return q }
	}
	if s.now == nil {
		s.now = func() time.Time { return time.Now().UTC() }
	}
	switch o.Dialect {
	case "mysql":
		s.ignore, s.suffix = "INSERT IGNORE INTO", ""
	case "sqlite":
		s.ignore, s.suffix = "INSERT OR IGNORE INTO", ""
	default:
		s.ignore, s.suffix = "INSERT INTO", " ON CONFLICT DO NOTHING"
	}
	text := "TEXT"
	key := "VARCHAR(191)"
	if o.Dialect == "mysql" {
		text = "LONGTEXT"
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS chat_channels (
			id ` + key + ` PRIMARY KEY, kind ` + key + ` NOT NULL, name ` + key + ` NOT NULL DEFAULT '',
			tenant ` + key + ` NOT NULL DEFAULT '', created_by ` + key + ` NOT NULL, created_at ` + key + ` NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS chat_members (
			channel_id ` + key + ` NOT NULL, user_id ` + key + ` NOT NULL, role ` + key + ` NOT NULL,
			last_read_seq BIGINT NOT NULL DEFAULT 0, joined_at ` + key + ` NOT NULL,
			PRIMARY KEY (channel_id, user_id))`,
		`CREATE INDEX IF NOT EXISTS chat_members_user ON chat_members (user_id)`,
		`CREATE TABLE IF NOT EXISTS chat_messages (
			id ` + key + ` PRIMARY KEY, channel_id ` + key + ` NOT NULL, seq BIGINT NOT NULL,
			sender ` + key + ` NOT NULL, body ` + text + ` NOT NULL, meta ` + text + ` NOT NULL,
			created_at ` + key + ` NOT NULL, UNIQUE (channel_id, seq))`,
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			if o.Dialect == "mysql" && strings.Contains(q, "CREATE INDEX IF NOT EXISTS") {
				// MySQL has no IF NOT EXISTS for indexes; a repeat run reports a
				// duplicate key name, which is the state we want.
				if strings.Contains(err.Error(), "Duplicate key name") {
					continue
				}
			}
			return nil, fmt.Errorf("chat: schema: %w", err)
		}
	}
	return s, nil
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.rebind(q), args...)
}

func (s *Store) announce(ctx context.Context, channel, event string, v any, from string) {
	if s.notify == nil {
		return
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	// The message is already committed; a failed announcement only delays
	// delivery until the client next reads History.
	_ = s.notify.Notify(ctx, Topic(channel), event, raw, from)
}

// ---------------------------------------------------------------------------
// Channels
// ---------------------------------------------------------------------------

// CreateChannel makes a group channel with the creator as owner and the given
// users as members.
func (s *Store) CreateChannel(ctx context.Context, by Actor, name string, members []string) (Channel, error) {
	name = strings.TrimSpace(name)
	if by.ID == "" || name == "" || len(name) > 120 {
		return Channel{}, fmt.Errorf("%w: a channel needs a name of 1 to 120 characters", ErrInvalid)
	}
	ch := Channel{ID: newID("ch_"), Kind: "channel", Name: name, Tenant: by.Tenant, CreatedBy: by.ID, CreatedAt: s.now()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO chat_channels (id, kind, name, tenant, created_by, created_at) VALUES ($1,$2,$3,$4,$5,$6)`),
		ch.ID, ch.Kind, ch.Name, ch.Tenant, ch.CreatedBy, ts(ch.CreatedAt)); err != nil {
		return Channel{}, err
	}
	seen := map[string]bool{}
	for i, u := range append([]string{by.ID}, members...) {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		role := "member"
		if i == 0 {
			role = "owner"
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO chat_members (channel_id, user_id, role, joined_at) VALUES ($1,$2,$3,$4)`),
			ch.ID, u, role, ts(ch.CreatedAt)); err != nil {
			return Channel{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Channel{}, err
	}
	for u := range seen {
		s.announce(ctx, ch.ID, "member.joined", map[string]any{"user": u}, by.ID)
	}
	return ch, nil
}

// OpenDM returns the direct conversation between the actor and other, creating
// it on first use.
func (s *Store) OpenDM(ctx context.Context, by Actor, other string) (Channel, error) {
	if by.ID == "" || other == "" || other == by.ID {
		return Channel{}, fmt.Errorf("%w: a direct message needs two different users", ErrInvalid)
	}
	id := DMID(by.ID, other)
	if ch, err := s.channel(ctx, id); err == nil {
		if _, err := s.member(ctx, by, ch); err != nil {
			return Channel{}, err
		}
		return ch, nil
	}
	now := s.now()
	if _, err := s.exec(ctx, s.ignore+` chat_channels (id, kind, name, tenant, created_by, created_at) VALUES ($1,$2,$3,$4,$5,$6)`+s.suffix,
		id, "dm", "", by.Tenant, by.ID, ts(now)); err != nil {
		return Channel{}, err
	}
	for _, u := range []string{by.ID, other} {
		if _, err := s.exec(ctx, s.ignore+` chat_members (channel_id, user_id, role, joined_at) VALUES ($1,$2,$3,$4)`+s.suffix,
			id, u, "member", ts(now)); err != nil {
			return Channel{}, err
		}
	}
	return s.channel(ctx, id)
}

func (s *Store) channel(ctx context.Context, id string) (Channel, error) {
	var ch Channel
	var created string
	err := s.db.QueryRowContext(ctx, s.rebind(`SELECT id, kind, name, tenant, created_by, created_at FROM chat_channels WHERE id = $1`), id).
		Scan(&ch.ID, &ch.Kind, &ch.Name, &ch.Tenant, &ch.CreatedBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Channel{}, ErrNotFound
	}
	ch.CreatedAt = parseTS(created)
	return ch, err
}

// member returns the actor's role in ch, or ErrForbidden. A user from another
// tenant is never a member, whatever the table says.
func (s *Store) member(ctx context.Context, by Actor, ch Channel) (string, error) {
	if ch.Tenant != "" && by.Tenant != ch.Tenant {
		return "", ErrForbidden
	}
	var role string
	err := s.db.QueryRowContext(ctx, s.rebind(`SELECT role FROM chat_members WHERE channel_id = $1 AND user_id = $2`), ch.ID, by.ID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	return role, err
}

// IsMember reports whether the user may read and subscribe to a channel. It
// answers false rather than an error for an unknown channel, so the hub's
// authorizer cannot be used to probe which channels exist.
func (s *Store) IsMember(ctx context.Context, by Actor, channelID string) bool {
	ch, err := s.channel(ctx, channelID)
	if err != nil {
		return false
	}
	_, err = s.member(ctx, by, ch)
	return err == nil
}

// AddMember adds a user to a group channel. Only its owner may.
func (s *Store) AddMember(ctx context.Context, by Actor, channelID, user string) error {
	ch, role, err := s.authorize(ctx, by, channelID)
	if err != nil {
		return err
	}
	if ch.Kind != "channel" || role != "owner" || user == "" {
		return ErrForbidden
	}
	if _, err := s.exec(ctx, s.ignore+` chat_members (channel_id, user_id, role, joined_at) VALUES ($1,$2,$3,$4)`+s.suffix,
		channelID, user, "member", ts(s.now())); err != nil {
		return err
	}
	s.announce(ctx, channelID, "member.joined", map[string]any{"user": user}, by.ID)
	return nil
}

// RemoveMember removes a user from a group channel. The owner may remove anyone
// else; anyone may remove themselves.
func (s *Store) RemoveMember(ctx context.Context, by Actor, channelID, user string) error {
	ch, role, err := s.authorize(ctx, by, channelID)
	if err != nil {
		return err
	}
	if ch.Kind != "channel" || (user != by.ID && role != "owner") || (user == ch.CreatedBy && user == by.ID && role == "owner") {
		return ErrForbidden
	}
	if _, err := s.exec(ctx, `DELETE FROM chat_members WHERE channel_id = $1 AND user_id = $2`, channelID, user); err != nil {
		return err
	}
	s.announce(ctx, channelID, "member.left", map[string]any{"user": user}, by.ID)
	return nil
}

func (s *Store) authorize(ctx context.Context, by Actor, channelID string) (Channel, string, error) {
	ch, err := s.channel(ctx, channelID)
	if err != nil {
		return Channel{}, "", ErrForbidden // do not reveal which ids exist
	}
	role, err := s.member(ctx, by, ch)
	return ch, role, err
}

// Channels lists the actor's channels, most recently active first, with unread
// counts.
func (s *Store) Channels(ctx context.Context, by Actor) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, s.rebind(`
		SELECT c.id, c.kind, c.name, c.tenant, c.created_by, c.created_at, m.role, m.last_read_seq,
		       COALESCE((SELECT MAX(seq) FROM chat_messages x WHERE x.channel_id = c.id), 0)
		FROM chat_members m JOIN chat_channels c ON c.id = m.channel_id
		WHERE m.user_id = $1 AND (c.tenant = '' OR c.tenant = $2)`), by.ID, by.Tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Summary
	for rows.Next() {
		var sm Summary
		var created string
		var read int64
		if err := rows.Scan(&sm.ID, &sm.Kind, &sm.Name, &sm.Tenant, &sm.CreatedBy, &created, &sm.Role, &read, &sm.LastSeq); err != nil {
			return nil, err
		}
		sm.CreatedAt = parseTS(created)
		sm.Unread = int(sm.LastSeq - read)
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeq != out[j].LastSeq {
			return out[i].LastSeq > out[j].LastSeq
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// Members lists the user ids of a channel the actor belongs to.
func (s *Store) Members(ctx context.Context, by Actor, channelID string) ([]string, error) {
	if _, _, err := s.authorize(ctx, by, channelID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT user_id FROM chat_members WHERE channel_id = $1 ORDER BY user_id`), channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

// Send stores a message and announces it. Seq numbers are gapless per channel,
// which is what lets a client detect a missed message.
func (s *Store) Send(ctx context.Context, by Actor, channelID, body string, meta json.RawMessage) (Message, error) {
	if strings.TrimSpace(body) == "" || len(body) > MaxBody {
		return Message{}, fmt.Errorf("%w: a message body is 1 to %d bytes", ErrInvalid, MaxBody)
	}
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	} else if !json.Valid(meta) || len(meta) > 2<<10 {
		return Message{}, fmt.Errorf("%w: meta must be JSON of at most 2 KiB", ErrInvalid)
	}
	if _, _, err := s.authorize(ctx, by, channelID); err != nil {
		return Message{}, err
	}
	msg := Message{ID: newID("m_"), ChannelID: channelID, Sender: by.ID, Body: body, Meta: meta, CreatedAt: s.now()}
	defer s.lock(channelID)()
	var err error
	for attempt := 0; attempt < 12; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(mrand.IntN(20)+attempt) * time.Millisecond)
		}
		err = s.db.QueryRowContext(ctx, s.rebind(`SELECT COALESCE(MAX(seq), 0) + 1 FROM chat_messages WHERE channel_id = $1`), channelID).Scan(&msg.Seq)
		if err != nil {
			return Message{}, err
		}
		_, err = s.exec(ctx, `INSERT INTO chat_messages (id, channel_id, seq, sender, body, meta, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			msg.ID, channelID, msg.Seq, by.ID, body, string(meta), ts(msg.CreatedAt))
		if err == nil {
			break
		}
		// Two senders took the same seq: the loser retries with the next one.
		if !isDuplicate(err) {
			return Message{}, err
		}
	}
	if err != nil {
		return Message{}, fmt.Errorf("chat: could not assign a sequence number: %w", err)
	}
	// The sender has, by definition, read up to their own message.
	_, _ = s.exec(ctx, `UPDATE chat_members SET last_read_seq = $1 WHERE channel_id = $2 AND user_id = $3 AND last_read_seq < $1`, msg.Seq, channelID, by.ID)
	s.announce(ctx, channelID, "message", msg, by.ID)
	return msg, nil
}

func isDuplicate(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate")
}

// History returns up to limit messages older than beforeSeq (all when zero),
// oldest first.
func (s *Store) History(ctx context.Context, by Actor, channelID string, beforeSeq int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if _, _, err := s.authorize(ctx, by, channelID); err != nil {
		return nil, err
	}
	q := `SELECT id, channel_id, seq, sender, body, meta, created_at FROM chat_messages WHERE channel_id = $1`
	args := []any{channelID}
	if beforeSeq > 0 {
		q += ` AND seq < $2`
		args = append(args, beforeSeq)
	}
	q += fmt.Sprintf(` ORDER BY seq DESC LIMIT %d`, limit)
	rows, err := s.db.QueryContext(ctx, s.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var meta, created string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.Seq, &m.Sender, &m.Body, &meta, &created); err != nil {
			return nil, err
		}
		m.Meta, m.CreatedAt = json.RawMessage(meta), parseTS(created)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// Since returns the messages after afterSeq, oldest first: what a reconnecting
// client asks for.
func (s *Store) Since(ctx context.Context, by Actor, channelID string, afterSeq int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	if _, _, err := s.authorize(ctx, by, channelID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(fmt.Sprintf(
		`SELECT id, channel_id, seq, sender, body, meta, created_at FROM chat_messages WHERE channel_id = $1 AND seq > $2 ORDER BY seq ASC LIMIT %d`, limit)),
		channelID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var meta, created string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.Seq, &m.Sender, &m.Body, &meta, &created); err != nil {
			return nil, err
		}
		m.Meta, m.CreatedAt = json.RawMessage(meta), parseTS(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkRead records that the actor has read up to seq (never backwards) and
// tells the channel, which is the read receipt.
func (s *Store) MarkRead(ctx context.Context, by Actor, channelID string, upTo int64) error {
	if _, _, err := s.authorize(ctx, by, channelID); err != nil {
		return err
	}
	var last int64
	if err := s.db.QueryRowContext(ctx, s.rebind(`SELECT COALESCE(MAX(seq), 0) FROM chat_messages WHERE channel_id = $1`), channelID).Scan(&last); err != nil {
		return err
	}
	if upTo <= 0 || upTo > last {
		upTo = last // read "everything" or a seq that does not exist yet
	}
	res, err := s.exec(ctx, `UPDATE chat_members SET last_read_seq = $1 WHERE channel_id = $2 AND user_id = $3 AND last_read_seq < $1`, upTo, channelID, by.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.announce(ctx, channelID, "receipt", map[string]any{"user": by.ID, "seq": upTo}, by.ID)
	}
	return nil
}

// Receipts returns each member's read position in a channel.
func (s *Store) Receipts(ctx context.Context, by Actor, channelID string) (map[string]int64, error) {
	if _, _, err := s.authorize(ctx, by, channelID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT user_id, last_read_seq FROM chat_members WHERE channel_id = $1`), channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var u string
		var n int64
		if err := rows.Scan(&u, &n); err != nil {
			return nil, err
		}
		out[u] = n
	}
	return out, rows.Err()
}

// Typing tells the channel the actor is typing. It is not stored.
func (s *Store) Typing(ctx context.Context, by Actor, channelID string) error {
	if _, _, err := s.authorize(ctx, by, channelID); err != nil {
		return err
	}
	s.announce(ctx, channelID, "typing", map[string]any{"user": by.ID}, by.ID)
	return nil
}
