package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Entry statuses.
const (
	EntryDraft     = "draft"
	EntrySubmitted = "submitted"
)

// DataEntry is one record entered on a stage that collects entries. It holds
// the values of the stage's own forms; the rest of the case is untouched until
// the stage completes and one entry is merged into the case data.
type DataEntry struct {
	ID          string         `json:"id"`
	By          string         `json:"by"`
	Seq         int            `json:"seq"`
	Status      string         `json:"status"`
	Data        map[string]any `json:"data"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	SubmittedAt *time.Time     `json:"submitted_at,omitempty"`
	Edits       int            `json:"edits,omitempty"`
}

// EntriesView tells a viewer how a stage's entries stand.
type EntriesView struct {
	PerUser    int  `json:"per_user"`
	Min        int  `json:"min"`
	Max        int  `json:"max,omitempty"`
	AllowEdit  bool `json:"allow_edit"`
	AllowDraft bool `json:"allow_draft"`
	// Mine are the viewer's entries. Others counts what other people
	// submitted; staff of the stage see those entries in the overview.
	Mine      []DataEntry `json:"mine"`
	Submitted int         `json:"submitted"`
	Drafts    int         `json:"drafts"`
	// CanAdd: the viewer may start another entry. Remaining is how many more
	// they may make (-1 for no limit). CanComplete: enough entries are in.
	CanAdd      bool `json:"can_add"`
	Remaining   int  `json:"remaining"`
	CanComplete bool `json:"can_complete"`
	// Editing is the entry whose values the page shows ("" for a new one).
	Editing string `json:"editing,omitempty"`
}

func (s *EntriesSpec) need() int { return max(1, s.Min) }

func (ss *StageState) countEntries(by string) (mine, submitted, drafts int) {
	for _, en := range ss.Entries {
		if en.Status == EntrySubmitted {
			submitted++
		} else {
			drafts++
		}
		if en.By == by {
			mine++
		}
	}
	return
}

func (ss *StageState) entry(id string) (int, *DataEntry) {
	for i := range ss.Entries {
		if ss.Entries[i].ID == id {
			return i, &ss.Entries[i]
		}
	}
	return -1, nil
}

// entryForms are the forms a stage's entries hold: those editable on its page.
func (e *Engine) entryForms(st *Stage) []string {
	var out []string
	if st.Page != nil {
		for _, g := range st.Page.Groups {
			if g.Mode == ModeReadonly || g.Mode == ModeHidden || g.Mode == ModeSummary {
				continue
			}
			for _, f := range g.Forms {
				if !slices.Contains(out, f) {
					out = append(out, f)
				}
			}
		}
	}
	return out
}

// shadow is a scratch copy of the case whose data shows one entry's values (or
// none, for a new entry) in the stage's forms. Validation and rendering run on
// it, so entries reuse every rule of an ordinary stage.
func (e *Engine) shadow(c *Case, st *Stage, en *DataEntry) *Case {
	sc := c.Clone()
	sc.entryShadow = true
	for _, f := range e.entryForms(st) {
		delete(sc.Data, f)
		if en != nil {
			if v, ok := en.Data[f]; ok {
				sc.Data[f] = cloneAny(v)
			}
		}
	}
	return sc
}

func cloneAny(v any) any {
	out := cloneData(map[string]any{"v": v})
	return out["v"]
}

// SaveEntry creates or changes an entry and, with submit, hands it in. id is
// empty for a new entry. The viewer must be able to act on the stage; the
// stage's limits (per person, in all, drafts, edits) are enforced.
func (e *Engine) SaveEntry(_ context.Context, in *Case, actor Actor, stage, id string, data map[string]any, submit bool) (*Case, *DataEntry, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, nil, err
	}
	spec := st.Entries
	if spec == nil {
		return nil, nil, badState("stage %q does not collect entries", stage)
	}
	if actor.Link != nil || actor.ID == "" || !e.CanAct(c, st, actor) {
		return nil, nil, forbidden("you may not enter data at stage %q", stage)
	}
	if err := e.guardClaim(c, st, ss, actor); err != nil {
		return nil, nil, err
	}
	now := e.now()
	var cur *DataEntry
	if id != "" {
		_, cur = ss.entry(id)
		if cur == nil {
			return nil, nil, fmt.Errorf("%w: entry %q", ErrNotFound, id)
		}
		if cur.By != actor.ID {
			return nil, nil, forbidden("an entry can only be changed by the person who made it")
		}
		if cur.Status == EntrySubmitted && !spec.AllowEdit {
			return nil, nil, badState("submitted entries cannot be edited on this stage")
		}
	} else {
		mine, _, _ := ss.countEntries(actor.ID)
		if spec.PerUser > 0 && mine >= spec.PerUser {
			return nil, nil, badState("you may make at most %d %s on this stage", spec.PerUser, plural(spec.PerUser, "entry", "entries"))
		}
		if spec.Max > 0 && len(ss.Entries) >= spec.Max {
			return nil, nil, badState("this stage already has the most entries it takes (%d)", spec.Max)
		}
	}
	if !submit && !spec.AllowDraft && (cur == nil || cur.Status != EntrySubmitted) {
		return nil, nil, badState("drafts are not kept on this stage: submit the entry")
	}
	sc := e.shadow(c, st, cur)
	if _, err := e.saveInto(sc, actor, stage, data); err != nil {
		return nil, nil, err
	}
	// A submitted entry (or a resubmitted edit) must be complete and pass the rules.
	if submit || (cur != nil && cur.Status == EntrySubmitted) {
		missing := e.missingRequired(sc, st, sc.Stages[stage], actor)
		if len(missing) > 0 {
			return nil, nil, &ValidationError{Message: "the entry is incomplete", Fields: missing}
		}
		if broken := e.brokenRules(sc, st, sc.Stages[stage], actor); len(broken) > 0 {
			return nil, nil, &ValidationError{Message: "the entry does not pass its checks", Fields: broken}
		}
	}
	values := map[string]any{}
	for _, f := range e.entryForms(st) {
		if v, ok := sc.Data[f]; ok {
			values[f] = cloneAny(v)
		}
	}
	verb := "entry_saved"
	if cur == nil {
		ss.Entries = append(ss.Entries, DataEntry{ID: randomID(), By: actor.ID, Seq: len(ss.Entries) + 1, Status: EntryDraft, CreatedAt: now})
		cur = &ss.Entries[len(ss.Entries)-1]
	} else {
		cur.Edits++
	}
	cur.Data, cur.UpdatedAt = values, now
	if submit || cur.Status == EntrySubmitted {
		if cur.Status != EntrySubmitted {
			cur.SubmittedAt = &now
			verb = "entry_submitted"
		}
		cur.Status = EntrySubmitted
	}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: verb, Comment: fmt.Sprintf("entry %d", cur.Seq)})
	c.emit("entry."+strings.TrimPrefix(verb, "entry_"), stage, actor.ID, now, map[string]any{"entry": cur.ID, "seq": cur.Seq, "status": cur.Status})
	c.UpdatedAt = now
	out := *cur
	return c, &out, nil
}

// DeleteEntry removes an entry its owner may still change (or any entry, for
// the stage's admins).
func (e *Engine) DeleteEntry(_ context.Context, in *Case, actor Actor, stage, id string) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	spec := st.Entries
	if spec == nil {
		return nil, badState("stage %q does not collect entries", stage)
	}
	i, en := ss.entry(id)
	if en == nil {
		return nil, fmt.Errorf("%w: entry %q", ErrNotFound, id)
	}
	switch {
	case isAdmin(st, actor) && actor.ID != "system":
	case en.By != actor.ID || actor.Link != nil:
		return nil, forbidden("an entry can only be removed by the person who made it")
	case en.Status == EntrySubmitted && !spec.AllowEdit:
		return nil, badState("submitted entries cannot be removed on this stage")
	}
	now := e.now()
	ss.Entries = slices.Delete(ss.Entries, i, i+1)
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: stage, Action: "entry_deleted", Comment: fmt.Sprintf("entry %d", en.Seq)})
	c.emit("entry.deleted", stage, actor.ID, now, map[string]any{"entry": en.ID, "seq": en.Seq})
	c.UpdatedAt = now
	return c, nil
}

// entriesView describes the stage's entries to the viewer.
func (e *Engine) entriesView(st *Stage, ss *StageState, actor Actor, editing string) *EntriesView {
	spec := st.Entries
	v := &EntriesView{PerUser: spec.PerUser, Min: spec.need(), Max: spec.Max, AllowEdit: spec.AllowEdit, AllowDraft: spec.AllowDraft, Mine: []DataEntry{}, Editing: editing}
	mine, submitted, drafts := ss.countEntries(actor.ID)
	v.Submitted, v.Drafts = submitted, drafts
	for _, en := range ss.Entries {
		if en.By == actor.ID {
			v.Mine = append(v.Mine, en)
		}
	}
	v.Remaining = -1
	if spec.PerUser > 0 {
		v.Remaining = max(0, spec.PerUser-mine)
	}
	if spec.Max > 0 {
		room := max(0, spec.Max-len(ss.Entries))
		if v.Remaining < 0 || room < v.Remaining {
			v.Remaining = room
		}
	}
	v.CanAdd = v.Remaining != 0 && actor.ID != ""
	v.CanComplete = submitted >= spec.need()
	return v
}

// mergeEntries writes the chosen submitted entry into the case data when the
// stage completes; the entries themselves stay on the stage.
func (e *Engine) mergeEntries(c *Case, st *Stage, ss *StageState, now time.Time) {
	spec := st.Entries
	if spec == nil || spec.Merge == "none" {
		return
	}
	var pick *DataEntry
	for i := range ss.Entries {
		en := &ss.Entries[i]
		if en.Status != EntrySubmitted {
			continue
		}
		if pick == nil || spec.Merge != "first" {
			pick = en
		}
	}
	if pick == nil {
		return
	}
	for form, v := range pick.Data {
		c.Data[form] = cloneAny(v)
	}
	c.History = append(c.History, Entry{At: now, Actor: "system", Stage: st.Name, Action: "entries_merged", Comment: fmt.Sprintf("entry %d of %d", pick.Seq, len(ss.Entries))})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ViewEntry renders a stage for one entry: the page shows that entry's values
// ("" or "new" for a blank one) and is editable only when the viewer may
// change it. Stages that do not collect entries render as View does.
func (e *Engine) ViewEntry(c *Case, actor Actor, stage, entryID string) (*View, error) {
	var first *View
	if stage == "" {
		// The default stage is whichever one the viewer is shown (an applicant
		// sees the public stage they filled in).
		v, err := e.View(c, actor, "")
		if err != nil {
			return nil, err
		}
		stage, first = v.Stage.Name, v
	}
	st, ok := e.C.Stage(stage)
	if !ok || st.Entries == nil {
		if first != nil {
			return first, nil
		}
		return e.View(c, actor, stage)
	}
	ss := c.Stages[stage]
	if ss == nil {
		ss = &StageState{Status: StagePending}
	}
	var en *DataEntry
	if entryID != "" && entryID != "new" {
		if _, en = ss.entry(entryID); en == nil {
			return nil, fmt.Errorf("%w: entry %q", ErrNotFound, entryID)
		}
		if en.By != actor.ID && (en.Status != EntrySubmitted || !e.staff(actor)) {
			return nil, forbidden("you may not view this entry")
		}
	}
	v, err := e.View(e.shadow(c, st, en), actor, stage)
	if err != nil {
		return nil, err
	}
	ev := e.entriesView(st, ss, actor, entryID)
	v.Entries = ev
	canChange := (en == nil && ev.CanAdd) || (en != nil && en.By == actor.ID && (en.Status == EntryDraft || st.Entries.AllowEdit))
	if !canChange && v.Page != nil {
		v.CanEdit = false
		for gi := range v.Page.Groups {
			for fi := range v.Page.Groups[gi].Forms {
				ins := v.Page.Groups[gi].Forms[fi].Inputs
				for ii := range ins {
					ins[ii].Editable = false
				}
			}
		}
	}
	return v, nil
}

// entryFormsOf is entryForms for the compiler.
func (c *Compiled) entryFormsOf(st *Stage) []string { return (&Engine{C: c}).entryForms(st) }
