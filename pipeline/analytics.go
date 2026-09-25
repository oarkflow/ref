package pipeline

import (
	"math"
	"sort"
	"time"
)

// Analytics is process mining over a set of cases: where time goes, where
// work queues up, how often it loops back, and whether SLAs hold.
type Analytics struct {
	Pipeline   string           `json:"pipeline"`
	AsOf       time.Time        `json:"as_of"`
	Cases      int              `json:"cases"`
	Open       int              `json:"open"`
	ByStatus   map[string]int   `json:"by_status"`
	CycleHours Stat             `json:"cycle_hours"`
	Stages     []StageAnalytics `json:"stages"`
	// Bottlenecks ranks stages by open work weighted by their p90 dwell.
	Bottlenecks []string            `json:"bottlenecks"`
	Throughput  []Bucket            `json:"throughput"`
	Assignees   []AssigneeAnalytics `json:"assignees,omitempty"`
}

// Stat summarises durations in hours.
type Stat struct {
	Count int     `json:"count"`
	Avg   float64 `json:"avg"`
	P50   float64 `json:"p50"`
	P90   float64 `json:"p90"`
	Max   float64 `json:"max"`
}

// StageAnalytics is one stage's figures.
type StageAnalytics struct {
	Stage      string  `json:"stage"`
	Title      string  `json:"title,omitempty"`
	Visits     int     `json:"visits"`
	Completed  int     `json:"completed"`
	Returned   int     `json:"returned"`
	Skipped    int     `json:"skipped"`
	OpenNow    int     `json:"open_now"`
	OnHold     int     `json:"on_hold"`
	Unassigned int     `json:"unassigned"`
	DwellHours Stat    `json:"dwell_hours"`
	ReworkRate float64 `json:"rework_rate"`
	// SLA figures cover finished visits of stages with an SLA.
	SLAMet        int     `json:"sla_met"`
	SLABreached   int     `json:"sla_breached"`
	SLAAttainment float64 `json:"sla_attainment"`
	AtRisk        int     `json:"at_risk"`
}

// Bucket is closed cases per day.
type Bucket struct {
	Day    string `json:"day"`
	Closed int    `json:"closed"`
	Opened int    `json:"opened"`
}

// AssigneeAnalytics is one person's figures.
type AssigneeAnalytics struct {
	Assignee   string `json:"assignee"`
	Open       int    `json:"open"`
	Completed  int    `json:"completed"`
	DwellHours Stat   `json:"dwell_hours"`
}

