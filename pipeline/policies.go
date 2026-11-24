package pipeline

import (
	"slices"
	"time"
)

// The workflow sets defaults for how information requests, releases and starts
// behave; a stage may override any of them. These resolve the effective policy
// of a stage with every default filled in.

func (e *Engine) infoPolicy(st *Stage) InfoPolicy {
	var p InfoPolicy
	for _, src := range []*InfoPolicy{e.C.Def.Information, st.Information} {
		if src == nil {
			continue
		}
		if len(src.To) > 0 {
			p.To = src.To
		}
		if src.SLA != "" {
			p.SLA = src.SLA
		}
		if src.Due != "" {
			p.Due = src.Due
		}
		if src.OnTimeout != "" {
			p.OnTimeout = src.OnTimeout
		}
		p.Fields = p.Fields || src.Fields
	}
	if len(p.To) == 0 {
		p.To = []string{"creator"}
	}
	if p.SLA == "" {
		p.SLA = "pause"
	}
	if p.OnTimeout == "" {
		p.OnTimeout = "notify"
	}
	return p
}

func (e *Engine) releasePolicy(st *Stage) ReleasePolicy {
	var p ReleasePolicy
	for _, src := range []*ReleasePolicy{e.C.Def.Release, st.Release} {
		if src == nil {
			continue
		}
		if len(src.Targets) > 0 {
			p.Targets = src.Targets
		}
		if src.Assignee != "" {
			p.Assignee = src.Assignee
		}
		p.RestoreData = p.RestoreData || src.RestoreData
	}
	if len(p.Targets) == 0 {
		p.Targets = []string{"same", "previous"}
	}
	if p.Assignee == "" {
		p.Assignee = "optional"
	}
	return p
}

func (e *Engine) startPolicy(st *Stage) StartPolicy {
	var p StartPolicy
	for _, src := range []*StartPolicy{e.C.Def.Start, st.Start} {
		if src == nil {
			continue
		}
		if src.OnCreate != "" {
			p.OnCreate = src.OnCreate
		}
		if src.OnReturn != "" {
			p.OnReturn = src.OnReturn
		}
	}
	if p.OnCreate == "" {
		p.OnCreate = "auto"
	}
	if p.OnReturn == "" {
		p.OnReturn = "keep"
	}
	return p
}

// holdPausesSLA reports whether a hold or an information request stops the
// stage's deadline clock.
func holdPausesSLA(st *Stage) bool { return st.SLA == nil || st.SLA.OnHold != "run" }

// unpauseSLA restarts a stopped deadline clock: the deadline (and warning) move
// by the working time spent stopped since from.
func (e *Engine) unpauseSLA(st *Stage, ss *StageState, from, now time.Time) {
	if ss.SLA == nil || ss.SLA.Status != SLAPaused {
		return
	}
	var cal *WorkCalendar
	if st.SLA != nil {
		cal = e.calendar(st.SLA.Calendar)
	}
	shift := cal.Between(from, now)
	ss.SLA.DueAt = cal.Add(ss.SLA.DueAt, shift)
	if ss.SLA.WarnAt != nil {
		w := cal.Add(*ss.SLA.WarnAt, shift)
		ss.SLA.WarnAt = &w
	}
	ss.SLA.Status = SLAOnTrack
	if ss.SLA.WarnAt != nil && !now.Before(*ss.SLA.WarnAt) {
		ss.SLA.Status = SLAWarning
	}
	due := ss.SLA.DueAt
	ss.DueAt = &due
}

// allows reports whether list names target.
func allows(list []string, target string) bool { return slices.Contains(list, target) }
