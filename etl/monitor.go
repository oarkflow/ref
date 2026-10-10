package etl

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Alert is something that needs a person's attention, worked out from the
// current state. It is not stored: it appears while the condition holds and
// goes away when it clears.
type Alert struct {
	ID       string    `json:"id"`
	Severity string    `json:"severity"` // critical, warning, info
	Kind     string    `json:"kind"`     // held, retrying, intake_failed, stuck, stale, rejects, paused, sweeper
	SourceID string    `json:"source_id,omitempty"`
	BatchID  string    `json:"batch_id,omitempty"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	Since    time.Time `json:"since"`
	// Acknowledged alerts stay listed but are silenced until AckedUntil.
	AckedBy    string    `json:"acked_by,omitempty"`
	AckedNote  string    `json:"acked_note,omitempty"`
	AckedUntil time.Time `json:"acked_until,omitempty"`
}

// SourceHealth is one source's last day at a glance.
type SourceHealth struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Owner       string    `json:"owner"`
	Destination string    `json:"destination"`
	Paused      bool      `json:"paused"`
	Freshness   string    `json:"freshness"` // ok, late, stale, quiet (no expectation), none (nothing yet)
	ExpectEvery Dur       `json:"expect_every"`
	LastBatchAt time.Time `json:"last_batch_at,omitempty"`
	Batches     int       `json:"batches"`
	RowsIn      int       `json:"rows_in"`
	Delivered   int       `json:"delivered"`
	Quarantined int       `json:"quarantined"`
	RejectRate  float64   `json:"reject_rate"`
	Held        int       `json:"held"`
	Retrying    int       `json:"retrying"`
	AvgSeconds  float64   `json:"avg_seconds"`
	P95Seconds  float64   `json:"p95_seconds"`
}

// StageStat is how one stage has behaved lately.
type StageStat struct {
	Stage    int     `json:"stage"`
	Name     string  `json:"name"`
	Runs     int     `json:"runs"`
	Failures int     `json:"failures"`
	AvgMs    float64 `json:"avg_ms"`
	P95Ms    float64 `json:"p95_ms"`
}

// Queue is how much work is waiting for the sweeper.
type Queue struct {
	Due            int     `json:"due"`
	OldestDueSecs  float64 `json:"oldest_due_seconds"`
	SweeperAgeSecs float64 `json:"sweeper_age_seconds"` // -1: no sweeper has run
}

// MonitorView is everything the monitoring screen shows.
type MonitorView struct {
	Since   time.Time      `json:"since"`
	Bucket  Dur            `json:"bucket"`
	Summary Summary        `json:"summary"`
	Series  []Point        `json:"series"`
	Sources []SourceHealth `json:"sources"`
	Stages  []StageStat    `json:"stages"`
	Alerts  []Alert        `json:"alerts"`
	Queue   Queue          `json:"queue"`
	// Circuits are the destinations whose breaker has seen failures.
	Circuits []Circuit `json:"circuits"`
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	i := int(float64(len(v)-1) * p)
	return v[i]
}

// Monitor builds the monitoring view for the last `window` (default 24h).
func (e *Engine) Monitor(ctx context.Context, actor Actor, window time.Duration) (*MonitorView, error) {
	acc, err := e.need(ctx, actor, PermMonitor, "")
	if err != nil {
		return nil, err
	}
	v, err := e.monitor(ctx, acc.Sources(PermMonitor), window)
	if err != nil {
		return nil, err
	}
	e.decorate(ctx, v.Alerts)
	return v, nil
}

// decorate adds the acknowledgement state of open alerts to the computed ones.
func (e *Engine) decorate(ctx context.Context, alerts []Alert) {
	open, err := e.Store.ListAlerts(ctx, true, 500)
	if err != nil {
		return
	}
	by := map[string]AlertRecord{}
	for _, r := range open {
		by[r.ID] = r
	}
	now := e.now()
	for i := range alerts {
		if r, ok := by[alerts[i].ID]; ok && r.Acknowledged(now) {
			alerts[i].AckedBy, alerts[i].AckedNote, alerts[i].AckedUntil = r.AckedBy, r.AckedNote, r.AckedUntil
		}
	}
}

func (e *Engine) monitor(ctx context.Context, scope []string, window time.Duration) (*MonitorView, error) {
	e.init()
	if window <= 0 {
		window = 24 * time.Hour
	}
	now := e.now()
	since := now.Add(-window)
	bucket := window / 48
	if bucket < time.Minute {
		bucket = time.Minute
	}
	v := &MonitorView{Since: since, Bucket: Dur(bucket), Series: []Point{}, Sources: []SourceHealth{}, Alerts: []Alert{}, Stages: []StageStat{}, Circuits: e.Circuits(ctx)}
	var err error
	if v.Summary, err = e.Store.Summary(ctx, scope); err != nil {
		return nil, err
	}
	if v.Series, err = e.Store.Series(ctx, since, bucket, scope); err != nil {
		return nil, err
	}
	sources, err := e.Store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := e.Store.SourceStats(ctx, since, scope)
	if err != nil {
		return nil, err
	}
	statBySource := map[string]SourceStat{}
	for _, st := range stats {
		statBySource[st.SourceID] = st
	}
	hb := e.heartbeat.Load()
	v.Queue.SweeperAgeSecs = -1
	if hb > 0 {
		v.Queue.SweeperAgeSecs = time.Since(time.Unix(0, hb)).Seconds()
	}
	if due, err := e.Store.Due(ctx, now, 1000); err == nil {
		for _, b := range due {
			if scope != nil && !contains(scope, b.SourceID) {
				continue
			}
			v.Queue.Due++
			if age := now.Sub(b.NextAttemptAt).Seconds(); age > v.Queue.OldestDueSecs && !b.NextAttemptAt.IsZero() {
				v.Queue.OldestDueSecs = age
			}
		}
	}

	// Stage totals are exact: they are summed from hourly counters kept in the
	// store. Percentiles come from the events of the most recent batches.
	type stageAgg struct {
		runs, fails, ms float64
		d               []float64
	}
	stages := map[int]*stageAgg{}
	agg := func(st int) *stageAgg {
		if stages[st] == nil {
			stages[st] = &stageAgg{}
		}
		return stages[st]
	}
	sinceHour := since.UTC().Format("2006010215")
	if rows, err := e.Store.Counters(ctx); err == nil {
		for _, r := range rows {
			if !strings.HasPrefix(r.Name, HourlyPrefix+"stage_") || labelHour(r.Labels) < sinceHour {
				continue
			}
			st, _ := strconv.Atoi(labelValue(r.Labels, "stage"))
			switch r.Name {
			case HourlyPrefix + "stage_runs":
				agg(st).runs += r.Value
			case HourlyPrefix + "stage_failures":
				agg(st).fails += r.Value
			case HourlyPrefix + "stage_ms":
				agg(st).ms += r.Value
			}
		}
	}
	recent, err := e.Store.ListBatches(ctx, Query{SourceIDs: scope, Limit: 300})
	if err != nil {
		return nil, err
	}
	for _, b := range recent {
		for _, ev := range b.Events {
			if ev.Attempt > 0 && !ev.At.Before(since) {
				agg(ev.Stage).d = append(agg(ev.Stage).d, float64(ev.DurationMs))
			}
		}
	}
	for st := 1; st <= 6; st++ {
		stat := StageStat{Stage: st, Name: StageNames[st]}
		if ag := stages[st]; ag != nil {
			stat.Runs, stat.Failures = int(ag.runs), int(ag.fails)
			if ag.runs > 0 {
				stat.AvgMs = ag.ms / ag.runs
			}
			stat.P95Ms = percentile(ag.d, .95)
		}
		v.Stages = append(v.Stages, stat)
	}

	for _, s := range sources {
		if scope != nil && !contains(scope, s.ID) {
			continue
		}
		st := statBySource[s.ID]
		h := SourceHealth{ID: s.ID, Name: s.Name, Owner: s.Owner, Destination: s.Destination, Paused: s.Paused, ExpectEvery: s.ExpectEvery,
			LastBatchAt: st.LastBatchAt, Batches: st.Batches, RowsIn: st.RowsIn, Delivered: st.Delivered, Quarantined: st.Quarantined,
			Held: st.Held, Retrying: st.Retrying, AvgSeconds: st.AvgSeconds}
		if h.RowsIn > 0 {
			h.RejectRate = float64(h.Quarantined) / float64(h.RowsIn)
		}
		if lat, err := e.Store.LatencySample(ctx, s.ID, since, 500); err == nil {
			h.P95Seconds = percentile(lat, .95)
		}
		h.Freshness = freshness(s, h.LastBatchAt, now)
		v.Sources = append(v.Sources, h)
	}
	// Alerts about batches look at the batches that need attention, not at
	// whatever was most recent.
	var problem []*Batch
	for _, status := range []string{StatusHeld, StatusRetrying, StatusFailed, StatusInFlight} {
		list, err := e.Store.ListBatches(ctx, Query{SourceIDs: scope, Statuses: []string{status}, Limit: 200})
		if err != nil {
			return nil, err
		}
		problem = append(problem, list...)
	}
	if a := e.alerts(sources, problem, v, scope, now); a != nil {
		v.Alerts = a
	}
	return v, nil
}

// labelValue reads one key="value" pair out of a counter's label string.
func labelValue(labels, key string) string {
	i := strings.Index(labels, key+`="`)
	if i < 0 {
		return ""
	}
	rest := labels[i+len(key)+2:]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

func freshness(s *Source, last, now time.Time) string {
	if s.ExpectEvery <= 0 {
		return "quiet"
	}
	ref := last
	if ref.IsZero() {
		ref = s.CreatedAt
	}
	age := now.Sub(ref)
	switch {
	case age <= time.Duration(s.ExpectEvery):
		return "ok"
	case age <= 2*time.Duration(s.ExpectEvery):
		return "late"
	}
	return "stale"
}

func (e *Engine) alerts(sources []*Source, batches []*Batch, v *MonitorView, scope []string, now time.Time) []Alert {
	var out []Alert
	add := func(a Alert) { a.ID = a.Kind + ":" + a.SourceID + ":" + a.BatchID; out = append(out, a) }
	stuckAfter := max(2*time.Minute, 10*e.StageDelay)
	for _, b := range batches {
		switch b.Status {
		case StatusHeld:
			f := Failure{}
			if n := len(b.Failures); n > 0 {
				f = b.Failures[n-1]
			}
			add(Alert{Severity: "critical", Kind: "held", SourceID: b.SourceID, BatchID: b.ID, Since: f.At,
				Title:   "Batch held, needs a replay",
				Message: fmt.Sprintf("%s stopped at stage %d (%s) after %d attempts: %s", b.ID, f.Stage, f.StageName, f.Attempt, b.LastError)})
		case StatusRetrying:
			add(Alert{Severity: "warning", Kind: "retrying", SourceID: b.SourceID, BatchID: b.ID, Since: b.UpdatedAt,
				Title:   "Batch retrying",
				Message: fmt.Sprintf("%s failed attempt %d and retries at %s: %s", b.ID, b.Attempts, b.NextAttemptAt.Format("15:04:05"), b.LastError)})
		case StatusFailed:
			if now.Sub(b.CreatedAt) < time.Hour {
				add(Alert{Severity: "warning", Kind: "intake_failed", SourceID: b.SourceID, BatchID: b.ID, Since: b.CreatedAt,
					Title: "Batch refused at intake", Message: b.LastError})
			}
		case StatusInFlight:
			if now.Sub(b.UpdatedAt) > stuckAfter && now.Sub(b.NextAttemptAt) > stuckAfter {
				add(Alert{Severity: "warning", Kind: "stuck", SourceID: b.SourceID, BatchID: b.ID, Since: b.UpdatedAt,
					Title: "Batch not moving", Message: fmt.Sprintf("%s has been at stage %d for %s with nothing running it", b.ID, b.Stage, now.Sub(b.UpdatedAt).Round(time.Second))})
			}
		}
	}
	byID := map[string]SourceHealth{}
	for _, h := range v.Sources {
		byID[h.ID] = h
	}
	for _, s := range sources {
		if scope != nil && !contains(scope, s.ID) {
			continue
		}
		h := byID[s.ID]
		switch h.Freshness {
		case "late", "stale":
			sev := "warning"
			if h.Freshness == "stale" {
				sev = "critical"
			}
			ref := h.LastBatchAt
			if ref.IsZero() {
				ref = s.CreatedAt
			}
			add(Alert{Severity: sev, Kind: "stale", SourceID: s.ID, Since: ref.Add(time.Duration(s.ExpectEvery)),
				Title:   "Data is late",
				Message: fmt.Sprintf("%s should deliver every %s; the last batch arrived %s ago", s.Name, time.Duration(s.ExpectEvery), now.Sub(ref).Round(time.Second))})
		}
		limit := max(0.05, s.MaxRejectRate/2)
		if h.RowsIn >= 20 && h.RejectRate > limit {
			add(Alert{Severity: "warning", Kind: "rejects", SourceID: s.ID, Since: now,
				Title:   "Many rows refused",
				Message: fmt.Sprintf("%.0f%% of %s rows in the last day were quarantined (%d of %d); batches are refused above %.0f%%", h.RejectRate*100, s.Name, h.Quarantined, h.RowsIn, s.MaxRejectRate*100)})
		}
		if s.Paused {
			add(Alert{Severity: "info", Kind: "paused", SourceID: s.ID, Since: s.UpdatedAt, Title: "Source paused", Message: s.Name + " is not accepting new batches"})
		}
	}
	for _, c := range v.Circuits {
		if c.State != "closed" {
			add(Alert{Severity: "warning", Kind: "circuit", Since: c.OpenUntil, Title: "Destination circuit open",
				Message: fmt.Sprintf("%s failed %d times in a row; deliveries wait (without using attempts) until it answers: %s", c.Destination, c.Failures, c.State)})
		}
	}
	if v.Queue.SweeperAgeSecs < 0 || v.Queue.SweeperAgeSecs > 30 {
		if v.Queue.Due > 0 || v.Queue.SweeperAgeSecs > 30 {
			add(Alert{Severity: "critical", Kind: "sweeper", Since: now, Title: "Nothing is running batches",
				Message: fmt.Sprintf("The sweeper has not run for %.0fs and %d batches are waiting", v.Queue.SweeperAgeSecs, v.Queue.Due)})
		}
	}
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		return out[i].Since.After(out[j].Since)
	})
	return out
}