// Analyze computes analytics over cases as of now.
func (e *Engine) Analyze(cases []*Case, now time.Time) Analytics {
	a := Analytics{Pipeline: e.C.Def.Name, AsOf: now, ByStatus: map[string]int{}}
	stageIdx := map[string]int{}
	dwell := map[string][]float64{}
	for i, st := range e.C.Def.Stages {
		stageIdx[st.Name] = i
		a.Stages = append(a.Stages, StageAnalytics{Stage: st.Name, Title: st.Title})
	}
	people := map[string]*AssigneeAnalytics{}
	personDwell := map[string][]float64{}
	person := func(id string) *AssigneeAnalytics {
		if people[id] == nil {
			people[id] = &AssigneeAnalytics{Assignee: id}
		}
		return people[id]
	}
	var cycles []float64
	days := map[string]*Bucket{}
	day := func(t time.Time) *Bucket {
		k := t.UTC().Format("2006-01-02")
		if days[k] == nil {
			days[k] = &Bucket{Day: k}
		}
		return days[k]
	}
	for _, c := range cases {
		a.Cases++
		a.ByStatus[c.Status]++
		day(c.CreatedAt).Opened++
		if c.Terminal() {
			end := c.UpdatedAt
			if c.ClosedAt != nil {
				end = *c.ClosedAt
			}
			cycles = append(cycles, end.Sub(c.CreatedAt).Hours())
			day(end).Closed++
		} else {
			a.Open++
			if i, ok := stageIdx[c.Stage]; ok {
				s := &a.Stages[i]
				s.OpenNow++
				if ss := c.Stages[c.Stage]; ss != nil {
					if ss.Suspended != nil {
						s.OnHold++
					}
					if ss.Assignee == "" {
						s.Unassigned++
					} else {
						person(ss.Assignee).Open++
					}
					if ss.SLA != nil && (ss.SLA.Status == SLAWarning || (ss.SLA.Status != SLABreached && ss.SLA.WarnAt != nil && !now.Before(*ss.SLA.WarnAt))) {
						s.AtRisk++
					}
				}
			}
		}
		for _, v := range c.Timeline {
			i, ok := stageIdx[v.Stage]
			if !ok {
				continue
			}
			s := &a.Stages[i]
			s.Visits++
			switch v.Outcome {
			case "completed", "approved":
				s.Completed++
			case "returned":
				s.Returned++
			case "skipped":
				s.Skipped++
			}
			if v.LeftAt == nil || v.Outcome == "skipped" {
				continue
			}
			h := v.LeftAt.Sub(v.EnteredAt).Hours() - float64(v.SuspendedSeconds)/3600
			if h < 0 {
				h = 0
			}
			dwell[v.Stage] = append(dwell[v.Stage], h)
			if v.Assignee != "" {
				person(v.Assignee).Completed++
				personDwell[v.Assignee] = append(personDwell[v.Assignee], h)
			}
			if e.C.Def.Stages[i].SLA != nil {
				if v.Breached {
					s.SLABreached++
				} else {
					s.SLAMet++
				}
			}
		}
	}
	a.CycleHours = stat(cycles)
	type rank struct {
		stage string
		score float64
	}
	var ranks []rank
	for i := range a.Stages {
		s := &a.Stages[i]
		s.DwellHours = stat(dwell[s.Stage])
		if s.Visits > 0 {
			s.ReworkRate = round(float64(s.Returned) / float64(s.Visits))
		}
		if n := s.SLAMet + s.SLABreached; n > 0 {
			s.SLAAttainment = round(float64(s.SLAMet) / float64(n))
		}
		if s.OpenNow > 0 {
			ranks = append(ranks, rank{s.Stage, float64(s.OpenNow) * math.Max(s.DwellHours.P90, 1)})
		}
	}
	sort.SliceStable(ranks, func(i, j int) bool { return ranks[i].score > ranks[j].score })
	for _, r := range ranks {
		a.Bottlenecks = append(a.Bottlenecks, r.stage)
	}
	for _, b := range days {
		a.Throughput = append(a.Throughput, *b)
	}
	sort.Slice(a.Throughput, func(i, j int) bool { return a.Throughput[i].Day < a.Throughput[j].Day })
	for id, p := range people {
		p.DwellHours = stat(personDwell[id])
		a.Assignees = append(a.Assignees, *p)
	}
	sort.Slice(a.Assignees, func(i, j int) bool { return a.Assignees[i].Assignee < a.Assignees[j].Assignee })
	return a
}

func stat(xs []float64) Stat {
	if len(xs) == 0 {
		return Stat{}
	}
	s := slicesSorted(xs)
	sum := 0.0
	for _, x := range s {
		sum += x
	}
	pct := func(p float64) float64 {
		// nearest rank
		idx := int(math.Ceil(p*float64(len(s)))) - 1
		idx = max(0, min(idx, len(s)-1))
		return round(s[idx])
	}
	return Stat{Count: len(s), Avg: round(sum / float64(len(s))), P50: pct(0.5), P90: pct(0.9), Max: round(s[len(s)-1])}
}

func slicesSorted(xs []float64) []float64 {
	out := append([]float64(nil), xs...)
	sort.Float64s(out)
	return out
}

func round(f float64) float64 { return math.Round(f*100) / 100 }
