package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestNotifyPreferencesChannelsQuietHoursAndDigest(t *testing.T) {
	channels := []string{"email", "sms", "push"}
	p := &NotifyPreferences{User: "u1", Timezone: "Asia/Kathmandu",
		Channels: map[string]bool{"sms": false},
		Events:   map[string]map[string]bool{"sla.*": {"sms": true}, "sla.warning": {"sms": false}, "*": {"push": true}},
		Quiet:    &QuietHours{Start: "22:00", End: "07:00"}}
	if err := p.Validate(channels); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		event, channel  string
		byDefault, want bool
	}{
		{"case.returned", "email", true, true},   // the rule's default
		{"case.returned", "email", false, false}, // not a default, not opted in
		{"case.returned", "sms", true, false},    // opted out globally
		{"sla.breached", "sms", false, true},     // opted in for sla.*
		{"sla.warning", "sms", true, false},      // the exact name beats sla.*
		{"note.added", "push", false, true},      // "*" opts in
	} {
		if got := p.Enabled(tc.event, tc.channel, tc.byDefault); got != tc.want {
			t.Errorf("%s/%s default %v: got %v", tc.event, tc.channel, tc.byDefault, got)
		}
	}

	// Kathmandu is UTC+05:45: 23:00 local is 17:15Z; quiet until 07:00 local
	// (01:15Z the next day).
	late := mustTime(t, "2026-05-01T17:15:00Z")
	if end, quiet := p.QuietUntil(late); !quiet || !end.Equal(mustTime(t, "2026-05-02T01:15:00Z")) {
		t.Fatalf("overnight quiet: %v %v", end, quiet)
	}
	early := mustTime(t, "2026-05-01T00:15:00Z") // 06:00 local
	if end, quiet := p.QuietUntil(early); !quiet || !end.Equal(mustTime(t, "2026-05-01T01:15:00Z")) {
		t.Fatalf("early quiet: %v %v", end, quiet)
	}
	if _, quiet := p.QuietUntil(mustTime(t, "2026-05-01T06:15:00Z")); quiet { // noon
		t.Fatal("noon is not quiet")
	}
	day := &NotifyPreferences{Quiet: &QuietHours{Start: "12:00", End: "13:00"}}
	if end, quiet := day.QuietUntil(mustTime(t, "2026-05-01T12:30:00Z")); !quiet || end.Hour() != 13 {
		t.Fatalf("daytime quiet: %v", end)
	}

	// Deferral, and critical bypass.
	at, digest, reason := p.Schedule(SeverityInfo, late)
	if digest || reason != "quiet_hours" || !at.Equal(mustTime(t, "2026-05-02T01:15:00Z")) {
		t.Fatalf("info in quiet hours: %v %v %q", at, digest, reason)
	}
	at, _, reason = p.Schedule(SeverityCritical, late)
	if !at.Equal(late) || !strings.Contains(reason, "critical") {
		t.Fatalf("critical: %v %q", at, reason)
	}
	if at, _, reason := p.Schedule(SeverityInfo, mustTime(t, "2026-05-01T06:15:00Z")); reason != "" || at.Hour() != 6 {
		t.Fatalf("outside quiet hours: %v %q", at, reason)
	}

	// Hourly digest windows end on the local hour; one that ends in quiet
	// hours waits for the morning.
	p.Digest = DigestHourly
	at, digest, _ = p.Schedule(SeverityWarning, mustTime(t, "2026-05-01T06:20:00Z")) // 12:05 local
	if !digest || !at.Equal(mustTime(t, "2026-05-01T07:15:00Z")) {                   // 13:00 local
		t.Fatalf("hourly window: %v", at)
	}
	at, _, reason = p.Schedule(SeverityInfo, mustTime(t, "2026-05-01T16:20:00Z")) // 22:05 local
	if !at.Equal(mustTime(t, "2026-05-02T01:15:00Z")) || !strings.Contains(reason, "quiet") {
		t.Fatalf("digest in quiet hours: %v %q", at, reason)
	}
	p.Digest, p.DigestAt, p.Quiet = DigestDaily, "09:30", nil
	at, _, _ = p.Schedule(SeverityInfo, mustTime(t, "2026-05-01T06:20:00Z")) // 12:05 local
	if !at.Equal(mustTime(t, "2026-05-02T03:45:00Z")) {                      // 09:30 local next day
		t.Fatalf("daily window: %v", at)
	}

	bad := &NotifyPreferences{Timezone: "Mars/Olympus", Digest: "weekly", Channels: map[string]bool{"fax": true}, Quiet: &QuietHours{Start: "25:00", End: "7"}}
	var ve *ValidationError
	if err := bad.Validate(channels); !errors.As(err, &ve) || len(ve.Fields) != 5 {
		t.Fatalf("invalid preferences: %v", err)
	}
}

