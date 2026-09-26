package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Notifications turn pipeline events into messages for people. A notify rule
// names the event, the recipients and the default channels; each recipient's
// preferences decide what actually reaches them:
//
//   - channels are opted in or out globally, or per event pattern;
//   - quiet hours (in the recipient's time zone) defer delivery to the end of
//     the quiet window, except for urgent and critical notifications;
//   - a digest (hourly or daily) batches notifications into one message per
//     window and channel.
//
// Planning is pure (PlanNotifications); the host stores the planned
// notifications durably (NotifyStore) and a background loop delivers what is
// due (FlushNotifications).

// Digest modes.
const (
	DigestImmediate = "immediate"
	DigestHourly    = "hourly"
	DigestDaily     = "daily"
)

// QuietHours is a daily window ("22:00" to "07:00") during which non-urgent
// notifications wait. A window whose end is before its start spans midnight.
type QuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// NotifyPreferences are one person's notification settings.
type NotifyPreferences struct {
	User     string `json:"user"`
	TenantID string `json:"tenant_id,omitempty"`
	// Channels opts the person in (true) or out (false) of a channel for
	// every event.
	Channels map[string]bool `json:"channels,omitempty"`
	// Events overrides Channels per event pattern ("case.returned", "sla.*",
	// "*"): pattern -> channel -> on/off. The most specific pattern wins.
	Events map[string]map[string]bool `json:"events,omitempty"`
	// Timezone (IANA) is where quiet hours and digest windows are counted
	// (default UTC).
	Timezone string      `json:"timezone,omitempty"`
	Quiet    *QuietHours `json:"quiet_hours,omitempty"`
	// Digest: immediate (default), hourly or daily.
	Digest string `json:"digest,omitempty"`
	// DigestAt is the local time a daily digest is sent (default 08:00).
	DigestAt  string    `json:"digest_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Validate checks the preferences against the host's channels.
func (p *NotifyPreferences) Validate(channels []string) error {
	var errs []FieldError
	bad := func(path, rule, msg string) { errs = append(errs, FieldError{Path: path, Rule: rule, Message: msg}) }
	for ch := range p.Channels {
		if !slices.Contains(channels, ch) {
			bad("channels."+ch, "channel", fmt.Sprintf("%q is not a notification channel (%s)", ch, strings.Join(channels, ", ")))
		}
	}
	for pattern, chans := range p.Events {
		if strings.TrimSpace(pattern) == "" {
			bad("events", "event", "an event pattern is empty")
		}
		for ch := range chans {
			if !slices.Contains(channels, ch) {
				bad("events."+pattern+"."+ch, "channel", fmt.Sprintf("%q is not a notification channel", ch))
			}
		}
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		bad("timezone", "timezone", fmt.Sprintf("%q is not a time zone", p.Timezone))
	}
	if q := p.Quiet; q != nil {
		if _, ok := clockMinutes(q.Start); !ok {
			bad("quiet_hours.start", "time", "start must be a time like 22:00")
		}
		if _, ok := clockMinutes(q.End); !ok {
			bad("quiet_hours.end", "time", "end must be a time like 07:00")
		}
	}
	if !oneOf(p.Digest, "", DigestImmediate, DigestHourly, DigestDaily) {
		bad("digest", "option", "digest must be immediate, hourly or daily")
	}
	if p.DigestAt != "" {
		if _, ok := clockMinutes(p.DigestAt); !ok {
			bad("digest_at", "time", "digest_at must be a time like 08:00")
		}
	}
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Path < errs[j].Path })
		return &ValidationError{Message: "the notification preferences are invalid", Fields: errs}
	}
	return nil
}

// Enabled reports whether the person receives event on channel; byDefault is
// the notify rule's choice when the person has not said.
func (p *NotifyPreferences) Enabled(event, channel string, byDefault bool) bool {
	if p == nil {
		return byDefault
	}
	best, found, on := -1, false, false
	for pattern, chans := range p.Events {
		v, ok := chans[channel]
		if !ok || !EventMatches(pattern, event) {
			continue
		}
		// Specificity: an exact name beats any pattern, a longer prefix beats
		// a shorter one, "*" is the weakest.
		score := len(pattern)
		if pattern == event {
			score = 1 << 20
		} else if pattern == "*" {
			score = 0
		}
		if score > best {
			best, found, on = score, true, v
		}
	}
	if found {
		return on
	}
	if v, ok := p.Channels[channel]; ok {
		return v
	}
	return byDefault
}

func (p *NotifyPreferences) location() *time.Location {
	if p != nil && p.Timezone != "" {
		if loc, err := time.LoadLocation(p.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// QuietUntil reports whether t falls in the quiet hours, and when they end.
func (p *NotifyPreferences) QuietUntil(t time.Time) (time.Time, bool) {
	if p == nil || p.Quiet == nil {
		return time.Time{}, false
	}
	start, ok1 := clockMinutes(p.Quiet.Start)
	end, ok2 := clockMinutes(p.Quiet.End)
	if !ok1 || !ok2 || start == end {
		return time.Time{}, false
	}
	local := t.In(p.location())
	now := local.Hour()*60 + local.Minute()
	at := func(dayOffset, minutes int) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day()+dayOffset, minutes/60, minutes%60, 0, 0, local.Location()).UTC()
	}
	switch {
	case start < end && now >= start && now < end:
		return at(0, end), true
	case start > end && now >= start:
		return at(1, end), true
	case start > end && now < end:
		return at(0, end), true
	}
	return time.Time{}, false
}

// nextWindow is the end of the digest window containing t.
func (p *NotifyPreferences) nextWindow(t time.Time) time.Time {
	local := t.In(p.location())
	if p.Digest == DigestHourly {
		return time.Date(local.Year(), local.Month(), local.Day(), local.Hour()+1, 0, 0, 0, local.Location()).UTC()
	}
	m, ok := clockMinutes(p.DigestAt)
	if !ok {
		m = 8 * 60
	}
	next := time.Date(local.Year(), local.Month(), local.Day(), m/60, m%60, 0, 0, local.Location())
	if !next.After(local) {
		next = time.Date(local.Year(), local.Month(), local.Day()+1, m/60, m%60, 0, 0, local.Location())
	}
	return next.UTC()
}

// Schedule decides when a notification of severity reaches the person:
// deliverAt, whether it joins a digest, and why it waits (empty when it is
// delivered now).
func (p *NotifyPreferences) Schedule(severity string, now time.Time) (deliverAt time.Time, digest bool, reason string) {
	if severity == SeverityUrgent || severity == SeverityCritical {
		if _, quiet := p.QuietUntil(now); quiet {
			return now, false, "bypassed quiet hours: " + severity
		}
		return now, false, ""
	}
	if p != nil && (p.Digest == DigestHourly || p.Digest == DigestDaily) {
		at := p.nextWindow(now)
		reason = "digest:" + p.Digest
		if end, quiet := p.QuietUntil(at); quiet {
			at, reason = end, reason+", after quiet hours"
		}
		return at, true, reason
	}
	if end, quiet := p.QuietUntil(now); quiet {
		return end, false, "quiet_hours"
	}
	return now, false, ""
}

func clockMinutes(s string) (int, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// EventMatches supports exact names, "*" and prefix patterns like "sla.*".
func EventMatches(pattern, name string) bool {
	if pattern == "*" || pattern == name {
		return true
	}
	return strings.HasSuffix(pattern, ".*") && strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
}

func checkNotifyRule(r NotifyRule, def *Definition) error {
	if strings.TrimSpace(r.Event) == "" {
		return fmt.Errorf("needs an event")
	}
	if len(r.To) == 0 {
		return fmt.Errorf("needs recipients (to)")
	}
	for _, to := range r.To {
		if strings.TrimSpace(to) == "" || (strings.HasPrefix(to, "role:") && strings.TrimPrefix(to, "role:") == "") {
			return fmt.Errorf("recipient %q is empty", to)
		}
	}
	if !oneOf(r.Severity, "", SeverityInfo, SeverityWarning, SeverityUrgent, SeverityCritical) {
		return fmt.Errorf("severity %q is not info, warning, urgent or critical", r.Severity)
	}
	for _, ch := range r.Channels {
		if !validName(ch) {
			return fmt.Errorf("invalid channel name %q", ch)
		}
	}
	if r.Stage != "" && !slices.ContainsFunc(def.Stages, func(s Stage) bool { return s.Name == r.Stage }) {
		return fmt.Errorf("names unknown stage %q", r.Stage)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Planning
// ---------------------------------------------------------------------------

// Notification is one message for one person on one channel.
type Notification struct {
	ID         string         `json:"id"`
	TenantID   string         `json:"tenant_id,omitempty"`
	User       string         `json:"user"`
	Channel    string         `json:"channel"`
	Pipeline   string         `json:"pipeline"`
	CaseID     string         `json:"case_id"`
	CaseNumber string         `json:"case_number,omitempty"`
	Event      string         `json:"event"`
	Stage      string         `json:"stage,omitempty"`
	Severity   string         `json:"severity"`
	Subject    string         `json:"subject"`
	Body       string         `json:"body,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
	// At is when the event happened; DeliverAt when the message is due.
	At        time.Time `json:"at"`
	DeliverAt time.Time `json:"deliver_at"`
	// Digest groups notifications delivered as one message ("" = alone).
	Digest string `json:"digest,omitempty"`
	// Deferred says why the message waits (quiet_hours, digest:hourly, …).
	Deferred  string `json:"deferred,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
	Dead      bool   `json:"dead,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// PreferencesFunc returns a person's preferences (nil when unset).
type PreferencesFunc func(user string) (*NotifyPreferences, error)

// PlanNotifications applies the pipeline's notify rules to an event: the
// recipients, the channels each one receives, and when. eventID must be
// stable across retries (it makes the notification ids deterministic, so
// planning an event twice stores nothing twice). channels are the host's
// channels.
func (e *Engine) PlanNotifications(ctx context.Context, c *Case, eventID string, ev Event, channels []string, prefs PreferencesFunc, now time.Time) ([]Notification, error) {
	var out []Notification
	cache := map[string]*NotifyPreferences{}
	for i, rule := range e.C.Def.Notify {
		if !EventMatches(rule.Event, ev.Name) || (rule.Stage != "" && rule.Stage != ev.Stage) {
			continue
		}
		if rule.Condition != "" {
			env := e.Env(c, Actor{ID: ev.Actor})
			env["event"] = map[string]any{"name": ev.Name, "stage": ev.Stage, "actor": ev.Actor, "detail": ev.Detail}
			ok, err := e.cond(rule.Condition, env)
			if err != nil || !ok {
				continue
			}
		}
		severity := orDefault(rule.Severity, SeverityInfo)
		subject := renderNotice(orDefault(rule.Subject, "{case.number}: {event}"), c, ev)
		body := renderNotice(rule.Body, c, ev)
		for _, user := range e.recipients(ctx, c, ev, rule.To) {
			p, seen := cache[user]
			if !seen {
				var err error
				if p, err = prefs(user); err != nil {
					return nil, err
				}
				cache[user] = p
			}
			for _, ch := range channels {
				byDefault := len(rule.Channels) == 0 || slices.Contains(rule.Channels, ch)
				if !p.Enabled(ev.Name, ch, byDefault) {
					continue
				}
				at, digest, reason := p.Schedule(severity, now)
				sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", eventID, i, user, ch)))
				n := Notification{ID: "ntf_" + hex.EncodeToString(sum[:12]), TenantID: c.TenantID, User: user, Channel: ch,
					Pipeline: c.Pipeline, CaseID: c.ID, CaseNumber: c.Number, Event: ev.Name, Stage: ev.Stage, Severity: severity,
					Subject: subject, Body: body, Detail: ev.Detail, At: ev.At, DeliverAt: at, Deferred: reason}
				if digest {
					n.Digest = fmt.Sprintf("%s|%s|%s|%d", c.TenantID, user, ch, at.Unix())
				}
				out = append(out, n)
			}
		}
	}
	return out, nil
}

// recipients resolves a rule's recipients for an event, deduplicated.
func (e *Engine) recipients(ctx context.Context, c *Case, ev Event, to []string) []string {
	var out []string
	add := func(id string) {
		if id != "" && id != "system" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	ss := c.Stages[ev.Stage]
	if ss == nil {
		ss = c.Stages[c.Stage]
	}
	byRole := func(role string) bool {
		found := false
		for _, w := range e.workers(ctx) {
			if !w.Inactive && slices.Contains(w.Roles, role) {
				add(w.ID)
				found = true
			}
		}
		return found
	}
	for _, r := range to {
		switch {
		case r == "assignee":
			if who, ok := ev.Detail["assignee"].(string); ok && who != "" {
				add(who)
			} else if ss != nil {
				add(ss.Assignee)
			}
		case r == "previous_assignee":
			if ss != nil {
				add(ss.PreviousAssignee)
			}
		case r == "applicant":
			add(c.CreatedBy)
		case r == "actor":
			add(ev.Actor)
		case r == "escalation":
			// An escalation level's notify list: roles, or else user ids.
			names, _ := ev.Detail["notify"].([]string)
			if names == nil {
				for _, v := range asList(ev.Detail["notify"]) {
					names = append(names, fmt.Sprint(v))
				}
			}
			for _, name := range names {
				if !byRole(name) {
					add(name)
				}
			}
		case strings.HasPrefix(r, "role:"):
			byRole(strings.TrimPrefix(r, "role:"))
		default:
			add(strings.TrimPrefix(r, "user:"))
		}
	}
	return out
}

var noticeTokenRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_.]*)\}`)

// renderNotice fills a subject or body template.
func renderNotice(tmpl string, c *Case, ev Event) string {
	return noticeTokenRe.ReplaceAllStringFunc(tmpl, func(tok string) string {
		key := tok[1 : len(tok)-1]
		switch key {
		case "event":
			return ev.Name
		case "stage":
			return ev.Stage
		case "actor":
			return ev.Actor
		case "case.id":
			return c.ID
		case "case.number":
			return c.Number
		case "case.status":
			return c.Status
		case "case.stage":
			return c.Stage
		}
		if path, ok := strings.CutPrefix(key, "data."); ok {
			if v, ok := c.Get(path); ok && v != nil {
				return fmt.Sprint(v)
			}
			return ""
		}
		return tok
	})
}

// ---------------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------------

// NotificationBatch is what one delivery sends: a single notification, or a
// digest of every notification of one person, channel and window.
type NotificationBatch struct {
	TenantID string         `json:"tenant_id,omitempty"`
	User     string         `json:"user"`
	Channel  string         `json:"channel"`
	Digest   bool           `json:"digest"`
	Items    []Notification `json:"items"`
}

// Subject is the message subject: the notification's own, or a digest
// summary.
func (b NotificationBatch) Subject() string {
	if !b.Digest && len(b.Items) == 1 {
		return b.Items[0].Subject
	}
	if len(b.Items) == 1 {
		return "1 notification"
	}
	return fmt.Sprintf("%d notifications", len(b.Items))
}

// Body is the message body: the notification's own, or one line per
// notification of the digest.
func (b NotificationBatch) Body() string {
	if !b.Digest && len(b.Items) == 1 {
		return b.Items[0].Body
	}
	lines := make([]string, len(b.Items))
	for i, n := range b.Items {
		lines[i] = "- " + n.Subject
	}
	return strings.Join(lines, "\n")
}

// NotifyStore persists preferences and planned notifications.
type NotifyStore interface {
	// Preferences returns a person's preferences, or nil when unset.
	Preferences(ctx context.Context, tenant, user string) (*NotifyPreferences, error)
	SetPreferences(ctx context.Context, p *NotifyPreferences) error
	// EnqueueNotifications stores planned notifications; an id already
	// stored is skipped.
	EnqueueNotifications(ctx context.Context, items []Notification) error
	// ClaimNotifications leases up to limit notifications due by now.
	ClaimNotifications(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]Notification, error)
	AckNotifications(ctx context.Context, ids []string) error
	// RetryNotifications records a failed delivery: retried at next, or
	// dead-lettered.
	RetryNotifications(ctx context.Context, ids []string, next time.Time, dead bool, lastError string) error
	// PendingNotifications lists a person's undelivered notifications.
	PendingNotifications(ctx context.Context, tenant, user string, limit int) ([]Notification, error)
}

// FlushNotifications delivers the notifications due by now: every digest
// as one message, everything else one by one. A failed delivery is retried
// with Backoff and dead-lettered after maxAttempts. It returns how many
// messages were sent.
func FlushNotifications(ctx context.Context, s NotifyStore, now time.Time, maxAttempts int, send func(context.Context, NotificationBatch) error) (int, error) {
	due, err := s.ClaimNotifications(ctx, 500, time.Minute, now)
	if err != nil || len(due) == 0 {
		return 0, err
	}
	var order []string
	batches := map[string]*NotificationBatch{}
	for _, n := range due {
		key := n.Digest
		if key == "" {
			key = "id:" + n.ID
		}
		b := batches[key]
		if b == nil {
			b = &NotificationBatch{TenantID: n.TenantID, User: n.User, Channel: n.Channel, Digest: n.Digest != ""}
			batches[key] = b
			order = append(order, key)
		}
		b.Items = append(b.Items, n)
	}
	sent := 0
	for _, key := range order {
		b := batches[key]
		ids := make([]string, len(b.Items))
		attempts := 0
		for i, n := range b.Items {
			ids[i] = n.ID
			attempts = max(attempts, n.Attempts)
		}
		if err := send(ctx, *b); err != nil {
			attempts++
			if rerr := s.RetryNotifications(ctx, ids, now.Add(Backoff(attempts)), attempts >= max(1, maxAttempts), err.Error()); rerr != nil {
				return sent, rerr
			}
			continue
		}
		if err := s.AckNotifications(ctx, ids); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}
