package sms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"time"
	"unicode"

	smsg "github.com/oarkflow/smppflow/pkg/message"

	"github.com/oarkflow/ref/examples/smsgateway/internal/gateway"
)

// FaultKind classifies a pipeline failure so the REF layer can answer with the
// right status without this package knowing about HTTP.
type FaultKind int

const (
	FaultInvalid FaultKind = iota
	FaultAuth
	FaultForbidden
	FaultFunds
	FaultLimit
	FaultNotFound
	FaultConflict
	FaultUnavailable
)

// Fault is a classified failure.
type Fault struct {
	Kind FaultKind
	Code string
	Msg  string
	// Meta carries structured detail, such as the rejection reasons of a route.
	Meta map[string]any
}

func (f *Fault) Error() string { return f.Msg }

func fault(kind FaultKind, code, format string, args ...any) *Fault {
	return &Fault{Kind: kind, Code: code, Msg: fmt.Sprintf(format, args...)}
}

// ---------------------------------------------------------------------------
// Stage 1: user validation
// ---------------------------------------------------------------------------

// ValidateUser checks that the caller is an active account within its limits.
func (h *Hub) ValidateUser(ctx context.Context, userID string) (User, error) {
	u, ok := h.Dir.User(userID)
	if !ok {
		return u, fault(FaultAuth, "UNKNOWN_USER", "the account does not exist")
	}
	if u.Status != UserActive {
		return u, fault(FaultForbidden, "ACCOUNT_SUSPENDED", "the account is %s", u.Status)
	}
	if !h.userAllowed(u) {
		return u, fault(FaultLimit, "RATE_LIMITED", "more than %d messages per second", u.RatePerSecond)
	}
	if u.DailyLimit > 0 {
		n, err := h.Store.CountToday(ctx, u.ID)
		if err != nil {
			return u, fault(FaultUnavailable, "STORE_UNAVAILABLE", "cannot check the daily limit: %v", err)
		}
		if n >= u.DailyLimit {
			return u, fault(FaultLimit, "DAILY_LIMIT", "the daily limit of %d messages is reached", u.DailyLimit)
		}
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// Stage 2: data check
// ---------------------------------------------------------------------------

// SendRequest is what the API accepts.
type SendRequest struct {
	To   string `json:"to"`
	From string `json:"from"`
	Text string `json:"text"`
	// Type is otp, transactional (default) or promotional.
	Type string `json:"type"`
	// Reference makes the request idempotent: sending it again returns the first
	// message and charges nothing.
	Reference string `json:"reference"`
	// DLR requests a delivery receipt (default true).
	DLR *bool `json:"dlr"`
	// ScheduleAt delays dispatch until an RFC 3339 instant.
	ScheduleAt string `json:"schedule_at"`
	// ExpiresIn is the number of seconds after which an unsent message is
	// dropped (default and maximum: the hub's TTL).
	ExpiresIn int               `json:"expires_in"`
	Meta      map[string]string `json:"meta"`
}

// Draft is a validated, normalised message ready to be routed and paid for.
type Draft struct {
	ID      string
	To      string
	Country string
	From    string
	// SenderCountry is the country of a numeric sender id, when it is one.
	SenderCountry string
	Text          string
	Encoding      string
	Segments      int
	Unicode       bool
	Type          string
	WantDLR       bool
	Reference     string
	PayloadHash   string
	ScheduleAt    time.Time
	ExpiresAt     time.Time
	Meta          map[string]string
}

var (
	typePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	alphaSender   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,10}$`)
	numericSender = regexp.MustCompile(`^\+?[0-9]{3,15}$`)
	refPattern    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)
)

// CheckData validates and normalises a request for u.
func (h *Hub) CheckData(ctx context.Context, u User, in SendRequest) (*Draft, error) {
	d := &Draft{Meta: in.Meta}

	number, country, err := NormalizeNumber(in.To, h.Cfg.DefaultCountry)
	if err != nil {
		return nil, fault(FaultInvalid, "INVALID_NUMBER", "%v", strings.TrimPrefix(err.Error(), ErrNumber.Error()+": "))
	}
	d.To, d.Country = number, country
	if len(u.Countries) > 0 && !containsFold(u.Countries, country) {
		return nil, fault(FaultForbidden, "COUNTRY_NOT_ALLOWED", "the account may not send to %s", country)
	}

	text := in.Text
	if strings.TrimSpace(text) == "" {
		return nil, fault(FaultInvalid, "EMPTY_TEXT", "the message text is empty")
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return nil, fault(FaultInvalid, "INVALID_TEXT", "the message text contains a control character")
		}
	}
	an := smsg.AnalyzeText(text)
	if an.Parts > h.Cfg.MaxSegments {
		return nil, fault(FaultInvalid, "TEXT_TOO_LONG", "the message needs %d segments; the limit is %d", an.Parts, h.Cfg.MaxSegments)
	}
	d.Text, d.Encoding, d.Segments = text, string(an.Encoding), max(an.Parts, 1)
	d.Unicode = an.ContainsUnicode || strings.EqualFold(d.Encoding, "ucs2")

	d.From = strings.TrimSpace(in.From)
	if d.From == "" {
		d.From = u.DefaultSender
	}
	if d.From == "" {
		d.From = h.Cfg.DefaultSender
	}
	if !alphaSender.MatchString(d.From) && !numericSender.MatchString(d.From) {
		return nil, fault(FaultInvalid, "INVALID_SENDER", "the sender id %q is not valid: use up to 11 letters or digits, or a phone number", d.From)
	}
	if len(u.Senders) > 0 && !containsFold(u.Senders, d.From) {
		return nil, fault(FaultForbidden, "SENDER_NOT_ALLOWED", "the account may not send from %q", d.From)
	}
	if numericSender.MatchString(d.From) {
		// A numeric sender id is a phone number, and where it is registered
		// decides which providers may send from it.
		if _, c, err := NormalizeNumber(d.From, h.Cfg.DefaultCountry); err == nil {
			d.SenderCountry = c
		}
	}

	d.Type = strings.ToLower(strings.TrimSpace(in.Type))
	if d.Type == "" {
		d.Type = TypeTransactional
	}
	if !typePattern.MatchString(d.Type) {
		return nil, fault(FaultInvalid, "INVALID_TYPE", "the message type must be lowercase letters, digits and underscores")
	}
	d.WantDLR = in.DLR == nil || *in.DLR

	d.Reference = strings.TrimSpace(in.Reference)
	if d.Reference != "" && !refPattern.MatchString(d.Reference) {
		return nil, fault(FaultInvalid, "INVALID_REFERENCE", "the reference may contain letters, digits and . _ : - (100 at most)")
	}

	now := h.now().UTC()
	ttl := h.Cfg.TTL.D()
	if in.ExpiresIn > 0 && time.Duration(in.ExpiresIn)*time.Second < ttl {
		ttl = time.Duration(in.ExpiresIn) * time.Second
	}
	if in.ScheduleAt != "" {
		at, err := time.Parse(time.RFC3339, in.ScheduleAt)
		if err != nil {
			return nil, fault(FaultInvalid, "INVALID_SCHEDULE", "schedule_at must be an RFC 3339 time")
		}
		if at.After(now.Add(7 * 24 * time.Hour)) {
			return nil, fault(FaultInvalid, "INVALID_SCHEDULE", "a message cannot be scheduled more than 7 days ahead")
		}
		if at.After(now) {
			d.ScheduleAt = at.UTC()
		}
	}
	d.ExpiresAt = now.Add(ttl)
	if !d.ScheduleAt.IsZero() {
		d.ExpiresAt = d.ScheduleAt.Add(ttl)
	}

	if blocked, reason, err := h.Store.OptedOut(ctx, u.ID, d.To); err != nil {
		return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "cannot check opt-outs: %v", err)
	} else if blocked {
		return nil, fault(FaultForbidden, "OPTED_OUT", "the recipient has opted out%s", suffix(reason))
	}

	sum := sha256.Sum256([]byte(strings.Join([]string{d.To, d.From, d.Text, d.Type, fmt.Sprint(d.WantDLR), in.ScheduleAt}, "\x00")))
	d.PayloadHash = hex.EncodeToString(sum[:])
	d.ID = newID("msg_")
	return d, nil
}

func suffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Stage 3: routing
// ---------------------------------------------------------------------------

// PlanRoute chooses the provider chain for a draft.
func (h *Hub) PlanRoute(ctx context.Context, u User, d *Draft) (Explanation, error) {
	ex, err := h.Router.Plan(ctx, Request{
		UserID: u.ID, Tenant: u.Tenant, From: d.From, SenderCountry: d.SenderCountry, To: d.To, Country: d.Country, Type: d.Type,
		Segments: d.Segments, Unicode: d.Unicode, WantDLR: d.WantDLR, Objective: u.Objective,
	})
	if err != nil {
		var nr *ErrNoRoute
		if errors.As(err, &nr) {
			f := fault(FaultInvalid, "NO_ROUTE", "%v", nr)
			if nr.Transient {
				f = fault(FaultUnavailable, "NO_ROUTE", "%v", nr)
			}
			f.Meta = map[string]any{"reasons": nr.Reasons}
			return ex, f
		}
		return ex, err
	}
	return ex, nil
}

// ---------------------------------------------------------------------------
// Stage 4: payment (price, hold, persist)
// ---------------------------------------------------------------------------

// Price returns what the user pays for a draft.
func (h *Hub) Price(u User, d *Draft) (int64, error) {
	per, err := h.Dir.Price(u.ID, d.Country, d.Type)
	if err != nil {
		return 0, fault(FaultInvalid, "NO_PRICE", "%v", err)
	}
	total := per * int64(d.Segments)
	if u.MaxPrice > 0 && total > FromUnits(u.MaxPrice) {
		return 0, fault(FaultForbidden, "PRICE_LIMIT", "the message costs %.4f, above the account's limit of %.4f", ToUnits(total), u.MaxPrice)
	}
	return total, nil
}

// Accept stores the message and holds its price in one transaction. Funds are
// reserved, not spent: they are captured when a provider accepts the message
// and released if it ultimately fails. Accepting the same reference twice
// returns the first message and holds nothing more.
func (h *Hub) Accept(ctx context.Context, u User, d *Draft, ex Explanation) (AcceptResult, error) {
	price, err := h.Price(u, d)
	if err != nil {
		return AcceptResult{}, err
	}
	m := &Message{
		NextRunAt: d.ScheduleAt,
		ID:        d.ID, UserID: u.ID, Tenant: u.Tenant, IdempotencyKey: d.Reference, PayloadHash: d.PayloadHash,
		From: d.From, To: d.To, Country: d.Country, Text: d.Text, Encoding: d.Encoding, Segments: d.Segments,
		Type: d.Type, WantDLR: d.WantDLR, Plan: ex.Plan, PriceMicros: price, Currency: u.Currency,
		ExpiresAt: d.ExpiresAt, Meta: d.Meta,
	}
	res, err := h.Store.Accept(ctx, m)
	switch {
	case errors.Is(err, ErrInsufficientFunds):
		bal, _ := h.Store.Balance(ctx, u.ID)
		f := fault(FaultFunds, "INSUFFICIENT_FUNDS", "the message costs %.4f and the available balance is %.4f", ToUnits(price), ToUnits(bal.Available))
		f.Meta = map[string]any{"price": ToUnits(price), "available": ToUnits(bal.Available), "currency": u.Currency}
		return AcceptResult{}, f
	case errors.Is(err, ErrIdempotency):
		return AcceptResult{}, fault(FaultConflict, "REFERENCE_REUSED", "the reference %q was already used with a different message", d.Reference)
	case err != nil:
		return AcceptResult{}, fault(FaultUnavailable, "STORE_UNAVAILABLE", "cannot accept the message: %v", err)
	}
	if res.Duplicate {
		h.Counters.Duplicates.Add(1)
	} else {
		// A scheduled message carries its due time as next_run, so the recovery
		// sweep leaves it alone until it is due.
		h.Counters.Accepted.Add(1)
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Stage 5: dispatch (publish the first job)
// ---------------------------------------------------------------------------

// Enqueue publishes the dispatch job of a freshly accepted message. A message
// that was already accepted (a duplicate request) is not published again: its
// job exists, or the recovery sweep will republish it.
func (h *Hub) Enqueue(ctx context.Context, res AcceptResult) error {
	if res.Duplicate {
		return nil
	}
	m := res.Message
	if len(m.Plan) == 0 {
		return fault(FaultInvalid, "NO_ROUTE", "the message has no route")
	}
	delay := time.Until(m.NextRunAt)
	if err := h.publishJob(m.ID, m.Plan[0].Provider, m.DispatchSeq, delay); err != nil {
		// The message is stored and paid for; the recovery sweep will publish
		// its job. Accepting the request is still correct.
		h.Log.Warn("cannot publish dispatch job; recovery will retry", "message", m.ID, "error", err)
	}
	return nil
}

func (h *Hub) publishJob(id, provider string, seq int64, delay time.Duration) error {
	job := DispatchJob{MessageID: id, Provider: provider, Seq: seq}
	if delay > 0 {
		_, err := h.queue.EnqueueAfter(JobDispatchPrefix+provider, job, delay)
		return err
	}
	_, err := h.queue.Enqueue(JobDispatchPrefix+provider, job)
	return err
}

// ---------------------------------------------------------------------------
// Worker side: claim, send, settle
// ---------------------------------------------------------------------------

// JobState is a claimed dispatch job.
type JobState struct {
	Job     DispatchJob
	Message *Message
	// Skip is set when there is nothing to do: the job is stale, or the message
	// is already handled.
	Skip   bool
	Reason string
}

// LoadJob parses a dispatch job and claims its message.
func (h *Hub) LoadJob(ctx context.Context, payload []byte) (*JobState, error) {
	var job DispatchJob
	if err := json.Unmarshal(payload, &job); err != nil || job.MessageID == "" {
		// A job nobody can read can never succeed. Dropping it is the only
		// useful answer; retrying would just fill the dead-letter queue.
		return &JobState{Skip: true, Reason: "unreadable_job"}, nil
	}
	js := &JobState{Job: job}
	m, err := h.Store.Claim(ctx, job.MessageID, job.Seq, h.Cfg.Pipeline.ClaimLease.D())
	switch {
	case errors.Is(err, ErrNotFound):
		js.Skip, js.Reason = true, "unknown_message"
		return js, nil
	case errors.Is(err, ErrStale):
		h.Counters.StaleJobs.Add(1)
		js.Message, js.Skip, js.Reason = m, true, "stale_job"
		return js, nil
	case errors.Is(err, ErrBusy):
		return nil, fault(FaultUnavailable, "BUSY", "message %s is being dispatched by another worker", job.MessageID)
	case err != nil:
		return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "cannot claim message: %v", err)
	}
	js.Message = m
	if !m.ExpiresAt.IsZero() && h.now().After(m.ExpiresAt) {
		if err := h.Store.Fail(ctx, m.ID, job.Seq, "expired", "the message expired before it could be sent", !h.Cfg.KeepText); err != nil && !errors.Is(err, ErrStale) {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		h.Counters.Failed.Add(1)
		js.Skip, js.Reason = true, "expired"
	}
	return js, nil
}

// SendOutcome is the result of one send attempt.
type SendOutcome struct {
	OK            bool
	ProviderMsgID string
	Latency       time.Duration
	Class         gateway.Class
	Code          string
	Err           string
	RetryAfter    time.Duration
	// Skipped is true when no send was attempted (circuit open, provider
	// missing): the pipeline fails over without charging an attempt.
	Skipped bool
}

// ProviderSend hands the claimed message to its provider's gateway.
func (h *Hub) ProviderSend(ctx context.Context, js *JobState) (*SendOutcome, error) {
	if js.Skip {
		return &SendOutcome{Skipped: true, Code: js.Reason}, nil
	}
	m := js.Message
	if m.PlanIndex >= len(m.Plan) {
		return &SendOutcome{Class: gateway.ClassProvider, Code: "plan_exhausted", Err: "no provider left in the plan", Skipped: true}, nil
	}
	name := m.Plan[m.PlanIndex].Provider
	p, ok := h.provider(name)
	if !ok || p.Cfg.Disabled {
		return &SendOutcome{Class: gateway.ClassProvider, Code: "provider_unavailable", Err: "provider " + name + " is not available", Skipped: true}, nil
	}
	if !h.Router.Allow(name, m.Country, m.Type) {
		return &SendOutcome{Class: gateway.ClassProvider, Code: "circuit_open", Err: "the circuit for " + name + " is open", Skipped: true}, nil
	}
	if wait := p.limiter.reserve(); wait > 0 {
		if wait > 2*time.Second {
			p.Throttled.Add(1)
			return &SendOutcome{Class: gateway.ClassThrottled, Code: "tps_limit", Err: "provider rate limit", RetryAfter: wait}, nil
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	sctx, cancel := context.WithTimeout(ctx, p.Cfg.Timeout.D())
	defer cancel()
	start := time.Now()
	rec, err := p.GW.Send(sctx, gateway.Message{
		ID: m.ID, From: m.From, To: m.To, Text: m.Text, Segments: m.Segments, Encoding: m.Encoding,
		MessageType: m.Type, Country: m.Country, WantDLR: m.WantDLR, ExpiresAt: m.ExpiresAt, Attempt: m.Attempt, Meta: m.Meta,
	})
	latency := time.Since(start)
	req := Request{Country: m.Country, Type: m.Type}
	if err == nil {
		p.Sent.Add(1)
		p.note(true, nil)
		h.Router.Feedback(name, req, true, latency)
		return &SendOutcome{OK: true, ProviderMsgID: rec.ProviderMessageID, Latency: latency}, nil
	}
	class, ge := gateway.ClassOf(err)
	out := &SendOutcome{Class: class, Code: ge.Code, Err: ge.Error(), RetryAfter: ge.RetryAfter, Latency: latency}
	switch class {
	case gateway.ClassThrottled:
		p.Throttled.Add(1)
	case gateway.ClassPermanent:
		// The destination is at fault, not the provider.
	default:
		p.Failed.Add(1)
		p.note(false, err)
		h.Router.Feedback(name, req, false, latency)
	}
	return out, nil
}

// Outcome is what settling a job decided.
type Outcome struct {
	MessageID string       `json:"message_id"`
	Status    string       `json:"status"` // submitted, retry, failover, failed, skipped
	State     MessageState `json:"state"`
	Provider  string       `json:"provider,omitempty"`
	Reason    string       `json:"reason,omitempty"`
}

// maxTotalAttempts bounds the sends of one message across every provider, so a
// pathological plan cannot loop.
const maxTotalAttempts = 12

// Settle applies the result of a send: accept it, retry on the same provider,
// fail over to the next one, or fail the message and release its funds.
//
// Every branch is a compare-and-set on the job's sequence number, so a worker
// that lost its claim cannot undo or repeat what the new owner did.
func (h *Hub) Settle(ctx context.Context, js *JobState, out *SendOutcome) (*Outcome, error) {
	if js.Skip {
		id := js.Job.MessageID
		st := MessageState("")
		if js.Message != nil {
			id, st = js.Message.ID, js.Message.State
		}
		return &Outcome{MessageID: id, Status: "skipped", State: st, Reason: js.Reason}, nil
	}
	m, seq := js.Message, js.Job.Seq
	name := m.Plan[min(m.PlanIndex, len(m.Plan)-1)].Provider
	res := &Outcome{MessageID: m.ID, Provider: name}
	attempt := AttemptRecord{MessageID: m.ID, N: m.TotalAttempts, Provider: name, Code: out.Code, Error: out.Err,
		LatencyMs: out.Latency.Milliseconds(), At: h.now()}
	record := func(outcome string) {
		attempt.Outcome = outcome
		if err := h.Store.RecordAttempt(ctx, attempt); err != nil {
			h.Log.Warn("cannot record attempt", "message", m.ID, "error", err)
		}
	}

	if out.OK {
		err := h.Store.Submitted(ctx, m, seq, out.ProviderMsgID, h.Cfg.CaptureOn == "submit", !h.Cfg.KeepText)
		if errors.Is(err, ErrStale) {
			// Our claim lapsed and another worker finished the message. The
			// send happened; the money is untouched by us.
			h.Counters.StaleJobs.Add(1)
			res.Status, res.Reason = "skipped", "claim_lost"
			return res, nil
		}
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "cannot record the submission: %v", err)
		}
		h.Counters.Submitted.Add(1)
		record("accepted")
		res.Status, res.State = "submitted", StateSubmitted
		return res, nil
	}

	pcfg, _ := h.Dir.Provider(name)
	retry := pcfg.Config.Retry.withDefaults()
	reqInfo := fmt.Sprintf("%s: %s", out.Code, out.Err)

	switch {
	case out.Class == gateway.ClassPermanent:
		return h.failMessage(ctx, m, seq, res, out.Code, out.Err, record)

	case out.Class == gateway.ClassThrottled && m.TotalAttempts < maxTotalAttempts:
		// Backpressure, not failure: wait and retry the same provider without
		// spending one of its attempts.
		delay := max(out.RetryAfter, retry.Delay(m.Attempt))
		return h.reschedule(ctx, m, seq, res, m.PlanIndex, name, max(m.Attempt-1, 0), delay, out, "retry", record)

	case (out.Class == gateway.ClassRetryable || out.Class == gateway.ClassThrottled) && m.Attempt < retry.MaxAttempts && m.TotalAttempts < maxTotalAttempts:
		delay := jitter(retry.Delay(m.Attempt), retry.Jitter)
		if out.RetryAfter > delay {
			delay = out.RetryAfter
		}
		return h.reschedule(ctx, m, seq, res, m.PlanIndex, name, m.Attempt, delay, out, "retry", record)
	}

	// Retries on this provider are over: fail over to the next provider.
	next, ok := h.nextProvider(m)
	if !ok || m.TotalAttempts >= maxTotalAttempts {
		code := "all_providers_failed"
		return h.failMessage(ctx, m, seq, res, code, "every provider in the route failed; last error: "+reqInfo, record)
	}
	h.Counters.Failovers.Add(1)
	return h.reschedule(ctx, m, seq, res, next, m.Plan[next].Provider, 0, 0, out, "failover", record)
}

func (h *Hub) failMessage(ctx context.Context, m *Message, seq int64, res *Outcome, code, errText string, record func(string)) (*Outcome, error) {
	err := h.Store.Fail(ctx, m.ID, seq, code, errText, !h.Cfg.KeepText)
	if errors.Is(err, ErrStale) {
		res.Status, res.Reason = "skipped", "claim_lost"
		return res, nil
	}
	if err != nil {
		return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "cannot fail the message: %v", err)
	}
	h.Counters.Failed.Add(1)
	record("failed")
	res.Status, res.State, res.Reason = "failed", StateFailed, code
	return res, nil
}

// reschedule moves the message to its next attempt and publishes that attempt's
// job. The state change is committed first; if publishing then fails, the
// message is queued past its run time and the recovery sweep publishes it.
func (h *Hub) reschedule(ctx context.Context, m *Message, seq int64, res *Outcome, planIndex int, provider string, attempt int, delay time.Duration, out *SendOutcome, kind string, record func(string)) (*Outcome, error) {
	runAt := h.now().Add(delay)
	if !m.ExpiresAt.IsZero() && runAt.After(m.ExpiresAt) {
		return h.failMessage(ctx, m, seq, res, "expired", "the message would expire before its next attempt; last error: "+out.Err, record)
	}
	newSeq, err := h.Store.Reschedule(ctx, m.ID, seq, planIndex, provider, attempt, runAt, out.Code, truncate(out.Err, 300))
	if errors.Is(err, ErrStale) {
		res.Status, res.Reason = "skipped", "claim_lost"
		return res, nil
	}
	if err != nil {
		return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "cannot reschedule: %v", err)
	}
	if kind == "retry" {
		h.Counters.Retries.Add(1)
	}
	record(kind)
	if err := h.publishJob(m.ID, provider, newSeq, delay); err != nil {
		h.Log.Warn("cannot publish retry job; recovery will retry", "message", m.ID, "error", err)
	}
	res.Status, res.State, res.Provider, res.Reason = kind, StateQueued, provider, out.Code
	return res, nil
}

// nextProvider finds the next usable provider after the current plan index.
func (h *Hub) nextProvider(m *Message) (int, bool) {
	for i := m.PlanIndex + 1; i < len(m.Plan); i++ {
		p, ok := h.provider(m.Plan[i].Provider)
		if !ok || p.Cfg.Disabled {
			continue
		}
		return i, true
	}
	return 0, false
}

func jitter(d time.Duration, frac float64) time.Duration {
	if frac <= 0 || d <= 0 {
		return d
	}
	spread := float64(d) * frac
	return d + time.Duration((rand.Float64()*2-1)*spread)
}

// ---------------------------------------------------------------------------
// Delivery receipts
// ---------------------------------------------------------------------------

// EnqueueDLR queues a delivery receipt for processing.
func (h *Hub) EnqueueDLR(d gateway.DLR) error {
	_, err := h.queue.Enqueue(JobDLR, d)
	return err
}

// ApplyDLR applies a queued delivery receipt.
func (h *Hub) ApplyDLR(ctx context.Context, payload []byte) (*Outcome, error) {
	var d gateway.DLR
	if err := json.Unmarshal(payload, &d); err != nil {
		return &Outcome{Status: "skipped", Reason: "unreadable_receipt"}, nil
	}
	if !d.Status.Final() {
		return &Outcome{Status: "skipped", Reason: "interim_receipt"}, nil
	}
	var (
		m   *Message
		err error
	)
	if d.MessageID != "" {
		m, err = h.Store.Get(ctx, d.MessageID)
	} else {
		m, err = h.Store.FindByProviderID(ctx, d.Provider, d.ProviderMessageID)
	}
	if errors.Is(err, ErrNotFound) {
		// The receipt can outrun the commit of the submission it describes.
		// Failing the job retries it with backoff.
		return nil, fault(FaultUnavailable, "UNKNOWN_MESSAGE", "no message matches the receipt yet")
	}
	if err != nil {
		return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
	}
	delivered := d.Status == gateway.StatusDelivered
	res, err := h.Store.ApplyDLR(ctx, m.ID, delivered, d.Code, h.Cfg.CaptureOn == "delivered", h.Cfg.RefundOnDLRFailure, !h.Cfg.KeepText)
	if errors.Is(err, ErrNotReady) {
		return nil, fault(FaultUnavailable, "NOT_READY", "message %s is not submitted yet", m.ID)
	}
	if err != nil {
		return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
	}
	out := &Outcome{MessageID: m.ID, State: res.Message.State, Provider: m.Provider}
	if !res.Changed {
		out.Status, out.Reason = "skipped", "duplicate_receipt"
		return out, nil
	}
	if delivered {
		h.Counters.Delivered.Add(1)
		out.Status = "delivered"
	} else {
		h.Counters.Failed.Add(1)
		h.Router.Feedback(m.Provider, Request{Country: m.Country, Type: m.Type}, false, 0)
		out.Status, out.Reason = "failed", d.Code
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Recovery
// ---------------------------------------------------------------------------

func (h *Hub) recoveryLoop() {
	defer h.wg.Done()
	t := time.NewTicker(h.Cfg.Recovery.Interval.D())
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
			if n, err := h.Recover(context.Background()); err != nil {
				h.Log.Warn("recovery sweep failed", "error", err)
			} else if n > 0 {
				h.Log.Info("recovery republished dispatch jobs", "count", n)
			}
		}
	}
}

// Recover republishes the dispatch job of every message whose job may have been
// lost: published to a queue that failed, or claimed by a worker that died. It
// is what makes delivery at-least-once without relying on the queue alone, and
// it is safe to run on every node: a job published twice is dropped as stale or
// refused by the claim.
func (h *Hub) Recover(ctx context.Context) (int, error) {
	stalled, err := h.Store.ListStalled(ctx, h.Cfg.Recovery.Grace.D(), h.Cfg.Recovery.Batch)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range stalled {
		if err := h.publishJob(s.ID, s.Provider, s.Seq, 0); err != nil {
			return n, err
		}
		_ = h.Store.TouchRun(ctx, s.ID, s.Seq, h.now().Add(h.Cfg.Recovery.Grace.D()))
		h.Counters.Recovered.Add(1)
		n++
	}
	return n, nil
}
