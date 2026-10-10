package etl

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-process Store for tests and single-process demos.
type MemoryStore struct {
	mu      sync.Mutex
	sources map[string]*Source
	batches map[string]*Batch
	keys    map[string]string // source + "\x00" + key -> batch id
	cps     map[string][]Checkpoint
	quar    map[string][]Quarantine
	lin     map[string][]LineageEdge
	audit   []AuditEntry
	roles   map[string]*Role
	leases  map[string]lease
	breaks  map[string]BreakerState
	alerts  []*AlertRecord
	counts  map[string]*CounterRow
}

type lease struct {
	owner string
	until time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sources: map[string]*Source{}, batches: map[string]*Batch{}, keys: map[string]string{},
		cps: map[string][]Checkpoint{}, quar: map[string][]Quarantine{}, lin: map[string][]LineageEdge{}, roles: map[string]*Role{}, leases: map[string]lease{}, breaks: map[string]BreakerState{}, counts: map[string]*CounterRow{}}
}

func clone[T any](v *T) *T {
	b, _ := json.Marshal(v)
	out := new(T)
	_ = json.Unmarshal(b, out)
	return out
}

func (m *MemoryStore) Commit(ctx context.Context, c Change) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := c.Batch; b != nil {
		if b.Revision == 0 {
			if _, dup := m.keys[b.SourceID+"\x00"+b.Key]; dup {
				return ErrConflict
			}
			if _, dup := m.batches[b.ID]; dup {
				return ErrConflict
			}
		} else if cur, ok := m.batches[b.ID]; !ok || cur.Revision != b.Revision {
			return ErrConflict
		}
	}
	if s := c.Source; s != nil {
		m.sources[s.ID] = clone(s)
	}
	if r := c.Role; r != nil {
		m.roles[r.ID] = clone(r)
	}
	if c.DeleteRole != "" {
		delete(m.roles, c.DeleteRole)
	}
	if b := c.Batch; b != nil {
		b.Revision++
		m.batches[b.ID] = clone(b)
		delete(m.leases, b.ID) // a commit ends the worker's lease
		m.keys[b.SourceID+"\x00"+b.Key] = b.ID
	}
	for _, d := range c.Counters {
		k := d.Name + "{" + d.Labels + "}"
		row := m.counts[k]
		if row == nil {
			row = &CounterRow{Name: d.Name, Labels: d.Labels}
			m.counts[k] = row
		}
		row.Value += d.Value
	}
	for _, cp := range c.Checkpoints {
		m.cps[cp.BatchID] = append(m.cps[cp.BatchID], *clone(&cp))
	}
	for _, q := range c.Quarantine {
		m.quar[q.BatchID] = append(m.quar[q.BatchID], *clone(&q))
	}
	for _, e := range c.Lineage {
		m.lin[e.BatchID] = append(m.lin[e.BatchID], e)
	}
	for _, e := range c.Audit {
		e.Seq = int64(len(m.audit) + 1)
		if n := len(m.audit); n > 0 {
			e.PrevHash = m.audit[n-1].Hash
		}
		e.At = e.At.UTC()
		e.Hash = ChainHash(e)
		m.audit = append(m.audit, e)
	}
	return nil
}

func (m *MemoryStore) Ping(ctx context.Context) error { return nil }