func notifyDef() *Definition {
	return &Definition{
		Name:    "claims",
		Workers: []Worker{{ID: "sup1", Roles: []string{"supervisor"}}, {ID: "sup2", Roles: []string{"supervisor"}, Inactive: true}},
		Notes:   &NotesPolicy{ApplicantMayWrite: true},
		Forms:   []Form{{Name: "claim", Inputs: []Input{{Name: "title", Kind: KindText}}}},
		Stages: []Stage{
			{Name: "apply", Public: true, Page: &Page{Groups: []Group{{Name: "g", Forms: []string{"claim"}}}}},
			{Name: "assess", Roles: []string{"officer"}, Claimable: true},
		},
		Notify: []NotifyRule{
			{Event: "stage.entered", Stage: "assess", To: []string{"applicant", "role:supervisor"}, Channels: []string{"email"},
				Subject: "{case.number} ({data.claim.title}) is at {stage}"},
			{Event: "note.added", To: []string{"applicant"}, Channels: []string{"email"}, Severity: SeverityCritical, When: "event.actor != case.created_by"},
		},
	}
}

func TestPlanAndFlushNotifications(t *testing.T) {
	ctx := context.Background()
	clk := &clock{}
	clk.set("2026-05-01T21:30:00Z") // quiet for u1 (UTC 21:00-07:00)
	e := reviewEngine(t, notifyDef(), clk)
	store := NewMemoryStore()
	if err := store.SetPreferences(ctx, &NotifyPreferences{User: "u1", TenantID: "t", Quiet: &QuietHours{Start: "21:00", End: "07:00"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPreferences(ctx, &NotifyPreferences{User: "sup1", TenantID: "t", Digest: DigestHourly, Channels: map[string]bool{"sms": true}}); err != nil {
		t.Fatal(err)
	}
	prefs := func(user string) (*NotifyPreferences, error) { return store.Preferences(ctx, "t", user) }
	channels := []string{"email", "sms"}
	plan := func(c *Case) {
		t.Helper()
		for i, ev := range c.Events() {
			items, err := e.PlanNotifications(ctx, c, c.ID+"-"+ev.Name+string(rune('a'+i)), ev, channels, prefs, clk.now())
			if err != nil {
				t.Fatal(err)
			}
			if err := store.EnqueueNotifications(ctx, items); err != nil {
				t.Fatal(err)
			}
			if err := store.EnqueueNotifications(ctx, items); err != nil { // idempotent
				t.Fatal(err)
			}
		}
	}
	cases := map[string]*Case{}
	for _, id := range []string{"c1", "c2", "c3"} {
		c, _ := e.Start(ctx, Actor{ID: "u1"}, StartOptions{ID: id, Number: "N-" + id, TenantID: "t"})
		c, err := e.Act(ctx, c, Actor{ID: "u1"}, "apply", "submit", ActInput{Data: map[string]any{"claim": map[string]any{"title": "roof " + id}}})
		if err != nil {
			t.Fatal(err)
		}
		plan(c)
		cases[id] = c
	}
	pending, _ := store.PendingNotifications(ctx, "t", "u1", 50)
	if len(pending) != 3 || pending[0].Deferred != "quiet_hours" || !pending[0].DeliverAt.Equal(mustTime(t, "2026-05-02T07:00:00Z")) || pending[0].Subject != "N-c1 (roof c1) is at assess" {
		t.Fatalf("u1 pending: %+v", pending)
	}
	sup, _ := store.PendingNotifications(ctx, "t", "sup1", 50)
	if len(sup) != 6 || sup[0].Digest == "" || !sup[0].DeliverAt.Equal(mustTime(t, "2026-05-01T22:00:00Z")) {
		t.Fatalf("sup1 digest (email + opted-in sms): %+v", sup)
	}
	if p, _ := store.PendingNotifications(ctx, "t", "sup2", 50); len(p) != 0 {
		t.Fatal("inactive workers are not notified")
	}

	// A critical notification bypasses quiet hours; the applicant's own note
	// notifies nobody (the rule's condition).
	noted, err := e.AddNote(ctx, cases["c1"], Actor{ID: "o1", Roles: []string{"officer"}}, "please call us", false, "")
	if err != nil {
		t.Fatal(err)
	}
	plan(noted)
	own, err := e.AddNote(ctx, noted, Actor{ID: "u1"}, "thanks", false, "")
	if err != nil {
		t.Fatal(err)
	}
	plan(own)

	var sent []NotificationBatch
	send := func(_ context.Context, b NotificationBatch) error { sent = append(sent, b); return nil }
	n, err := FlushNotifications(ctx, store, clk.now(), 3, send)
	if err != nil || n != 1 || sent[0].User != "u1" || sent[0].Items[0].Severity != SeverityCritical || sent[0].Digest {
		t.Fatalf("critical now: %d %v %+v", n, err, sent)
	}

	// The digest window closes: one message per channel for three events.
	sent = nil
	clk.set("2026-05-01T22:00:00Z")
	if n, _ := FlushNotifications(ctx, store, clk.now(), 3, send); n != 2 {
		t.Fatalf("digest flush: %d %+v", n, sent)
	}
	for _, b := range sent {
		if !b.Digest || b.User != "sup1" || len(b.Items) != 3 || b.Subject() != "3 notifications" || strings.Count(b.Body(), "\n") != 2 {
			t.Fatalf("digest batch: %+v", b)
		}
	}

	// Quiet hours end: u1's three deferred notifications, one by one; a
	// failing channel is retried with backoff, then dead-lettered.
	sent = nil
	clk.set("2026-05-02T07:00:00Z")
	fail := func(_ context.Context, b NotificationBatch) error { return errors.New("smtp down") }
	if n, _ := FlushNotifications(ctx, store, clk.now(), 2, fail); n != 0 {
		t.Fatal("failed sends are not counted")
	}
	pending, _ = store.PendingNotifications(ctx, "t", "u1", 50)
	if len(pending) != 3 || pending[0].Attempts != 1 || pending[0].LastError != "smtp down" || !pending[0].DeliverAt.After(clk.now()) {
		t.Fatalf("retry: %+v", pending)
	}
	clk.add(time.Hour)
	_, _ = FlushNotifications(ctx, store, clk.now(), 2, fail)
	if pending, _ = store.PendingNotifications(ctx, "t", "u1", 50); len(pending) != 0 {
		t.Fatalf("dead-lettered notifications are not pending: %+v", pending)
	}
}

// notifyConformance runs the NotifyStore contract against a store.
func notifyConformance(t *testing.T, s NotifyStore) {
	ctx := context.Background()
	if p, err := s.Preferences(ctx, "t", "u1"); err != nil || p != nil {
		t.Fatalf("unset preferences: %v %v", p, err)
	}
	p := &NotifyPreferences{User: "u1", TenantID: "t", Digest: DigestDaily, Channels: map[string]bool{"sms": false}, UpdatedAt: time.Unix(10, 0)}
	if err := s.SetPreferences(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.Digest = DigestHourly
	if err := s.SetPreferences(ctx, p); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, err := s.Preferences(ctx, "t", "u1"); err != nil || got.Digest != DigestHourly || got.Channels["sms"] {
		t.Fatalf("preferences: %+v %v", got, err)
	}
	if got, _ := s.Preferences(ctx, "other", "u1"); got != nil {
		t.Fatal("preferences are per tenant")
	}
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	items := []Notification{
		{ID: "n1", TenantID: "t", User: "u1", Channel: "email", Event: "a", Subject: "one", At: now, DeliverAt: now, Digest: "d"},
		{ID: "n2", TenantID: "t", User: "u1", Channel: "email", Event: "b", Subject: "two", At: now, DeliverAt: now, Digest: "d"},
		{ID: "n3", TenantID: "t", User: "u1", Channel: "sms", Event: "c", Subject: "later", At: now, DeliverAt: now.Add(time.Hour)},
	}
	if err := s.EnqueueNotifications(ctx, items); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueNotifications(ctx, items[:1]); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if pending, _ := s.PendingNotifications(ctx, "t", "u1", 10); len(pending) != 3 || pending[2].ID != "n3" {
		t.Fatalf("pending: %+v", pending)
	}
	claimed, err := s.ClaimNotifications(ctx, 10, time.Minute, now)
	if err != nil || len(claimed) != 2 || claimed[0].Subject != "one" {
		t.Fatalf("claim: %v %+v", err, claimed)
	}
	if again, _ := s.ClaimNotifications(ctx, 10, time.Minute, now); len(again) != 0 {
		t.Fatalf("leased notifications are not claimed twice: %+v", again)
	}
	if err := s.RetryNotifications(ctx, []string{"n2"}, now.Add(time.Second), false, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.AckNotifications(ctx, []string{"n1"}); err != nil {
		t.Fatal(err)
	}
	claimed, _ = s.ClaimNotifications(ctx, 10, time.Minute, now.Add(2*time.Second))
	if len(claimed) != 1 || claimed[0].ID != "n2" || claimed[0].Attempts != 1 || claimed[0].LastError != "boom" {
		t.Fatalf("retried: %+v", claimed)
	}
	if err := s.RetryNotifications(ctx, []string{"n2"}, now, true, "dead"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PendingNotifications(ctx, "t", "u1", 10); len(pending) != 1 || pending[0].ID != "n3" {
		t.Fatalf("after dead letter: %+v", pending)
	}
	claimed, _ = s.ClaimNotifications(ctx, 10, time.Minute, now.Add(2*time.Hour))
	if len(claimed) != 1 || claimed[0].ID != "n3" {
		t.Fatalf("due later: %+v", claimed)
	}
}
