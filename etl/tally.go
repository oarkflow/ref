package etl

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Tally collects counter changes during one operation. They are committed with
// the operation's other writes, so counters live in the store (and survive a
// restart, and are shared by every process using it) and never disagree with
// the state they count.
type Tally struct {
	m map[string]*CounterDelta
}

func NewTally() *Tally { return &Tally{m: map[string]*CounterDelta{}} }

func labelPairs(kv []string) string {
	pairs := make([]string, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, kv[i], strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (t *Tally) add(name, labels string, v float64) {
	if t == nil {
		return
	}
	k := name + "{" + labels + "}"
	d := t.m[k]
	if d == nil {
		d = &CounterDelta{Name: name, Labels: labels}
		t.m[k] = d
	}
	d.Value += v
}

// Add increments a counter.
func (t *Tally) Add(name string, v float64, kv ...string) { t.add(name, labelPairs(kv), v) }

// Observe records a value in a histogram: it adds to the bucket counters, the sum and the count.
func (t *Tally) Observe(name string, v float64, kv ...string) {
	if t == nil {
		return
	}
	base := labelPairs(kv)
	with := func(le string) string {
		if base == "" {
			return `le="` + le + `"`
		}
		return base + `,le="` + le + `"`
	}
	for _, b := range buckets {
		if v <= b {
			t.add(name+"_bucket", with(num(b)), 1)
		}
	}
	t.add(name+"_bucket", with("+Inf"), 1)
	t.add(name+"_sum", base, v)
	t.add(name+"_count", base, 1)
}

// Deltas returns the changes in a fixed order, so concurrent writers lock rows
// in the same order and cannot deadlock each other.
func (t *Tally) Deltas() []CounterDelta {
	if t == nil {
		return nil
	}
	out := make([]CounterDelta, 0, len(t.m))
	for _, d := range t.m {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Labels < out[j].Labels
	})
	return out
}

var buckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300}

// Gauge is a value worked out at scrape time.
type Gauge struct {
	Name   string
	Labels []string
	Value  float64
}

var help = map[string]string{
	"etl_batches_ingested_total":        "Batches accepted, by source.",
	"etl_rows_total":                    "Rows by source and what became of them (in, quarantined, delivered).",
	"etl_stage_runs_total":              "Stage runs by stage and outcome (ok, retry, held).",
	"etl_stage_duration_seconds":        "How long a stage run took.",
	"etl_batch_duration_seconds":        "Time from a batch arriving to its delivery.",
	"etl_replays_total":                 "Held batches replayed.",
	"etl_hook_panics_total":             "Stage hooks that panicked (contained as a failed attempt).",
	"etl_hook_timeouts_total":           "Stage hooks that ran past the stage timeout.",
	"etl_circuit_opened_total":          "Times a destination's circuit breaker opened.",
	"etl_circuit_waits_total":           "Stage runs deferred because a destination's circuit was open.",
	"etl_lease_conflicts_total":         "Stage runs skipped because another worker held the batch.",
	"etl_contract_violations_total":     "Transforms held for changing the row count or breaking the output rules.",
	"etl_reconciliation_failures_total": "Batches held because rows in did not equal quarantined plus delivered.",
	"etl_duplicates_rejected_total":     "Uploads refused as duplicate content.",
	"etl_refused_total":                 "Uploads refused before any row was read, by reason.",
	"etl_pruned_checkpoints_total":      "Checkpoints removed by retention.",
	"etl_batches":                       "Batches by status.",
	"etl_rows_unaccounted":              "Rows in minus quarantined, delivered and still travelling. Must be 0.",
	"etl_due_batches":                   "Batches waiting to be advanced.",
	"etl_oldest_due_seconds":            "Age of the oldest batch waiting to be advanced.",
	"etl_sweeper_heartbeat_seconds":     "Seconds since the sweeper last ran.",
	"etl_alerts":                        "Open alerts by severity.",
	"etl_circuit_open":                  "1 while a destination's circuit is open.",
	"etl_db_retries_total":              "Store writes retried after a transient database error (this process).",
}

func num(v float64) string {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return "0"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

type histo struct {
	buckets map[string]float64
	sum     float64
	count   float64
}

// RenderMetrics writes durable counters and gauges in the Prometheus text format.
func RenderMetrics(rows []CounterRow, gauges []Gauge) string {
	var b strings.Builder
	seen := map[string]bool{}
	head := func(name, kind string) {
		if !seen[name] {
			seen[name] = true
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help[name], name, kind)
		}
	}
	hists := map[string]map[string]*histo{} // base -> labels without le -> histogram
	var plain []CounterRow
	for _, r := range rows {
		switch {
		case strings.HasSuffix(r.Name, "_bucket"), strings.HasSuffix(r.Name, "_sum"), strings.HasSuffix(r.Name, "_count"):
			base := r.Name[:strings.LastIndex(r.Name, "_")]
			labels, le := r.Labels, ""
			if strings.HasSuffix(r.Name, "_bucket") {
				parts := strings.Split(labels, ",")
				kept := parts[:0]
				for _, p := range parts {
					if strings.HasPrefix(p, `le="`) {
						le = strings.TrimSuffix(strings.TrimPrefix(p, `le="`), `"`)
					} else if p != "" {
						kept = append(kept, p)
					}
				}
				labels = strings.Join(kept, ",")
			}
			if hists[base] == nil {
				hists[base] = map[string]*histo{}
			}
			h := hists[base][labels]
			if h == nil {
				h = &histo{buckets: map[string]float64{}}
				hists[base][labels] = h
			}
			switch {
			case strings.HasSuffix(r.Name, "_bucket"):
				h.buckets[le] = r.Value
			case strings.HasSuffix(r.Name, "_sum"):
				h.sum = r.Value
			default:
				h.count = r.Value
			}
		default:
			plain = append(plain, r)
		}
	}
	sort.Slice(plain, func(i, j int) bool {
		if plain[i].Name != plain[j].Name {
			return plain[i].Name < plain[j].Name
		}
		return plain[i].Labels < plain[j].Labels
	})
	wrap := func(l string) string {
		if l == "" {
			return ""
		}
		return "{" + l + "}"
	}
	for _, r := range plain {
		head(r.Name, "counter")
		fmt.Fprintf(&b, "%s%s %s\n", r.Name, wrap(r.Labels), num(r.Value))
	}
	for _, g := range gauges {
		head(g.Name, "gauge")
		fmt.Fprintf(&b, "%s%s %s\n", g.Name, wrap(labelPairs(g.Labels)), num(g.Value))
	}
	bases := make([]string, 0, len(hists))
	for k := range hists {
		bases = append(bases, k)
	}
	sort.Strings(bases)
	for _, base := range bases {
		head(base, "histogram")
		labelSets := make([]string, 0, len(hists[base]))
		for l := range hists[base] {
			labelSets = append(labelSets, l)
		}
		sort.Strings(labelSets)
		for _, l := range labelSets {
			h := hists[base][l]
			with := func(le string) string {
				if l == "" {
					return `{le="` + le + `"}`
				}
				return "{" + l + `,le="` + le + `"}`
			}
			for _, bound := range buckets {
				fmt.Fprintf(&b, "%s_bucket%s %s\n", base, with(num(bound)), num(h.buckets[num(bound)]))
			}
			fmt.Fprintf(&b, "%s_bucket%s %s\n%s_sum%s %s\n%s_count%s %s\n", base, with("+Inf"), num(h.buckets["+Inf"]), base, wrap(l), num(h.sum), base, wrap(l), num(h.count))
		}
	}
	return b.String()
}