// Alerts returns the current alerts for the sources the actor may monitor.
func (e *Engine) Alerts(ctx context.Context, actor Actor) ([]Alert, error) {
	v, err := e.Monitor(ctx, actor, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	return v.Alerts, nil
}

// Logs returns recent structured logs for the sources the actor may monitor.
func (e *Engine) LogsFor(ctx context.Context, actor Actor, f LogFilter) ([]LogEntry, error) {
	e.init()
	acc, err := e.need(ctx, actor, PermMonitor, "")
	if err != nil {
		return nil, err
	}
	f.Sources = acc.Sources(PermMonitor)
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	return e.Logs.Recent(f), nil
}

// HealthCheck is one probe.
type HealthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, degraded, down
	Detail string `json:"detail"`
	Ms     int64  `json:"ms"`
}

// Health runs the probes. The overall status is the worst of them: down when
// the store cannot be reached, degraded when the engine runs but needs attention.
func (e *Engine) Health(ctx context.Context) (string, []HealthCheck) {
	e.init()
	var checks []HealthCheck
	run := func(name string, fn func() (string, string)) {
		t := time.Now()
		st, detail := fn()
		checks = append(checks, HealthCheck{Name: name, Status: st, Detail: detail, Ms: time.Since(t).Milliseconds()})
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	run("store", func() (string, string) {
		if err := e.Store.Ping(pctx); err != nil {
			return "down", err.Error()
		}
		return "ok", "reachable"
	})
	run("sweeper", func() (string, string) {
		hb := e.heartbeat.Load()
		switch {
		case hb == 0:
			return "degraded", "no sweep has run yet"
		case time.Since(time.Unix(0, hb)) > 30*time.Second:
			return "down", fmt.Sprintf("last sweep %s ago", time.Since(time.Unix(0, hb)).Round(time.Second))
		}
		return "ok", fmt.Sprintf("last sweep %s ago", time.Since(time.Unix(0, hb)).Round(time.Millisecond))
	})
	run("queue", func() (string, string) {
		due, err := e.Store.Due(pctx, e.now(), 1000)
		if err != nil {
			return "down", err.Error()
		}
		var oldest time.Duration
		for _, b := range due {
			if !b.NextAttemptAt.IsZero() && e.now().Sub(b.NextAttemptAt) > oldest {
				oldest = e.now().Sub(b.NextAttemptAt)
			}
		}
		if oldest > 5*time.Minute {
			return "degraded", fmt.Sprintf("%d batches waiting, oldest %s", len(due), oldest.Round(time.Second))
		}
		return "ok", fmt.Sprintf("%d batches waiting", len(due))
	})
	run("circuits", func() (string, string) {
		open := 0
		for _, c := range e.Circuits(ctx) {
			if c.State == "open" {
				open++
			}
		}
		if open > 0 {
			return "degraded", fmt.Sprintf("%d destination circuit(s) open", open)
		}
		return "ok", "all closed"
	})
	run("held batches", func() (string, string) {
		sum, err := e.Store.Summary(pctx, nil)
		if err != nil {
			return "down", err.Error()
		}
		if n := sum.ByStatus[StatusHeld]; n > 0 {
			return "degraded", fmt.Sprintf("%d held, waiting for a replay", n)
		}
		if sum.RowsLost != 0 {
			return "down", fmt.Sprintf("%d rows unaccounted for", sum.RowsLost)
		}
		return "ok", "none held, no rows unaccounted for"
	})
	run("audit chain", func() (string, string) {
		e.auditMu.Lock()
		defer e.auditMu.Unlock()
		if time.Since(e.auditAt) > time.Minute {
			bad, total, err := e.Store.VerifyAudit(pctx)
			if err != nil {
				return "down", err.Error()
			}
			e.auditAt, e.auditBad = time.Now(), bad
			if bad == 0 {
				return "ok", fmt.Sprintf("%d entries verified", total)
			}
		} else if e.auditBad == 0 {
			return "ok", "verified within the last minute"
		}
		return "down", fmt.Sprintf("chain broken at entry %d", e.auditBad)
	})
	status := "ok"
	for _, c := range checks {
		if c.Status == "down" {
			status = "down"
		} else if c.Status == "degraded" && status == "ok" {
			status = "degraded"
		}
	}
	return status, checks
}

// MetricsText renders the metrics in the Prometheus text format. Counters and
// histograms come from the store, so they survive restarts and cover every
// process; gauges are worked out from the store at scrape time.
func (e *Engine) MetricsText(ctx context.Context) (string, error) {
	e.init()
	rows, err := e.Store.Counters(ctx)
	if err != nil {
		return "", err
	}
	sum, err := e.Store.Summary(ctx, nil)
	if err != nil {
		return "", err
	}
	var g []Gauge
	for _, st := range []string{StatusInFlight, StatusRetrying, StatusHeld, StatusDelivered, StatusFailed} {
		g = append(g, Gauge{"etl_batches", []string{"status", st}, float64(sum.ByStatus[st])})
	}
	g = append(g, Gauge{Name: "etl_rows_unaccounted", Value: float64(sum.RowsLost)})
	now := e.now()
	due, err := e.Store.Due(ctx, now, 1000)
	if err != nil {
		return "", err
	}
	var oldest float64
	for _, b := range due {
		if !b.NextAttemptAt.IsZero() && now.Sub(b.NextAttemptAt).Seconds() > oldest {
			oldest = now.Sub(b.NextAttemptAt).Seconds()
		}
	}
	g = append(g, Gauge{Name: "etl_due_batches", Value: float64(len(due))}, Gauge{Name: "etl_oldest_due_seconds", Value: oldest})
	age := -1.0
	if hb := e.heartbeat.Load(); hb > 0 {
		age = time.Since(time.Unix(0, hb)).Seconds()
	}
	g = append(g, Gauge{Name: "etl_sweeper_heartbeat_seconds", Value: age}, Gauge{Name: "etl_hooks_abandoned", Value: float64(e.abandon.Load())})
	for _, c := range e.Circuits(ctx) {
		open := 0.0
		if c.State != "closed" {
			open = 1
		}
		g = append(g, Gauge{"etl_circuit_open", []string{"destination", c.Destination}, open})
	}
	if r, ok := e.Store.(interface{ Retries() int64 }); ok {
		g = append(g, Gauge{Name: "etl_db_retries_total", Value: float64(r.Retries())})
	}
	if v, err := e.monitor(ctx, nil, 24*time.Hour); err == nil {
		count := map[string]float64{"critical": 0, "warning": 0, "info": 0}
		for _, a := range v.Alerts {
			count[a.Severity]++
		}
		for _, sv := range []string{"critical", "warning", "info"} {
			g = append(g, Gauge{"etl_alerts", []string{"severity", sv}, count[sv]})
		}
	}
	return RenderMetrics(rows, g), nil
}

// Counters returns the durable counters, for the console.
func (e *Engine) Counters(ctx context.Context, actor Actor) ([]CounterRow, error) {
	if _, err := e.need(ctx, actor, PermMonitor, ""); err != nil {
		return nil, err
	}
	return e.Store.Counters(ctx)
}

// EvaluateAlerts works out the alerts across every source, records which opened
// and which cleared (so there is a history), and tells Notify about the ones
// that need telling: an alert that opened and is not acknowledged is announced
// once, retried until Notify succeeds; one that cleared is announced once.
// Run it on a timer (the sweeper loop does).
func (e *Engine) EvaluateAlerts(ctx context.Context) error {
	e.init()
	v, err := e.monitor(ctx, nil, 24*time.Hour)
	if err != nil {
		return err
	}
	now := e.now()
	opened, cleared, err := e.Store.SyncAlerts(ctx, v.Alerts, now)
	if err != nil {
		return err
	}
	for _, a := range opened {
		e.log("warn", "alert opened: "+a.Title, nil, 0, "alert", a.ID, "severity", a.Severity, "message", a.Message)
	}
	for _, a := range cleared {
		e.log("info", "alert cleared: "+a.Title, nil, 0, "alert", a.ID)
	}
	if e.Notify == nil {
		return nil
	}
	for _, a := range cleared {
		if a.NotifiedAt.IsZero() || a.Severity == "info" {
			continue // the opening was never announced, so there is nothing to take back
		}
		_ = e.Notify(ctx, "cleared", a)
	}
	open, err := e.Store.ListAlerts(ctx, true, 500)
	if err != nil {
		return err
	}
	for _, a := range open {
		if !a.NotifiedAt.IsZero() || a.Acknowledged(now) || a.Severity == "info" {
			continue
		}
		if err := e.Notify(ctx, "opened", a); err != nil {
			e.log("warn", "alert notification failed; will retry", nil, 0, "alert", a.ID, "error", err.Error())
			continue
		}
		_ = e.Store.MarkNotified(ctx, a.RID, now)
	}
	return nil
}

// AckAlert silences an open alert for a while and records who did it and why.
// The alert stays listed, marked acknowledged, and is not announced again.
func (e *Engine) AckAlert(ctx context.Context, actor Actor, id, note string, d time.Duration) error {
	acc, err := e.need(ctx, actor, PermAdvance, "")
	if err != nil {
		return err
	}
	if d <= 0 {
		d = 4 * time.Hour
	}
	if d > 7*24*time.Hour {
		return fmt.Errorf("%w: an alert can be silenced for a week at most", ErrInvalid)
	}
	// An alert about a source needs the permission on that source.
	if parts := strings.SplitN(id, ":", 3); len(parts) == 3 && parts[1] != "" && !acc.Allows(PermAdvance, parts[1]) {
		return ErrNotFound
	}
	if err := e.Store.AckAlert(ctx, id, actor.ID, strings.TrimSpace(note), e.now().Add(d)); err != nil {
		return err
	}
	_ = e.Store.Commit(ctx, Change{Audit: []AuditEntry{e.audit(actor.ID, "alert.ack", "", "", "", fmt.Sprintf("%s for %s: %s", id, d, note))}})
	e.log("info", "alert acknowledged", nil, 0, "alert", id, "actor", actor.ID, "for", d.String())
	return nil
}

// AlertHistory lists alerts that opened and cleared (and those still open), newest first.
func (e *Engine) AlertHistory(ctx context.Context, actor Actor, limit int) ([]AlertRecord, error) {
	acc, err := e.need(ctx, actor, PermMonitor, "")
	if err != nil {
		return nil, err
	}
	all, err := e.Store.ListAlerts(ctx, false, max(limit, 1)*3)
	if err != nil {
		return nil, err
	}
	scope := acc.Sources(PermMonitor)
	out := []AlertRecord{}
	for _, a := range all {
		if scope != nil && (a.SourceID == "" || !contains(scope, a.SourceID)) {
			continue
		}
		out = append(out, a)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