func (m *MemoryStore) GetRole(ctx context.Context, id string) (*Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.roles[id]; ok {
		return clone(r), nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) ListRoles(ctx context.Context) ([]*Role, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Role
	for _, r := range m.roles {
		out = append(out, clone(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemoryStore) GetSource(ctx context.Context, id string) (*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sources[id]; ok {
		return clone(s), nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) ListSources(ctx context.Context) ([]*Source, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Source
	for _, s := range m.sources {
		out = append(out, clone(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemoryStore) GetBatch(ctx context.Context, id string) (*Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.batches[id]; ok {
		return clone(b), nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) FindBatch(ctx context.Context, sourceID, key string) (*Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.keys[sourceID+"\x00"+key]; ok {
		return clone(m.batches[id]), nil
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) ListBatches(ctx context.Context, q Query) ([]*Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Batch
	for _, b := range m.batches {
		if q.SourceID != "" && b.SourceID != q.SourceID {
			continue
		}
		if q.SourceIDs != nil && !contains(q.SourceIDs, b.SourceID) {
			continue
		}
		if len(q.Statuses) > 0 && !contains(q.Statuses, b.Status) {
			continue
		}
		out = append(out, clone(b))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return page(out, q.Limit, q.Offset), nil
}

func page[T any](s []T, limit, offset int) []T {
	if offset > len(s) {
		offset = len(s)
	}
	s = s[offset:]
	if limit > 0 && limit < len(s) {
		s = s[:limit]
	}
	return s
}

func (m *MemoryStore) LatestCheckpoint(ctx context.Context, batchID string) (*Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.cps[batchID]
	if len(l) == 0 {
		return nil, ErrNotFound
	}
	return clone(&l[len(l)-1]), nil
}

func (m *MemoryStore) ListQuarantine(ctx context.Context, batchID string, sources []string, limit int) ([]Quarantine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Quarantine
	if batchID != "" {
		out = append(out, m.quar[batchID]...)
	} else {
		for _, l := range m.quar {
			for _, q := range l {
				if sources == nil || contains(sources, q.SourceID) {
					out = append(out, q)
				}
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	}
	return page(out, limit, 0), nil
}

func (m *MemoryStore) ListAudit(ctx context.Context, batchID string, sources []string, limit, offset int) ([]AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AuditEntry
	for i := len(m.audit) - 1; i >= 0; i-- {
		if (batchID == "" || m.audit[i].BatchID == batchID) && (sources == nil || contains(sources, m.audit[i].SourceID)) {
			out = append(out, m.audit[i])
		}
	}
	return page(out, limit, offset), nil
}

func (m *MemoryStore) VerifyAudit(ctx context.Context) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return verifyChain(m.audit)
}

// verifyChain checks sequence numbers, links and hashes of entries in order.
func verifyChain(entries []AuditEntry) (int64, int64, error) {
	prev := ""
	for i, e := range entries {
		if e.Seq != int64(i+1) || e.PrevHash != prev || ChainHash(e) != e.Hash {
			return e.Seq, int64(len(entries)), nil
		}
		prev = e.Hash
	}
	return 0, int64(len(entries)), nil
}

func (m *MemoryStore) Lineage(ctx context.Context, batchID string) ([]LineageEdge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]LineageEdge(nil), m.lin[batchID]...), nil
}

func (m *MemoryStore) Due(ctx context.Context, now time.Time, limit int) ([]*Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Batch
	for _, b := range m.batches {
		if l, ok := m.leases[b.ID]; ok && l.until.After(now) {
			continue
		}
		if (b.Status == StatusRetrying || b.Status == StatusInFlight) && !b.NextAttemptAt.After(now) {
			out = append(out, clone(b))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextAttemptAt.Before(out[j].NextAttemptAt) })
	return page(out, limit, 0), nil
}

func (m *MemoryStore) Summary(ctx context.Context, sources []string) (Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var groups []group
	for _, b := range m.batches {
		if sources != nil && !contains(sources, b.SourceID) {
			continue
		}
		groups = append(groups, group{b.Status, b.Stage, 1, b.RowsIn, b.Quarantined, b.Delivered})
	}
	return summarize(groups), nil
}

func (m *MemoryStore) Series(ctx context.Context, since time.Time, bucket time.Duration, sources []string) ([]Point, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := map[int64]*Point{}
	for _, b := range m.batches {
		if b.CreatedAt.Before(since) || (sources != nil && !contains(sources, b.SourceID)) {
			continue
		}
		k := b.CreatedAt.UnixNano() / int64(bucket)
		p := agg[k]
		if p == nil {
			p = &Point{At: time.Unix(0, k*int64(bucket)).UTC()}
			agg[k] = p
		}
		p.add(b)
	}
	return fillSeries(agg, since, bucket), nil
}

func (p *Point) add(b *Batch) {
	p.Batches++
	p.RowsIn += b.RowsIn
	p.Delivered += b.Delivered
	p.Quarantined += b.Quarantined
	switch b.Status {
	case StatusHeld:
		p.Held++
	case StatusFailed:
		p.Failed++
	}
}

// fillSeries returns every bucket from since to now in order, empty ones too,
// so a chart has a point for each window.
func fillSeries(agg map[int64]*Point, since time.Time, bucket time.Duration) []Point {
	first := since.UnixNano() / int64(bucket)
	last := time.Now().UnixNano() / int64(bucket)
	for k := range agg {
		last = max(last, k)
	}
	var out []Point
	for k := first; k <= last; k++ {
		if p := agg[k]; p != nil {
			out = append(out, *p)
		} else {
			out = append(out, Point{At: time.Unix(0, k*int64(bucket)).UTC()})
		}
	}
	return out
}

// group is one status/stage bucket of batches, the unit Summary is built from.
type group struct {
	Status                         string
	Stage, Count                   int
	RowsIn, Quarantined, Delivered int
}

func summarize(groups []group) Summary {
	s := Summary{ByStatus: map[string]int{}}
	accounted := 0 // rows delivered, refused, still travelling, or in a failed batch (refused as a whole)
	for _, g := range groups {
		s.Batches += g.Count
		s.ByStatus[g.Status] += g.Count
		s.RowsIn += g.RowsIn
		s.Quarantined += g.Quarantined
		s.Delivered += g.Delivered
		accounted += g.Quarantined
		switch g.Status {
		case StatusInFlight, StatusRetrying, StatusHeld:
			s.ByStage[min(max(g.Stage, 0), 7)] += g.Count
			accounted += g.RowsIn - g.Quarantined
		case StatusDelivered:
			accounted += g.Delivered
		case StatusFailed:
			accounted += g.RowsIn - g.Quarantined
		}
	}
	s.RowsLost = s.RowsIn - accounted
	return s
}

func (m *MemoryStore) Claim(ctx context.Context, id, owner string, now time.Time, ttl time.Duration) (*Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batches[id]
	if !ok {
		return nil, ErrNotFound
	}
	if l, held := m.leases[id]; held && l.until.After(now) && l.owner != owner {
		return nil, ErrLeased
	}
	m.leases[id] = lease{owner, now.Add(ttl)}
	return clone(b), nil
}

func (m *MemoryStore) Release(ctx context.Context, id, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.leases[id]; ok && l.owner == owner {
		delete(m.leases, id)
	}
	return nil
}

func (m *MemoryStore) FindByHash(ctx context.Context, sourceID, hash string) (*Batch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.batches {
		if b.SourceID == sourceID && b.ContentHash == hash {
			return clone(b), nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemoryStore) Prune(ctx context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, b := range m.batches {
		if (b.Status == StatusDelivered || b.Status == StatusFailed) && !b.FinishedAt.IsZero() && b.FinishedAt.Before(before) {
			n += len(m.cps[id])
			delete(m.cps, id)
		}
	}
	return n, nil
}

func (m *MemoryStore) Counters(ctx context.Context) ([]CounterRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CounterRow, 0, len(m.counts))
	for _, r := range m.counts {
		out = append(out, *r)
	}
	return out, nil
}

func (m *MemoryStore) RecordBreaker(ctx context.Context, dest string, ok bool, now time.Time, threshold int, cooldown time.Duration) (BreakerState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.breaks[dest]
	st.Destination, st.UpdatedAt = dest, now
	nextBreaker(&st, ok, now, threshold, cooldown)
	m.breaks[dest] = st
	return st, nil
}

// nextBreaker applies one result to a breaker: success closes it, a failure
// counts, and the threshold-th failure in a row (and every one after it) opens
// it for the cool-down.
func nextBreaker(st *BreakerState, ok bool, now time.Time, threshold int, cooldown time.Duration) {
	if ok {
		st.Failures, st.OpenUntil = 0, time.Time{}
		return
	}
	st.Failures++
	if st.Failures >= threshold {
		st.OpenUntil = now.Add(cooldown)
	}
}

func (m *MemoryStore) Breakers(ctx context.Context) ([]BreakerState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []BreakerState{}
	for _, b := range m.breaks {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Destination < out[j].Destination })
	return out, nil
}

func (m *MemoryStore) SyncAlerts(ctx context.Context, current []Alert, now time.Time) (opened, cleared []AlertRecord, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	open := map[string]*AlertRecord{}
	for _, a := range m.alerts {
		if a.ClearedAt.IsZero() {
			open[a.ID] = a
		}
	}
	seen := map[string]bool{}
	for _, c := range current {
		seen[c.ID] = true
		if rec := open[c.ID]; rec != nil {
			rec.Alert = c
			continue
		}
		rec := &AlertRecord{Alert: c, RID: fmt.Sprintf("%s@%d", c.ID, now.UnixNano()), OpenedAt: now}
		m.alerts = append(m.alerts, rec)
		opened = append(opened, *rec)
	}
	for id, rec := range open {
		if !seen[id] {
			rec.ClearedAt = now
			cleared = append(cleared, *rec)
		}
	}
	return opened, cleared, nil
}

func (m *MemoryStore) ListAlerts(ctx context.Context, openOnly bool, limit int) ([]AlertRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AlertRecord{}
	for i := len(m.alerts) - 1; i >= 0; i-- {
		if a := m.alerts[i]; !openOnly || a.ClearedAt.IsZero() {
			out = append(out, *a)
		}
	}
	return page(out, limit, 0), nil
}

func (m *MemoryStore) AckAlert(ctx context.Context, id, by, note string, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.alerts {
		if a.ID == id && a.ClearedAt.IsZero() {
			a.AckedBy, a.AckedNote, a.AckedUntil = by, note, until
			return nil
		}
	}
	return ErrNotFound
}

func (m *MemoryStore) MarkNotified(ctx context.Context, rid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.alerts {
		if a.RID == rid {
			a.NotifiedAt = at
		}
	}
	return nil
}

func (m *MemoryStore) SourceStats(ctx context.Context, since time.Time, sources []string) ([]SourceStat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	agg := map[string]*SourceStat{}
	lat := map[string][]float64{}
	for _, b := range m.batches {
		if sources != nil && !contains(sources, b.SourceID) {
			continue
		}
		st := agg[b.SourceID]
		if st == nil {
			st = &SourceStat{SourceID: b.SourceID}
			agg[b.SourceID] = st
		}
		if b.CreatedAt.After(st.LastBatchAt) {
			st.LastBatchAt = b.CreatedAt
		}
		switch b.Status {
		case StatusHeld:
			st.Held++
		case StatusRetrying:
			st.Retrying++
		}
		if b.CreatedAt.Before(since) {
			continue
		}
		st.Batches++
		st.RowsIn += b.RowsIn
		st.Delivered += b.Delivered
		st.Quarantined += b.Quarantined
		if b.Status == StatusDelivered && !b.FinishedAt.IsZero() {
			lat[b.SourceID] = append(lat[b.SourceID], b.FinishedAt.Sub(b.CreatedAt).Seconds())
		}
	}
	out := []SourceStat{}
	for id, st := range agg {
		var sum float64
		for _, v := range lat[id] {
			sum += v
		}
		if n := len(lat[id]); n > 0 {
			st.AvgSeconds = sum / float64(n)
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, nil
}

func (m *MemoryStore) LatencySample(ctx context.Context, sourceID string, since time.Time, limit int) ([]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var bs []*Batch
	for _, b := range m.batches {
		if b.SourceID == sourceID && b.Status == StatusDelivered && !b.CreatedAt.Before(since) && !b.FinishedAt.IsZero() {
			bs = append(bs, b)
		}
	}
	sort.Slice(bs, func(i, j int) bool { return bs[i].CreatedAt.After(bs[j].CreatedAt) })
	out := []float64{}
	for _, b := range page(bs, limit, 0) {
		out = append(out, b.FinishedAt.Sub(b.CreatedAt).Seconds())
	}
	return out, nil
}

func (m *MemoryStore) PruneCounters(ctx context.Context, beforeHour string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, r := range m.counts {
		if strings.HasPrefix(r.Name, HourlyPrefix) && labelHour(r.Labels) != "" && labelHour(r.Labels) < beforeHour {
			delete(m.counts, k)
			n++
		}
	}
	return n, nil
}

// HourlyPrefix names the per-hour counters Monitor reads and retention prunes.
const HourlyPrefix = "etl_hourly_"

// labelHour reads the hour="YYYYMMDDHH" label out of a counter's label string.
func labelHour(labels string) string {
	i := strings.Index(labels, `hour="`)
	if i < 0 {
		return ""
	}
	rest := labels[i+6:]
	if j := strings.IndexByte(rest, '"'); j > 0 {
		return rest[:j]
	}
	return ""
}
