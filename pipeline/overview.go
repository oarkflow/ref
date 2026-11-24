package pipeline

import "time"

// LogEntry is one visit of a stage: who started it, how long it waited and how
// long it took, in seconds. The people are left out for viewers who are not
// staff.
type LogEntry struct {
	Stage        string     `json:"stage"`
	Title        string     `json:"title,omitempty"`
	EnteredAt    time.Time  `json:"entered_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	StartedBy    string     `json:"started_by,omitempty"`
	LeftAt       *time.Time `json:"left_at,omitempty"`
	Outcome      string     `json:"outcome,omitempty"`
	Assignee     string     `json:"assignee,omitempty"`
	WaitSeconds  int64      `json:"wait_seconds"`
	WorkSeconds  int64      `json:"work_seconds"`
	HoldSeconds  int64      `json:"hold_seconds,omitempty"`
	TotalSeconds int64      `json:"total_seconds"`
	DueAt        *time.Time `json:"due_at,omitempty"`
	Breached     bool       `json:"breached,omitempty"`
	Open         bool       `json:"open,omitempty"`
}

// SummaryField is one answered input of the case, with where it came from.
type SummaryField struct {
	Stage  string `json:"stage"`
	Group  string `json:"group,omitempty"`
	Path   string `json:"path"`
	Label  string `json:"label"`
	Value  any    `json:"value"`
	Masked bool   `json:"masked,omitempty"`
}

// Overview is the case at a glance: what it is, where it stands, the data
// gathered so far, the log of who did what and how long it took, and what the
// viewer can do next. It is not a stage screen; working a stage is.
type Overview struct {
	Case     CaseSummary     `json:"case"`
	Stage    StageView       `json:"stage"`
	Progress []StageProgress `json:"progress"`
	Fields   []SummaryField  `json:"fields"`
	Work     *WorkView       `json:"work,omitempty"`
	// Information is the pending request for more information, if any.
	Information          *InformationRequest `json:"information,omitempty"`
	InformationCanAnswer bool                `json:"information_can_answer,omitempty"`
	Log                  []LogEntry          `json:"log"`
	// Breaches are the missed deadlines and how each was settled (staff only).
	Breaches []BreachRecord `json:"breaches,omitempty"`
	// CanWork: the viewer has something to do on the current stage.
	// Entries is how the current stage's entries stand for the viewer (a
	// data-entry stage); EntryRows are the entries the viewer may read.
	Entries   *EntriesView `json:"entries,omitempty"`
	EntryRows []EntryRow   `json:"entry_rows,omitempty"`
	// Party is who the current stage is waiting for outside the workflow.
	Party *PartyView `json:"party,omitempty"`
	// Certificates issued on the case.
	Certificates []Certificate `json:"certificates,omitempty"`
	// UI is the workflow's look and the overview's layout.
	UI      *UIView `json:"ui,omitempty"`
	CanWork bool    `json:"can_work"`
	// NeedsStart: the viewer must press Start before working.
	NeedsStart bool `json:"needs_start"`
}

// Overview builds the case at a glance for the actor.
func (e *Engine) Overview(c *Case, actor Actor) (*Overview, error) {
	v, err := e.View(c, actor, "")
	if err != nil {
		return nil, err
	}
	staff := e.staff(actor)
	o := &Overview{UI: e.uiFor("overview", nil), Case: v.Case, Stage: v.Stage, Progress: v.Progress, Work: v.Work, Fields: []SummaryField{}, Log: []LogEntry{}, Certificates: v.Certificates}
	if cur, ok := e.C.Stage(c.Stage); ok {
		if ss := c.Stages[c.Stage]; ss != nil {
			o.Stage = StageView{Name: cur.Name, Title: cur.Title, Kind: cur.Kind, Description: cur.Description, Status: ss.Status, DueAt: ss.DueAt,
				Current: true, RequireStart: cur.RequireStart, StartedAt: ss.StartedAt}
			if ss.StartedBy != "" && (staff || actor.ID == ss.StartedBy) {
				o.Stage.StartedBy = ss.StartedBy
			}
		}
	}
	if c.Status == CaseRequiresInformation {
		o.Information, o.InformationCanAnswer = v.Information, v.InformationCanAnswer
	}
	if cur, ok := e.C.Stage(c.Stage); ok && c.Stages[c.Stage] != nil {
		cs := c.Stages[c.Stage]
		if cur.Entries != nil {
			o.Entries = e.entriesView(cur, cs, actor, "")
		}
		if cur.External != nil && (staff || e.CanView(c, cur, actor) || isApplicant(c, actor)) {
			o.Party = e.partyView(c, cur, cs, actor)
		}
	}
	o.EntryRows = e.entryRows(c, actor, staff)
	o.CanWork = v.CanEdit || len(v.Actions) > 0
	for _, n := range v.Nodes {
		if len(n.Operations) > 0 {
			o.CanWork = true
		}
	}
	if o.Work != nil {
		for _, op := range o.Work.Operations {
			if op == "start" {
				o.NeedsStart = true
			}
		}
	}
	o.NeedsStart = o.NeedsStart && o.Stage.RequireStart && v.Stage.Name == c.Stage

	// The data gathered so far, from every stage the viewer may see.
	seen := map[string]bool{}
	for _, s := range e.C.Def.Stages {
		sv, err := e.View(c, actor, s.Name)
		if err != nil || sv.Page == nil {
			continue
		}
		for _, g := range sv.Page.Groups {
			for _, f := range g.Forms {
				if f.Repeatable {
					if len(f.Entries) > 0 && !seen[f.Name] {
						seen[f.Name] = true
						o.Fields = append(o.Fields, SummaryField{Stage: titleOr(s.Title, s.Name), Group: titleOr(g.Title, g.Name), Path: f.Name, Label: titleOr(f.Title, f.Name), Value: f.Entries})
					}
					continue
				}
				for _, iv := range f.Inputs {
					if iv.Value == nil || iv.Sealed || seen[iv.Path] {
						continue
					}
					seen[iv.Path] = true
					o.Fields = append(o.Fields, SummaryField{Stage: titleOr(s.Title, s.Name), Group: titleOr(g.Title, g.Name), Path: iv.Path, Label: iv.Label, Value: iv.Value, Masked: iv.Masked})
				}
			}
		}
	}

	now := e.now()
	for _, vis := range c.Timeline {
		st, _ := e.C.Stage(vis.Stage)
		title := vis.Stage
		if st != nil {
			title = titleOr(st.Title, st.Name)
		}
		end := now
		if vis.LeftAt != nil {
			end = *vis.LeftAt
		}
		le := LogEntry{Stage: vis.Stage, Title: title, EnteredAt: vis.EnteredAt, StartedAt: vis.StartedAt, LeftAt: vis.LeftAt, Outcome: vis.Outcome,
			DueAt: vis.DueAt, Breached: vis.Breached, Open: vis.LeftAt == nil, HoldSeconds: vis.SuspendedSeconds, TotalSeconds: int64(end.Sub(vis.EnteredAt).Seconds())}
		if vis.StartedAt != nil {
			le.WaitSeconds = int64(vis.StartedAt.Sub(vis.EnteredAt).Seconds())
			le.WorkSeconds = max(0, int64(end.Sub(*vis.StartedAt).Seconds())-vis.SuspendedSeconds)
		} else {
			le.WaitSeconds = le.TotalSeconds
		}
		if staff {
			le.StartedBy, le.Assignee = vis.StartedBy, vis.Assignee
		}
		o.Log = append(o.Log, le)
	}
	if staff {
		o.Breaches = c.Breaches
	}
	return o, nil
}

// EntryRow is one data entry as the overview shows it.
type EntryRow struct {
	Stage       string         `json:"stage"`
	StageTitle  string         `json:"stage_title"`
	ID          string         `json:"id"`
	By          string         `json:"by,omitempty"`
	Mine        bool           `json:"mine"`
	Seq         int            `json:"seq"`
	Status      string         `json:"status"`
	SubmittedAt *time.Time     `json:"submitted_at,omitempty"`
	Fields      []SummaryField `json:"fields"`
}

// entryRows lists the entries the viewer may read: their own, and for staff
// everyone's submitted ones. Drafts stay private to their maker.
func (e *Engine) entryRows(c *Case, actor Actor, staff bool) []EntryRow {
	var out []EntryRow
	for _, st := range e.C.Def.Stages {
		ss := c.Stages[st.Name]
		if st.Entries == nil || ss == nil {
			continue
		}
		stp, _ := e.C.Stage(st.Name)
		for i := range ss.Entries {
			en := &ss.Entries[i]
			mine := actor.ID != "" && en.By == actor.ID
			if !mine && !(staff && en.Status == EntrySubmitted && e.CanView(c, stp, actor)) {
				continue
			}
			row := EntryRow{Stage: st.Name, StageTitle: titleOr(st.Title, st.Name), ID: en.ID, Mine: mine, Seq: en.Seq, Status: en.Status, SubmittedAt: en.SubmittedAt, Fields: []SummaryField{}}
			if staff {
				row.By = en.By
			}
			if v, err := e.View(e.shadow(c, stp, en), actor, st.Name); err == nil && v.Page != nil {
				for _, g := range v.Page.Groups {
					for _, f := range g.Forms {
						for _, iv := range f.Inputs {
							if iv.Value != nil && !iv.Sealed {
								row.Fields = append(row.Fields, SummaryField{Stage: row.StageTitle, Group: titleOr(g.Title, g.Name), Path: iv.Path, Label: iv.Label, Value: iv.Value, Masked: iv.Masked})
							}
						}
					}
				}
			}
			out = append(out, row)
		}
	}
	return out
}
