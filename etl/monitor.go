package etl

import (
	"context"
	"fmt"
	"sort"
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

const recentBatches = 1000

// Monitor builds the monitoring view for the last `window` (default 24h).
func (e *Engine) Monitor(ctx context.Context, actor Actor, window time.Duration) (*MonitorView, error) {
	acc, err := e.need(ctx, actor, PermMonitor, "")
	if err != nil {
		return nil, err
	}
	return e.monitor(ctx, acc.Sources(PermMonitor), window)
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
	v := &MonitorView{Since: since, Bucket: Dur(bucket), Series: []Point{}, Sources: []SourceHealth{}, Alerts: []Alert{}, Stages: []StageStat{}, Circuits: e.Circuits()}
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
	batches, err := e.Store.ListBatches(ctx, Query{SourceIDs: scope, Limit: recentBatches})
	if err != nil {
		return nil, err
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

	byBatchSource := map[string][]*Batch{}
	for _, b := range batches {
		byBatchSource[b.SourceID] = append(byBatchSource[b.SourceID], b)
	}
	stages := map[int]*struct {
		d    []float64
		runs int
		fail int
	}{}
	for _, b := range batches {
		if b.CreatedAt.Before(since) && b.UpdatedAt.Before(since) {
			continue
		}
		for _, ev := range b.Events {
			if ev.DurationMs == 0 && ev.Attempt == 0 || ev.At.Before(since) {
				continue
			}
			s := stages[ev.Stage]
			if s == nil {
				s = &struct {
					d    []float64
					runs int
					fail int
				}{}
				stages[ev.Stage] = s
			}
			s.runs++
			s.d = append(s.d, float64(ev.DurationMs))
			if ev.Kind == "retry" || ev.Kind == "held" {
				s.fail++
			}
		}
	}
	for st := 1; st <= 6; st++ {
		stat := StageStat{Stage: st, Name: StageNames[st]}
		if s := stages[st]; s != nil {
			var sum float64
			for _, d := range s.d {
				sum += d
			}
			stat.Runs, stat.Failures = s.runs, s.fail
			stat.AvgMs, stat.P95Ms = sum/float64(len(s.d)), percentile(s.d, .95)
		}
		v.Stages = append(v.Stages, stat)
	}

	for _, s := range sources {
		if scope != nil && !contains(scope, s.ID) {
			continue
		}
		h := SourceHealth{ID: s.ID, Name: s.Name, Owner: s.Owner, Destination: s.Destination, Paused: s.Paused, ExpectEvery: s.ExpectEvery}
		var lat []float64
		var latSum float64
		for _, b := range byBatchSource[s.ID] {
			if h.LastBatchAt.IsZero() || b.CreatedAt.After(h.LastBatchAt) {
				h.LastBatchAt = b.CreatedAt
			}
			switch b.Status {
			case StatusHeld:
				h.Held++
			case StatusRetrying:
				h.Retrying++
			}
			if b.CreatedAt.Before(since) {
				continue
			}
			h.Batches++
			h.RowsIn += b.RowsIn
			h.Quarantined += b.Quarantined
			h.Delivered += b.Delivered
			if b.Status == StatusDelivered && !b.FinishedAt.IsZero() {
				d := b.FinishedAt.Sub(b.CreatedAt).Seconds()
				lat, latSum = append(lat, d), latSum+d
			}
		}
		if h.RowsIn > 0 {
			h.RejectRate = float64(h.Quarantined) / float64(h.RowsIn)
		}
		if len(lat) > 0 {
			h.AvgSeconds, h.P95Seconds = latSum/float64(len(lat)), percentile(lat, .95)
		}
		h.Freshness = freshness(s, h.LastBatchAt, now)
		v.Sources = append(v.Sources, h)
	}
	if a := e.alerts(sources, batches, v, scope, now); a != nil {
		v.Alerts = a
	}
	return v, nil
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
		for _, c := range e.Circuits() {
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
	g = append(g, Gauge{Name: "etl_sweeper_heartbeat_seconds", Value: age})
	for _, c := range e.Circuits() {
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
