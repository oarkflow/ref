package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/pipeline"
)

const reviewApp = `
name "claims"

resource "db" {
  kind "database.sql"
  config {
    driver env("RV_DRIVER")
    dsn env("RV_DSN")
    migrations [
      "CREATE TABLE IF NOT EXISTS mail (user_id TEXT NOT NULL, channel TEXT NOT NULL, subject TEXT NOT NULL, digest BOOLEAN NOT NULL, n INTEGER NOT NULL, severity TEXT NOT NULL)"
    ]
  }
}

resource "jwt" {
  kind "auth.jwt"
  config {
    secret env.required("RV_JWT_SECRET")
  }
}

resource "cases" {
  kind "pipeline.cases"
  config {
    database "db"
    notify_channels {
      email "send_mail"
      sms "send_mail"
    }
  }
}

pipeline "claim" {
  worker "s1" {
    roles ["senior"]
  }
  worker "s2" {
    roles ["senior"]
  }
  worker "s3" {
    roles ["senior"]
  }
  notes {
    applicant_may_write true
  }

  form "claim" {
    input "title" {
      kind text
      required true
    }
    input "amount" {
      kind number
      required true
    }
    input "iban" {
      kind text
      sensitive true
    }
  }

  stage "apply" {
    public true
    page {
      group "g" {
        forms ["claim"]
      }
    }
  }

  stage "screen" {
    roles ["screener"]
    review "sampling" {
      percent 0
      salt "s1"
      always_review_if ["claim.amount > 1000"]
    }
    action "ok" {
      outcome advance
    }
  }

  stage "assess" {
    roles ["officer", "senior"]
    page {
      group "submitted" {
        mode readonly
        forms ["claim"]
      }
    }
    review "triage" {
      bucket "urgent" {
        condition "claim.amount > 1000"
        priority 1
        queue "urgent"
      }
      bucket "normal" {
        priority 2
        queue "standard"
      }
    }
    review "diff" {
      against submission
    }
    review "gate" {
      approvals 2
      roles ["senior"]
    }
    action "accept" {
      outcome advance
    }
    action "return" {
      outcome return
      return_to "apply"
    }
  }

  stage "done" {
    roles ["officer"]
    action "close" {
      outcome approve
    }
  }

  notify "stage.entered" {
    stage "assess"
    to ["role:senior"]
    channels ["email"]
    subject "New work: {case.number}"
  }
  notify "review.rejected" {
    to ["applicant"]
    channels ["email"]
    severity critical
    subject "{case.number} was rejected by a reviewer"
  }
  notify "note.added" {
    to ["applicant"]
    channels ["email"]
    subject "A note on {case.number}"
    condition "event.actor != case.created_by"
  }
}

intent "send_mail" {
  response "n"
  node "n" {
    uses "database.exec"
    resource "db"
    kind effect
    requires [input]
    provides [n]
    config {
      statement "INSERT INTO mail (user_id, channel, subject, digest, n, severity) VALUES ($1, $2, $3, $4, $5, $6)"
      args ["input.user", "input.channel", "input.subject", "input.digest", "input.count", "input.severity"]
    }
  }
}

intent "mail" {
  response "rows"
  node "rows" {
    uses "database.query"
    resource "db"
    provides [rows]
    config {
      statement "SELECT user_id, channel, subject, digest, n, severity FROM mail"
    }
  }
}

intent "start" {
  response "v"
  node "v" {
    uses "pipeline.start"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}
intent "list" {
  response "v"
  node "v" {
    uses "pipeline.list"
    resource "cases"
    provides [v]
  }
}
intent "view" {
  response "v"
  node "v" {
    uses "pipeline.view"
    resource "cases"
    provides [v]
  }
}
intent "get" {
  response "v"
  node "v" {
    uses "pipeline.get"
    resource "cases"
    provides [v]
  }
}
intent "act" {
  response "v"
  node "v" {
    uses "pipeline.act"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}
intent "node" {
  response "v"
  node "v" {
    uses "pipeline.node"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}
intent "note" {
  response "v"
  node "v" {
    uses "pipeline.note"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}
intent "prefs" {
  response "v"
  node "v" {
    uses "pipeline.notify_prefs"
    resource "cases"
    provides [v]
  }
}
intent "prefs_set" {
  response "v"
  node "v" {
    uses "pipeline.notify_prefs_set"
    resource "cases"
    kind effect
    requires [input]
    provides [v]
  }
}

route "start" { method POST path "/claims" intent "start" auth "jwt" status 201 }
route "list" { method GET path "/claims" intent "list" auth "jwt" }
route "view" { method GET path "/claims/:id" intent "view" auth "jwt" }
route "get" { method GET path "/claims/:id/history" intent "get" auth "jwt" }
route "act" { method POST path "/claims/:id/stages/:stage/actions/:action" intent "act" auth "jwt" }
route "node" { method POST path "/claims/:id/stages/:stage/nodes/:node/:verb" intent "node" auth "jwt" }
route "note" { method POST path "/claims/:id/notes" intent "note" auth "jwt" }
route "prefs" { method GET path "/me/notifications" intent "prefs" auth "jwt" }
route "prefs_set" { method PUT path "/me/notifications" intent "prefs_set" auth "jwt" }
route "mail" { method GET path "/mail" intent "mail" auth "jwt" }
`

// Review modes and notification preferences end to end: sampling with an
// always-review override, triage-ordered work lists, diff reviews after a
// correction, a two-reviewer gate, quiet hours (deferral and the critical
// bypass) and a daily digest delivered as one message.
func TestPipelineReviewModesAndNotificationPreferences(t *testing.T) {
	dir := t.TempDir()
	runReviewModes(t, "sqlite", "file:"+filepath.Join(dir, "rv.db")+"?_pragma=busy_timeout(5000)")
}

// TestPipelineReviewModesPostgres runs the same journey on PostgreSQL when
// TEST_POSTGRES_DSN is set.
func TestPipelineReviewModesPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	runReviewModes(t, "pgx", freshPostgres(t, dsn))
}

func runReviewModes(t *testing.T, driver, dsn string) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.bcl")
	if err := os.WriteFile(path, []byte(reviewApp), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultLoadOptions()
	opts.Env = func(name string) (string, bool) {
		return map[string]string{"RV_DRIVER": driver, "RV_DSN": dsn}[name], true
	}
	if r := Validate(t.Context(), []byte(reviewApp), dir, opts); !r.Valid {
		t.Fatalf("validate: %v", r.Errors)
	}
	h := newAppHarness(t, path, map[string]string{
		"RV_DRIVER":     driver,
		"RV_DSN":        dsn,
		"RV_JWT_SECRET": "review-test-secret-0123456789abcdef-xyz",
	})
	res, _ := h.platform.Resource("cases")
	cases := res.(*PipelineCases)
	u1 := h.token("jwt", "u1", nil, nil)
	screener := h.token("jwt", "sc1", []string{"screener"}, nil)
	officer := h.token("jwt", "o1", []string{"officer"}, nil)
	s1 := h.token("jwt", "s1", []string{"senior"}, nil)
	s2 := h.token("jwt", "s2", []string{"senior"}, nil)
	s3 := h.token("jwt", "s3", []string{"senior"}, nil)

	// Preferences: u1 is in quiet hours now; s1 takes a daily digest; s2 opts
	// out of new-work email; s3 keeps the defaults (immediate).
	loc, _ := time.LoadLocation("Asia/Kathmandu")
	now := time.Now().In(loc)
	status, body := h.call("PUT", "/me/notifications", u1, map[string]any{"timezone": "Asia/Kathmandu",
		"quiet_hours": map[string]any{"start": now.Add(-time.Hour).Format("15:04"), "end": now.Add(time.Hour).Format("15:04")}})
	if status != 200 || dig(body, "preferences", "quiet_hours", "start") == nil || fmt.Sprint(dig(body, "channels")) != "[email sms]" {
		t.Fatalf("u1 prefs: %d %v", status, body)
	}
	if status, body := h.call("PUT", "/me/notifications", s1, map[string]any{"timezone": "Asia/Kathmandu", "digest": "daily",
		"digest_at": now.Add(3 * time.Hour).Format("15:04")}); status != 200 {
		t.Fatalf("s1 prefs: %d %v", status, body)
	}
	if status, body := h.call("PUT", "/me/notifications", s2, map[string]any{"events": map[string]any{"stage.entered": map[string]any{"email": false}}}); status != 200 {
		t.Fatalf("s2 prefs: %d %v", status, body)
	}
	status, body = h.call("PUT", "/me/notifications", s3, map[string]any{"digest": "weekly", "channels": map[string]any{"fax": true}})
	if status != 422 || !strings.Contains(fmt.Sprint(body), "digest") || !strings.Contains(fmt.Sprint(body), "fax") {
		t.Fatalf("invalid prefs: %d %v", status, body)
	}
	if status, _ := h.call("GET", "/me/notifications", "", nil); status != 401 && status != 403 {
		t.Fatalf("anonymous prefs: %d", status)
	}

	start := func(title string, amount float64) string {
		t.Helper()
		status, body := h.call("POST", "/claims", u1, map[string]any{"data": map[string]any{"claim": map[string]any{"title": title, "amount": amount, "iban": "NP12345678901234"}}})
		if status != 201 {
			t.Fatalf("start: %d %v", status, body)
		}
		id := fmt.Sprint(dig(body, "case", "id"))
		if status, body := h.call("POST", "/claims/"+id+"/stages/apply/actions/submit", u1, map[string]any{}); status != 200 {
			t.Fatalf("submit: %d %v", status, body)
		}
		return id
	}
	small := start("scratch", 50)
	big := start("flood", 5000)

	// Sampling: at 0% the small claim passes screening with an audit record;
	// the big one is always reviewed.
	status, hist := h.call("GET", "/claims/"+small+"/history", officer, nil)
	if status != 200 || dig(hist, "stage") != "assess" || dig(hist, "stages", "screen", "completed_by") != "system" {
		t.Fatalf("small claim: %d %v", status, dig(hist, "stages", "screen"))
	}
	sampling := dig(hist, "stages", "screen", "review", "sampling")
	if dig(sampling, "sampled") != false || dig(sampling, "bucket") != float64(pipeline.SampleBucket("s1", "claim", "screen", small)) ||
		!strings.Contains(fmt.Sprint(hist), "not_sampled") {
		t.Fatalf("sampling audit: %v", sampling)
	}
	status, view := h.call("GET", "/claims/"+big, screener, nil)
	if status != 200 || dig(view, "case", "stage") != "screen" || !strings.HasPrefix(fmt.Sprint(dig(view, "review", "sampling", "reason")), "always reviewed") {
		t.Fatalf("big claim sampled: %d %v", status, dig(view, "review"))
	}
	if status, body := h.call("POST", "/claims/"+big+"/stages/screen/actions/ok", screener, map[string]any{}); status != 200 {
		t.Fatalf("screen: %d %v", status, body)
	}

	// Triage: the work list puts the urgent claim first, and filters by queue.
	status, list := h.call("GET", "/claims?scope=queue", s1, nil)
	rows, _ := list.([]any)
	if status != 200 || len(rows) != 2 || dig(rows, 0, "id") != big || dig(rows, 0, "priority") != float64(1) || dig(rows, 1, "queue") != "standard" {
		t.Fatalf("triaged queue: %d %v", status, list)
	}
	if _, list := h.call("GET", "/claims?scope=queue&order=newest", s1, nil); dig(list, 0, "id") != big {
		t.Fatalf("newest first: %v", list)
	}
	if _, list := h.call("GET", "/claims?scope=queue&queue=standard", s1, nil); len(list.([]any)) != 1 || dig(list, 0, "id") != small {
		t.Fatalf("queue filter: %v", list)
	}

	// Diff: the first submission is all new (sensitive values masked); after
	// a correction the reviewer sees only what changed.
	status, view = h.call("GET", "/claims/"+big, s1, nil)
	if status != 200 || dig(view, "review", "diff", "first") != true || len(dig(view, "review", "diff", "changes").([]any)) != 3 ||
		!strings.Contains(fmt.Sprint(dig(view, "review", "diff", "changes")), "••••") || strings.Contains(fmt.Sprint(view), "NP12345678901234") {
		t.Fatalf("first diff: %d %v", status, dig(view, "review"))
	}
	if status, body := h.call("POST", "/claims/"+big+"/stages/assess/actions/return", officer, map[string]any{"flags": map[string]any{"claim.title": "be specific"}}); status != 200 {
		t.Fatalf("return: %d %v", status, body)
	}
	if status, body := h.call("POST", "/claims/"+big+"/stages/apply/actions/submit", u1, map[string]any{"data": map[string]any{"claim": map[string]any{"title": "river flood"}}}); status != 200 {
		t.Fatalf("resubmit: %d %v", status, body)
	}
	_, view = h.call("GET", "/claims/"+big, s1, nil)
	changes := dig(view, "review", "diff", "changes").([]any)
	if len(changes) != 1 || dig(changes, 0, "path") != "claim.title" || dig(changes, 0, "before") != "flood" || dig(changes, 0, "after") != "river flood" || dig(view, "review", "diff", "base_revision") == nil {
		t.Fatalf("diff after correction: %v", dig(view, "review", "diff"))
	}

	// Gate: two distinct seniors must approve; a rejection is recorded (and
	// notifies the applicant at critical severity, through quiet hours).
	if status, body := h.call("POST", "/claims/"+big+"/stages/assess/actions/accept", officer, map[string]any{}); status != 409 || !strings.Contains(fmt.Sprint(body), "gated") {
		t.Fatalf("gated accept: %d %v", status, body)
	}
	if status, _ := h.call("POST", "/claims/"+big+"/stages/assess/nodes/gate/approve", officer, map[string]any{}); status != 403 {
		t.Fatalf("officer approves gate: %d", status)
	}
	if status, _ := h.call("POST", "/claims/"+big+"/stages/assess/nodes/gate/reject", s1, map[string]any{}); status != 422 {
		t.Fatalf("reject without reason: %d", status)
	}
	if status, body := h.call("POST", "/claims/"+big+"/stages/assess/nodes/gate/reject", s1, map[string]any{"comment": "photos missing"}); status != 200 {
		t.Fatalf("reject: %d %v", status, body)
	}
	for _, tok := range []string{s2, s3} {
		if status, body := h.call("POST", "/claims/"+big+"/stages/assess/nodes/gate/approve", tok, map[string]any{}); status != 200 {
			t.Fatalf("approve: %d %v", status, body)
		}
	}
	_, view = h.call("GET", "/claims/"+big, officer, nil)
	if dig(view, "review", "gate", "open") != true || dig(view, "review", "gate", "rejections") != float64(1) || dig(view, "review", "gate", "approvals") != float64(2) {
		t.Fatalf("gate: %v", dig(view, "review", "gate"))
	}
	reviewed := dig(view, "case", "revision")
	if status, body := h.call("POST", "/claims/"+big+"/stages/assess/actions/accept", officer, map[string]any{}); status != 200 || dig(body, "case", "stage") != "done" {
		t.Fatalf("accept: %d %v", status, body)
	}
	_, hist = h.call("GET", "/claims/"+big+"/history", officer, nil)
	if dig(hist, "stages", "assess", "review", "reviewed_revision") != reviewed {
		t.Fatalf("reviewed revision %v: %v", reviewed, dig(hist, "stages", "assess", "review"))
	}

	// A staff note is info: deferred to the end of u1's quiet hours.
	if status, body := h.call("POST", "/claims/"+small+"/notes", officer, map[string]any{"body": "we called you"}); status != 200 {
		t.Fatalf("note: %d %v", status, body)
	}
	mailTo := func(user string) []any {
		_, rows := h.call("GET", "/mail", u1, nil)
		var out []any
		for _, r := range rows.([]any) {
			if dig(r, "user_id") == user {
				out = append(out, r)
			}
		}
		return out
	}
	var note any
	waitFor(t, "u1's note to be planned", func() bool {
		_, body := h.call("GET", "/me/notifications", u1, nil)
		pending, _ := dig(body, "pending").([]any)
		for _, p := range pending {
			if dig(p, "event") == "note.added" {
				note = p
			}
		}
		return note != nil
	})
	if dig(note, "deferred") != "quiet_hours" {
		t.Fatalf("deferred note: %v", note)
	}
	quietEnd, _ := time.Parse(time.RFC3339, fmt.Sprint(dig(note, "deliver_at")))
	if d := time.Until(quietEnd); d < 50*time.Minute || d > 61*time.Minute {
		t.Fatalf("deferred until %v (in %v)", quietEnd, d)
	}
	waitFor(t, "the critical rejection", func() bool { return len(mailTo("u1")) == 1 })
	if m := mailTo("u1")[0]; dig(m, "severity") != "critical" || !strings.Contains(fmt.Sprint(dig(m, "subject")), "rejected") || !isFalse(dig(m, "digest")) {
		t.Fatalf("critical mail: %v", m)
	}

	// New-work mail: s3 immediately (small, big and big's resubmission), s2
	// opted out, s1 waits for the digest.
	waitFor(t, "s3's mail", func() bool { return len(mailTo("s3")) == 3 })
	if len(mailTo("s2")) != 0 || len(mailTo("s1")) != 0 {
		t.Fatalf("s2 %v, s1 %v", mailTo("s2"), mailTo("s1"))
	}
	_, body = h.call("GET", "/me/notifications", s1, nil)
	digest, _ := dig(body, "pending").([]any)
	if len(digest) != 3 || dig(digest, 0, "digest") != true || dig(digest, 0, "deliver_at") != dig(digest, 2, "deliver_at") {
		t.Fatalf("s1 digest: %v", body)
	}

	// Four hours on: quiet hours are over and the digest window has closed.
	cases.skew.Store(int64(4 * time.Hour))
	waitFor(t, "the digest and the deferred note", func() bool { return len(mailTo("s1")) == 1 && len(mailTo("u1")) == 2 })
	if m := mailTo("s1")[0]; isFalse(dig(m, "digest")) || dig(m, "n") != float64(3) || dig(m, "subject") != "3 notifications" {
		t.Fatalf("digest mail: %v", m)
	}
	if !strings.Contains(fmt.Sprint(mailTo("u1")), "A note on") {
		t.Fatalf("deferred mail: %v", mailTo("u1"))
	}
	if _, body := h.call("GET", "/me/notifications", s1, nil); len(dig(body, "pending").([]any)) != 0 {
		t.Fatalf("pending after delivery: %v", body)
	}
}

func TestValidateChecksNotifyChannels(t *testing.T) {
	bad := strings.Replace(reviewApp, `      sms "send_mail"
`, "", 1)
	bad = strings.Replace(bad, `    channels ["email"]
    subject "New work: {case.number}"`, `    channels ["sms"]
    subject "New work: {case.number}"`, 1)
	bad = strings.Replace(bad, `email "send_mail"`, `email "no_such_intent"`, 1)
	r := Validate(t.Context(), []byte(bad), t.TempDir(), DefaultLoadOptions())
	joined := strings.Join(r.Errors, "\n")
	if r.Valid || !strings.Contains(joined, `channel "sms"`) || !strings.Contains(joined, "no_such_intent") {
		t.Fatalf("errors: %v", r.Errors)
	}
	bad = strings.Replace(reviewApp, `      percent 0`, `      percent 140`, 1)
	if r := Validate(t.Context(), []byte(bad), t.TempDir(), DefaultLoadOptions()); r.Valid || !strings.Contains(strings.Join(r.Errors, " "), "percent") {
		t.Fatalf("sampling percent: %v", r.Errors)
	}
}

// isFalse reads a boolean column the way either driver returns it.
func isFalse(v any) bool { return v == false || v == float64(0) || v == int64(0) }
