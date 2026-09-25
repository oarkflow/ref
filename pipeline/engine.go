package pipeline

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Engine applies operations to cases of one compiled pipeline. It is safe for
// concurrent use; every operation works on a clone of the case it is given.
type Engine struct {
	C          *Compiled
	Eval       Evaluator
	Automation Automation
	// StageHook runs a stage's on_enter / on_complete hook, if set.
	StageHook func(ctx context.Context, hook string, c *Case, stage string) error
	// SigningKey signs issued certificates (HMAC-SHA256). Without it
	// certificates carry a content hash only.
	SigningKey []byte
	Now        func() time.Time
	NewID      func() string
	// Lookup resolves an input's lookup set (reference data) into options for
	// the case. When set, submitted values must be one of them.
	Lookup func(set string, c *Case) []Option
}

// Option is one choice of a lookup-backed input.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// checkLookup validates a coerced value against the input's lookup set.
func (e *Engine) checkLookup(c *Case, in Input, v any) (string, string) {
	if in.Lookup == "" || e.Lookup == nil || v == nil {
		return "", ""
	}
	valid := map[string]bool{}
	for _, o := range e.Lookup(in.Lookup, c) {
		valid[o.Value] = true
	}
	values := []any{v}
	if list, ok := v.([]any); ok {
		values = list
	}
	for _, item := range values {
		if s := fmt.Sprint(item); !valid[s] {
			return "option", fmt.Sprintf("%q is not a valid choice for %s", s, orDefault(in.Label, in.Name))
		}
	}
	return "", ""
}

// NewEngine returns an engine for c.
func NewEngine(c *Compiled) *Engine { return &Engine{C: c} }

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) newID(prefix string) string {
	if e.NewID != nil {
		return e.NewID()
	}
	return prefix + "_" + randomID()
}

// StartOptions seeds a new case.
type StartOptions struct {
	ID       string
	Number   string
	TenantID string
	OrgUnit  string
	// Data is saved into the first stage as the actor would save it.
	Data map[string]any
}

// Start creates a case and enters the first stage.
func (e *Engine) Start(ctx context.Context, actor Actor, opts StartOptions) (*Case, error) {
	first := &e.C.Def.Stages[0]
	now := e.now()
	c := &Case{
		ID:        opts.ID,
		Number:    opts.Number,
		Pipeline:  e.C.Def.Name,
		TenantID:  opts.TenantID,
		OrgUnit:   opts.OrgUnit,
		Status:    CaseDraft,
		CreatedBy: actor.ID,
		Data:      map[string]any{},
		Stages:    map[string]*StageState{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if c.ID == "" {
		c.ID = e.newID("case")
	}
	for _, st := range e.C.Def.Stages {
		c.Stages[st.Name] = &StageState{Status: StagePending}
	}
	if !first.Public && !actor.HasAnyRole(first.Roles) {
		return nil, forbidden("you may not start a %s", e.C.Def.Name)
	}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Action: "start", To: first.Name})
	if err := e.enterStage(ctx, c, first.Name, actor, 0); err != nil {
		return nil, err
	}
	c.Status = CaseDraft
	if len(opts.Data) > 0 {
		if _, err := e.saveInto(c, actor, first.Name, opts.Data); err != nil {
			return nil, err
		}
		e.refreshChecks(c, first.Name)
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Environment and permissions
// ---------------------------------------------------------------------------

// Env is the expression environment for a case: every form's data at the top
// level (applicant.age), plus data, case, actor and stages.
func (e *Engine) Env(c *Case, actor Actor) map[string]any {
	env := make(map[string]any, len(c.Data)+4)
	maps.Copy(env, c.Data)
	env["data"] = c.Data
	env["case"] = map[string]any{
		"id": c.ID, "number": c.Number, "status": c.Status, "stage": c.Stage,
		"created_by": c.CreatedBy, "org_unit": c.OrgUnit, "tenant_id": c.TenantID,
	}
	roles := make([]any, len(actor.Roles))
	for i, r := range actor.Roles {
		roles[i] = r
	}
	env["actor"] = map[string]any{"id": actor.ID, "roles": roles, "claims": actor.Claims, "is_applicant": actor.ID != "" && actor.ID == c.CreatedBy}
	stages := map[string]any{}
	for name, ss := range c.Stages {
		nodes := map[string]any{}
		for n, ns := range ss.Nodes {
			nodes[n] = ns.Status
		}
		stages[name] = map[string]any{"status": ss.Status, "nodes": nodes}
	}
	env["stages"] = stages
	return env
}

func (e *Engine) cond(expr string, env map[string]any) (bool, error) {
	if strings.TrimSpace(expr) == "" {
		return true, nil
	}
	if e.Eval == nil {
		return false, fmt.Errorf("pipeline: expression %q needs an evaluator", expr)
	}
	v, err := e.Eval.Eval(expr, env)
	if err != nil {
		return false, fmt.Errorf("pipeline: %q: %w", expr, err)
	}
	return truthy(v), nil
}

func isApplicant(c *Case, actor Actor) bool { return actor.ID != "" && actor.ID == c.CreatedBy }

// CanAct reports whether the actor may work on a stage.
func (e *Engine) CanAct(c *Case, st *Stage, actor Actor) bool {
	return actor.HasAnyRole(st.Roles) || (st.Public && isApplicant(c, actor))
}

// CanView reports whether the actor may see a stage's page.
func (e *Engine) CanView(c *Case, st *Stage, actor Actor) bool {
	return e.CanAct(c, st, actor) || actor.HasAnyRole(st.ViewRoles) || actor.HasAnyRole(e.C.Def.RevealRoles)
}

func (e *Engine) canActNode(c *Case, st *Stage, n *Node, actor Actor) bool {
	if len(n.Roles) > 0 {
		return actor.HasAnyRole(n.Roles)
	}
	return e.CanAct(c, st, actor)
}

// ---------------------------------------------------------------------------
// Stage lifecycle
// ---------------------------------------------------------------------------

func (e *Engine) stageOpen(c *Case, stage string) (*Stage, *StageState, error) {
	if c.Terminal() {
		return nil, nil, badState("the case is %s", c.Status)
	}
	st, ok := e.C.Stage(stage)
	if !ok {
		return nil, nil, fmt.Errorf("%w: stage %q", ErrNotFound, stage)
	}
	ss := c.Stages[stage]
	if c.Stage != stage || ss == nil || (ss.Status != StageActive && ss.Status != StageReturned) {
		return nil, nil, badState("stage %q is not open (the case is at %q)", stage, c.Stage)
	}
	return st, ss, nil
}

// enterStage opens a stage: requirements, skip, node initialisation, checks,
// automation and the on_enter hook. depth bounds chains of skipped stages.
func (e *Engine) enterStage(ctx context.Context, c *Case, name string, actor Actor, depth int) error {
	if depth > len(e.C.Def.Stages) {
		return fmt.Errorf("pipeline: stage transitions loop at %q", name)
	}
	st, _ := e.C.Stage(name)
	ss := c.Stages[name]
	if ss == nil {
		ss = &StageState{}
		c.Stages[name] = ss
	}
	for _, r := range st.Requires {
		if rs := c.Stages[r]; rs == nil || (rs.Status != StageCompleted && rs.Status != StageSkipped) {
			return badState("stage %q requires %q to be completed first", name, r)
		}
	}
	env := e.Env(c, actor)
	skip, err := e.cond(st.SkipIf, env)
	if err != nil {
		return err
	}
	if st.SkipIf != "" && skip {
		ss.Status = StageSkipped
		c.History = append(c.History, Entry{At: e.now(), Actor: actor.ID, Stage: name, Action: "skip"})
		next := e.nextStage(st)
		if next == "" {
			c.Status = CaseCompleted
			return nil
		}
		return e.enterStage(ctx, c, next, actor, depth+1)
	}
	for _, cond := range st.EntryConditions {
		ok, err := e.cond(cond, env)
		if err != nil {
			return err
		}
		if !ok {
			return badState("stage %q cannot open: %s", name, cond)
		}
	}
	now := e.now()
	ss.Status = StageActive
	ss.EnteredAt = &now
	ss.CompletedAt = nil
	ss.Visits++
	if d, _ := parseDuration(st.Due); d > 0 {
		due := now.Add(d)
		ss.DueAt = &due
	}
	c.Stage = name
	if c.Status != CaseDraft || ss.Visits > 1 || name != e.C.Def.Stages[0].Name {
		c.Status = CaseInProgress
	}
	e.initNodes(c, st, ss, env)
	if err := e.runAutomated(ctx, c, st, ss); err != nil {
		return err
	}
	e.refreshChecks(c, name)
	if st.OnEnter != "" && e.StageHook != nil {
		if err := e.StageHook(ctx, st.OnEnter, c, name); err != nil {
			return fmt.Errorf("pipeline: stage %q on_enter: %w", name, err)
		}
	}
	if st.AutoAdvance && e.nodesSatisfied(st, ss) {
		return e.completeStage(ctx, c, name, actor, "", depth+1)
	}
	return nil
}

// initNodes (re)initialises node states on entry. A review node re-entered
// after a correction keeps its verified verdicts and forgets the flagged ones,
// so the reviewer re-checks only what changed.
func (e *Engine) initNodes(c *Case, st *Stage, ss *StageState, env map[string]any) {
	if ss.Nodes == nil {
		ss.Nodes = map[string]*NodeState{}
	}
	for i := range st.Nodes {
		n := &st.Nodes[i]
		prev := ss.Nodes[n.Name]
		ns := &NodeState{Status: NodePending}
		if prev != nil && n.Kind == NodeReview && len(prev.Verdicts) > 0 {
			ns.Verdicts = map[string]Verdict{}
			for path, v := range prev.Verdicts {
				if v.Status == VerdictVerified {
					ns.Verdicts[path] = v
				}
			}
		}
		if prev != nil && n.Kind == NodeCertificate && prev.Status == NodePassed {
			ns = prev // an issued certificate stays issued
		}
		if applies, err := e.cond(n.AppliesIf, env); err == nil && !applies {
			ns.Status = NodeSkipped
		}
		ss.Nodes[n.Name] = ns
	}
}

func (e *Engine) runAutomated(ctx context.Context, c *Case, st *Stage, ss *StageState) error {
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if n.Kind == NodeCertificate && n.Auto && ss.Nodes[n.Name].Status == NodePending {
			cert, err := e.issue(c, n.Certificate, Actor{ID: "system"})
			if err != nil {
				return err
			}
			ns := ss.Nodes[n.Name]
			now := e.now()
			ns.UpdatedAt = &now
			ns.Result = map[string]any{"certificate_id": cert.ID, "number": cert.Number}
			ns.Status = NodePassed
			continue
		}
		if n.Kind != NodeAutomated || ss.Nodes[n.Name].Status != NodePending || e.Automation == nil {
			continue
		}
		if err := e.runNode(ctx, c, st.Name, n, ss.Nodes[n.Name]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runNode(ctx context.Context, c *Case, stage string, n *Node, ns *NodeState) error {
	result, passed, err := e.Automation.RunNode(ctx, n.Hook, c, stage, n.Name)
	now := e.now()
	ns.UpdatedAt = &now
	if err != nil {
		ns.Status = NodeFailed
		ns.Comment = err.Error()
		c.History = append(c.History, Entry{At: now, Stage: stage, Node: n.Name, Action: "automation_failed", Comment: err.Error()})
		return nil
	}
	ns.Result = result
	ns.Status = NodeFailed
	if passed {
		ns.Status = NodePassed
	}
	c.History = append(c.History, Entry{At: now, Stage: stage, Node: n.Name, Action: "automation", To: ns.Status})
	return nil
}

// refreshChecks re-evaluates a stage's check nodes against current data.
func (e *Engine) refreshChecks(c *Case, stage string) {
	st, _ := e.C.Stage(stage)
	ss := c.Stages[stage]
	if st == nil || ss == nil {
		return
	}
	env := e.Env(c, Actor{})
	for i := range st.Nodes {
		n := &st.Nodes[i]
		ns := ss.Nodes[n.Name]
		if n.Kind != NodeCheck || ns == nil || ns.Status == NodeSkipped || ns.Status == NodeWaived {
			continue
		}
		ok, err := e.cond(n.Check, env)
		switch {
		case err != nil:
			ns.Status, ns.Comment = NodeFailed, err.Error()
		case ok:
			ns.Status, ns.Comment = NodePassed, ""
		default:
			ns.Status = NodeFailed
		}
	}
}

// nodesSatisfied applies the stage's completion rule.
func (e *Engine) nodesSatisfied(st *Stage, ss *StageState) bool {
	done, required, requiredDone := 0, 0, 0
	for _, n := range st.Nodes {
		ns := ss.Nodes[n.Name]
		if ns == nil || ns.Status == NodeSkipped {
			continue
		}
		ok := ns.Status == NodePassed || ns.Status == NodeWaived
		if ok {
			done++
		}
		if !n.Optional {
			required++
			if ok {
				requiredDone++
			}
		}
	}
	switch st.Complete {
	case "any":
		return done > 0 || (required == 0 && len(st.Nodes) == 0)
	case "quorum":
		return done >= st.Quorum
	default:
		return requiredDone == required
	}
}

// OpenNodes lists required nodes that are not yet passed or waived.
func (e *Engine) OpenNodes(c *Case, stage string) []string {
	st, _ := e.C.Stage(stage)
	ss := c.Stages[stage]
	var open []string
	if st == nil || ss == nil {
		return nil
	}
	for _, n := range st.Nodes {
		ns := ss.Nodes[n.Name]
		if n.Optional || ns == nil || ns.Status == NodeSkipped || ns.Status == NodePassed || ns.Status == NodeWaived {
			continue
		}
		open = append(open, n.Name)
	}
	return open
}

func (e *Engine) nextStage(st *Stage) string {
	if st.Next != "" {
		return st.Next
	}
	i := e.C.stageIndex(st.Name)
	if i >= 0 && i+1 < len(e.C.Def.Stages) {
		return e.C.Def.Stages[i+1].Name
	}
	return ""
}

// completeStage closes a stage and opens the next one.
func (e *Engine) completeStage(ctx context.Context, c *Case, name string, actor Actor, next string, depth int) error {
	st, _ := e.C.Stage(name)
	ss := c.Stages[name]
	now := e.now()
	ss.Status = StageCompleted
	ss.CompletedAt = &now
	ss.CompletedBy = actor.ID
	env := e.Env(c, actor)
	for _, as := range st.Assign {
		if e.Eval == nil {
			return fmt.Errorf("pipeline: stage %q assign needs an evaluator", name)
		}
		v, err := e.Eval.Eval(as.Value, env)
		if err != nil {
			return fmt.Errorf("pipeline: stage %q assign %s: %w", name, as.Path, err)
		}
		c.Set(as.Path, v)
	}
	if st.Certificate != "" {
		if _, err := e.issue(c, st.Certificate, actor); err != nil {
			return err
		}
	}
	if st.OnComplete != "" && e.StageHook != nil {
		if err := e.StageHook(ctx, st.OnComplete, c, name); err != nil {
			return fmt.Errorf("pipeline: stage %q on_complete: %w", name, err)
		}
	}
	if next == "" && ss.ReturnedFrom != "" {
		// A corrected submission goes straight back to whoever returned it.
		next = ss.ReturnedFrom
	}
	ss.ReturnedFrom = ""
	ss.Flags = nil
	if next == "" {
		next = e.nextStage(st)
	}
	if next == "" {
		c.Status = CaseCompleted
		return nil
	}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Action: "transition", From: name, To: next})
	return e.enterStage(ctx, c, next, actor, depth)
}

// ---------------------------------------------------------------------------
// Data
// ---------------------------------------------------------------------------

// editable returns the inputs the actor may write at a stage, as
// form -> input name -> Input. Only editable, visible groups of an open stage
// the actor may act on count; while the stage is returned for correction only
// the flagged inputs do.
func (e *Engine) editable(c *Case, st *Stage, ss *StageState, actor Actor) map[string]map[string]Input {
	out := map[string]map[string]Input{}
	if st.Page == nil || !e.CanAct(c, st, actor) {
		return out
	}
	correcting := ss.Status == StageReturned && len(ss.Flags) > 0
	env := e.Env(c, actor)
	for _, g := range st.Page.Groups {
		if g.Mode != "" && g.Mode != ModeEditable {
			continue
		}
		if len(g.Roles) > 0 && !actor.HasAnyRole(g.Roles) && !(st.Public && isApplicant(c, actor)) {
			continue
		}
		if ok, err := e.cond(g.VisibleIf, env); err != nil || !ok {
			continue
		}
		for _, form := range g.Forms {
			inputs, _ := e.C.FormInputs(form)
			for _, in := range inputs {
				if ok, err := e.cond(in.VisibleIf, env); err != nil || !ok {
					continue
				}
				if correcting {
					if _, flagged := ss.Flags[form+"."+in.Name]; !flagged {
						continue
					}
				}
				if out[form] == nil {
					out[form] = map[string]Input{}
				}
				out[form][in.Name] = in
			}
		}
	}
	return out
}

// previewEditable resolves editable inputs against the case with the
// submitted values overlaid, so a conditional input (visible_if) submitted
// together with the input controlling it is accepted. Only values that are
// themselves editable feed the preview, and it iterates until stable so
// chains of conditions resolve.
func (e *Engine) previewEditable(c *Case, st *Stage, ss *StageState, actor Actor, data map[string]any) map[string]map[string]Input {
	allowed := e.editable(c, st, ss, actor)
	for range 4 {
		preview := c.Clone()
		for form, inputs := range allowed {
			if e.C.forms[form].Repeatable {
				if list, ok := data[form].([]any); ok {
					preview.Set(form, list)
				}
				continue
			}
			values, ok := data[form].(map[string]any)
			if !ok {
				continue
			}
			for name := range inputs {
				if v, ok := values[name]; ok {
					preview.Set(form+"."+name, v)
				}
			}
		}
		pss := preview.Stages[st.Name]
		if pss == nil {
			pss = ss
		}
		next := e.editable(preview, st, pss, actor)
		if sameEditable(allowed, next) {
			return next
		}
		allowed = next
	}
	return allowed
}

func sameEditable(a, b map[string]map[string]Input) bool {
	if len(a) != len(b) {
		return false
	}
	for form, inputs := range a {
		other, ok := b[form]
		if !ok || len(other) != len(inputs) {
			return false
		}
		for name := range inputs {
			if _, ok := other[name]; !ok {
				return false
			}
		}
	}
	return true
}

// saveInto merges submitted data into c, accepting only editable inputs. It
// validates everything before writing anything.
func (e *Engine) saveInto(c *Case, actor Actor, stage string, data map[string]any) ([]string, error) {
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	if !e.CanAct(c, st, actor) {
		return nil, forbidden("you may not edit stage %q", stage)
	}
	allowed := e.previewEditable(c, st, ss, actor, data)
	type write struct {
		path  string
		value any
	}
	var (
		writes []write
		errs   []FieldError
	)
	forms := make([]string, 0, len(data))
	for form := range data {
		forms = append(forms, form)
	}
	sort.Strings(forms)
	for _, form := range forms {
		inputs, ok := allowed[form]
		if !ok {
			continue // not editable here: silently ignored, never written
		}
		cf := e.C.forms[form]
		if cf.Repeatable {
			list, ok := data[form].([]any)
			if !ok {
				errs = append(errs, FieldError{Path: form, Rule: "type", Message: form + " must be a list"})
				continue
			}
			if cf.MaxItems > 0 && len(list) > cf.MaxItems {
				errs = append(errs, FieldError{Path: form, Rule: "max_items", Message: fmt.Sprintf("%s allows at most %d entries", form, cf.MaxItems)})
				continue
			}
			entries := make([]any, 0, len(list))
			for i, raw := range list {
				entry, ok := raw.(map[string]any)
				if !ok {
					errs = append(errs, FieldError{Path: fmt.Sprintf("%s.%d", form, i), Rule: "type", Message: "each entry must be an object"})
					continue
				}
				clean := map[string]any{}
				for name, value := range entry {
					in, ok := inputs[name]
					if !ok {
						continue
					}
					v, rule, msg := e.C.coerce(form, in, value)
					if msg == "" {
						rule, msg = e.checkLookup(c, in, v)
					}
					if msg != "" {
						errs = append(errs, FieldError{Path: fmt.Sprintf("%s.%d.%s", form, i, name), Rule: rule, Message: msg})
						continue
					}
					if v != nil {
						clean[name] = v
					}
				}
				entries = append(entries, clean)
			}
			writes = append(writes, write{path: form, value: entries})
			continue
		}
		values, ok := data[form].(map[string]any)
		if !ok {
			errs = append(errs, FieldError{Path: form, Rule: "type", Message: form + " must be an object"})
			continue
		}
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			in, ok := inputs[name]
			if !ok {
				continue
			}
			v, rule, msg := e.C.coerce(form, in, values[name])
			if msg == "" {
				rule, msg = e.checkLookup(c, in, v)
			}
			if msg != "" {
				errs = append(errs, FieldError{Path: form + "." + name, Rule: rule, Message: msg})
				continue
			}
			writes = append(writes, write{path: form + "." + name, value: v})
		}
	}
	if len(errs) > 0 {
		return nil, &ValidationError{Message: "some fields are invalid", Fields: errs}
	}
	var changed []string
	for _, w := range writes {
		old, had := c.Get(w.path)
		if w.value == nil {
			if had {
				e.unset(c, w.path)
				changed = append(changed, w.path)
			}
			continue
		}
		if !had || fmt.Sprint(old) != fmt.Sprint(w.value) {
			changed = append(changed, w.path)
		}
		c.Set(w.path, w.value)
	}
	return changed, nil
}

func (e *Engine) unset(c *Case, path string) {
	segs := strings.Split(path, ".")
	cur := c.Data
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, segs[len(segs)-1])
}

// Save stores a draft of a stage's data without completing it.
func (e *Engine) Save(ctx context.Context, in *Case, actor Actor, stage string, data map[string]any) (*Case, error) {
	c := in.Clone()
	changed, err := e.saveInto(c, actor, stage, data)
	if err != nil {
		return nil, err
	}
	e.refreshChecks(c, stage)
	if len(changed) > 0 {
		c.History = append(c.History, Entry{At: e.now(), Actor: actor.ID, Stage: stage, Action: "save", Changes: changed})
	}
	c.UpdatedAt = e.now()
	return c, nil
}

// missingRequired lists required inputs the stage's editable groups still lack.
func (e *Engine) missingRequired(c *Case, st *Stage, ss *StageState, actor Actor) []FieldError {
	env := e.Env(c, actor)
	var errs []FieldError
	for form, inputs := range e.editable(c, st, ss, actor) {
		cf := e.C.forms[form]
		if cf.Repeatable {
			list, _ := c.Data[form].([]any)
			if cf.MinItems > 0 && len(list) < cf.MinItems {
				errs = append(errs, FieldError{Path: form, Rule: "min_items", Message: fmt.Sprintf("%s needs at least %d entries", titleOr(cf.Title, form), cf.MinItems)})
			}
			for i, raw := range list {
				entry, _ := raw.(map[string]any)
				for _, in := range cf.inputs {
					if _, editable := inputs[in.Name]; !editable {
						continue
					}
					if e.required(in, env) && isEmpty(entry[in.Name]) {
						errs = append(errs, FieldError{Path: fmt.Sprintf("%s.%d.%s", form, i, in.Name), Rule: "required", Message: titleOr(in.Label, in.Name) + " is required"})
					}
				}
			}
			continue
		}
		for _, in := range cf.inputs {
			if _, editable := inputs[in.Name]; !editable {
				continue
			}
			v, _ := c.Get(form + "." + in.Name)
			if e.required(in, env) && isEmpty(v) {
				errs = append(errs, FieldError{Path: form + "." + in.Name, Rule: "required", Message: titleOr(in.Label, in.Name) + " is required"})
			}
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Path < errs[j].Path })
	return errs
}

func (e *Engine) required(in Input, env map[string]any) bool {
	if in.Required {
		return true
	}
	if in.RequiredIf == "" {
		return false
	}
	ok, err := e.cond(in.RequiredIf, env)
	return err == nil && ok
}

func titleOr(title, fallback string) string {
	if title != "" {
		return title
	}
	return fallback
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

// ActInput carries an action's comment and optional data to save first.
type ActInput struct {
	Comment string         `json:"comment,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	// Flags adds inputs to return for correction beyond the review verdicts.
	Flags map[string]string `json:"flags,omitempty"`
}

// defaultSubmit is used when a stage declares no actions.
var defaultSubmit = ActionSpec{Name: "submit", Label: "Submit", Outcome: OutcomeAdvance}

// Actions returns the actions the actor may take at the case's current stage.
func (e *Engine) Actions(c *Case, actor Actor) []ActionSpec {
	st, ss, err := e.stageOpen(c, c.Stage)
	if err != nil {
		return nil
	}
	env := e.Env(c, actor)
	specs := st.Actions
	if len(specs) == 0 {
		specs = []ActionSpec{defaultSubmit}
	}
	var out []ActionSpec
	for _, a := range specs {
		if e.mayTakeAction(c, st, ss, a, actor, env) {
			out = append(out, a)
		}
	}
	return out
}

func (e *Engine) mayTakeAction(c *Case, st *Stage, _ *StageState, a ActionSpec, actor Actor, env map[string]any) bool {
	if a.Outcome == OutcomeWithdraw && isApplicant(c, actor) {
		return true
	}
	allowed := e.CanAct(c, st, actor)
	if len(a.Roles) > 0 {
		allowed = actor.HasAnyRole(a.Roles) || (st.Public && isApplicant(c, actor) && slices.Contains(a.Roles, "applicant"))
	}
	if !allowed {
		return false
	}
	ok, err := e.cond(a.Condition, env)
	return err == nil && ok
}

// Act takes a stage action: advance (submit), return for correction, approve,
// reject, withdraw or hold.
func (e *Engine) Act(ctx context.Context, in *Case, actor Actor, stage, action string, input ActInput) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	spec, found := ActionSpec{}, false
	for _, a := range st.Actions {
		if a.Name == action {
			spec, found = a, true
		}
	}
	if !found && len(st.Actions) == 0 && action == defaultSubmit.Name {
		spec, found = defaultSubmit, true
	}
	if !found {
		return nil, fmt.Errorf("%w: action %q at stage %q", ErrNotFound, action, stage)
	}
	if !e.mayTakeAction(c, st, ss, spec, actor, e.Env(c, actor)) {
		return nil, forbidden("you may not %s at stage %q", action, stage)
	}
	if spec.CommentRequired && strings.TrimSpace(input.Comment) == "" {
		return nil, &ValidationError{Message: "a comment is required", Fields: []FieldError{{Path: "comment", Rule: "required", Message: "a comment is required to " + titleOr(spec.Label, action)}}}
	}
	var changed []string
	if len(input.Data) > 0 {
		if changed, err = e.saveInto(c, actor, stage, input.Data); err != nil {
			return nil, err
		}
		e.refreshChecks(c, stage)
	}
	now := e.now()
	entry := Entry{At: now, Actor: actor.ID, Stage: stage, Action: action, Comment: input.Comment, Changes: changed}
	outcome := spec.Outcome
	if outcome == "" {
		outcome = OutcomeAdvance
	}
	switch outcome {
	case OutcomeAdvance, OutcomeApprove:
		if missing := e.missingRequired(c, st, ss, actor); len(missing) > 0 {
			return nil, &ValidationError{Message: "the stage is incomplete", Fields: missing}
		}
		if !spec.SkipNodes {
			for _, n := range st.Nodes {
				if ns := ss.Nodes[n.Name]; ns != nil && ns.Status == NodeFailed && !n.Optional {
					return nil, badState("node %q failed; return or reject the case instead", n.Name)
				}
			}
			if !e.nodesSatisfied(st, ss) {
				return nil, badState("stage %q still has open work: %s", stage, strings.Join(e.OpenNodes(c, stage), ", "))
			}
		}
		c.History = append(c.History, entry)
		if err := e.completeStage(ctx, c, stage, actor, spec.Next, 0); err != nil {
			return nil, err
		}
		if outcome == OutcomeApprove {
			c.Status = CaseApproved
		}
	case OutcomeReturn:
		target := spec.ReturnTo
		if target == "" {
			target = e.C.Def.Stages[0].Name
		}
		flags := e.collectFlags(st, ss, input.Flags, actor, now)
		ts := c.Stages[target]
		ss.Status = StagePending
		ts.Status = StageReturned
		ts.Flags = flags
		ts.ReturnedFrom = stage
		ts.Visits++
		ts.EnteredAt = &now
		c.Stage = target
		c.Status = CaseReturned
		entry.From, entry.To = stage, target
		for path := range flags {
			entry.Changes = append(entry.Changes, path)
		}
		sort.Strings(entry.Changes)
		c.History = append(c.History, entry)
	case OutcomeReject:
		ss.Status = StageRejected
		c.Status = CaseRejected
		c.History = append(c.History, entry)
	case OutcomeWithdraw:
		c.Status = CaseWithdrawn
		c.History = append(c.History, entry)
	case OutcomeHold:
		c.History = append(c.History, entry)
	}
	c.UpdatedAt = now
	return c, nil
}

// collectFlags gathers every flagged verdict of the stage's review nodes plus
// any flags passed with the action.
func (e *Engine) collectFlags(st *Stage, ss *StageState, extra map[string]string, actor Actor, now time.Time) map[string]Flag {
	flags := map[string]Flag{}
	for _, n := range st.Nodes {
		ns := ss.Nodes[n.Name]
		if n.Kind != NodeReview || ns == nil {
			continue
		}
		for path, v := range ns.Verdicts {
			if v.Status == VerdictFlagged {
				flags[path] = Flag{Path: path, Comment: v.Comment, By: v.By, At: v.At}
			}
		}
	}
	for path, comment := range extra {
		flags[path] = Flag{Path: path, Comment: comment, By: actor.ID, At: now}
	}
	return flags
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

// NodeInput carries a node operation's payload.
type NodeInput struct {
	Comment string `json:"comment,omitempty"`
	// Verdicts maps "form.input" to {status, comment} for review nodes.
	Verdicts map[string]VerdictInput `json:"verdicts,omitempty"`
	Result   map[string]any          `json:"result,omitempty"`
}

// VerdictInput is one submitted verdict.
type VerdictInput struct {
	Status  string `json:"status"`
	Comment string `json:"comment,omitempty"`
}

// NodeAct performs one operation on a node: verify/complete (review),
// approve/reject (approval), complete (form, task), fail (task), run
// (automated), recheck (check), issue (certificate) or waive (any).
func (e *Engine) NodeAct(ctx context.Context, in *Case, actor Actor, stage, node, verb string, input NodeInput) (*Case, error) {
	c := in.Clone()
	st, ss, err := e.stageOpen(c, stage)
	if err != nil {
		return nil, err
	}
	var n *Node
	for i := range st.Nodes {
		if st.Nodes[i].Name == node {
			n = &st.Nodes[i]
		}
	}
	if n == nil {
		return nil, fmt.Errorf("%w: node %q at stage %q", ErrNotFound, node, stage)
	}
	ns := ss.Nodes[node]
	if ns.Status == NodeSkipped {
		return nil, badState("node %q does not apply to this case", node)
	}
	if verb == "waive" {
		if !actor.HasAnyRole(n.WaiveRoles) {
			return nil, forbidden("you may not waive %q", node)
		}
	} else if !e.canActNode(c, st, n, actor) {
		return nil, forbidden("you may not act on %q", node)
	}
	if verb != "waive" && e.violatesFourEyes(c, n, actor) {
		return nil, forbidden("four-eyes rule: you already acted on this case where %q forbids it", node)
	}
	now := e.now()
	entry := Entry{At: now, Actor: actor.ID, Stage: stage, Node: node, Action: verb, Comment: input.Comment}
	markActor := func() {
		if !slices.Contains(ns.Actors, actor.ID) {
			ns.Actors = append(ns.Actors, actor.ID)
		}
		ns.UpdatedAt = &now
		if input.Comment != "" {
			ns.Comment = input.Comment
		}
	}
	switch {
	case verb == "waive":
		ns.Status = NodeWaived
		markActor()
	case n.Kind == NodeReview && verb == "verify":
		paths := e.reviewPaths(c, n)
		if ns.Verdicts == nil {
			ns.Verdicts = map[string]Verdict{}
		}
		var errs []FieldError
		for path, v := range input.Verdicts {
			if !slices.Contains(paths, path) {
				errs = append(errs, FieldError{Path: path, Rule: "unknown", Message: path + " is not reviewed by this node"})
				continue
			}
			if v.Status != VerdictVerified && v.Status != VerdictFlagged {
				errs = append(errs, FieldError{Path: path, Rule: "verdict", Message: "verdict must be verified or flagged"})
				continue
			}
			if v.Status == VerdictFlagged && strings.TrimSpace(v.Comment) == "" {
				errs = append(errs, FieldError{Path: path, Rule: "comment", Message: "say what is wrong with " + path})
				continue
			}
			ns.Verdicts[path] = Verdict{Status: v.Status, Comment: v.Comment, By: actor.ID, At: now}
			entry.Changes = append(entry.Changes, path+"="+v.Status)
		}
		if len(errs) > 0 {
			return nil, &ValidationError{Message: "some verdicts are invalid", Fields: errs}
		}
		sort.Strings(entry.Changes)
		ns.Status = NodeInProgress
		markActor()
	case n.Kind == NodeReview && verb == "complete":
		var missing []FieldError
		flagged := false
		for _, path := range e.reviewPaths(c, n) {
			v, ok := ns.Verdicts[path]
			if !ok {
				missing = append(missing, FieldError{Path: path, Rule: "unreviewed", Message: path + " has not been reviewed"})
				continue
			}
			flagged = flagged || v.Status == VerdictFlagged
		}
		if len(missing) > 0 {
			return nil, &ValidationError{Message: "every submitted field must be verified or flagged", Fields: missing}
		}
		ns.Status = NodePassed
		if flagged {
			ns.Status = NodeFailed
		}
		entry.To = ns.Status
		markActor()
	case n.Kind == NodeApproval && verb == "approve":
		for _, a := range ns.Approvals {
			if a.By == actor.ID {
				return nil, badState("you already approved %q", node)
			}
		}
		ns.Approvals = append(ns.Approvals, Approval{By: actor.ID, At: now, Comment: input.Comment})
		need := max(1, n.Approvals)
		ns.Status = NodeInProgress
		if len(ns.Approvals) >= need {
			ns.Status = NodePassed
		}
		entry.To = fmt.Sprintf("%d/%d", len(ns.Approvals), need)
		markActor()
	case n.Kind == NodeApproval && verb == "reject":
		if strings.TrimSpace(input.Comment) == "" {
			return nil, &ValidationError{Message: "a reason is required", Fields: []FieldError{{Path: "comment", Rule: "required", Message: "say why you reject"}}}
		}
		ns.Status = NodeFailed
		markActor()
	case n.Kind == NodeForm && verb == "complete":
		var missing []FieldError
		env := e.Env(c, actor)
		for _, form := range n.Forms {
			inputs, _ := e.C.FormInputs(form)
			for _, in := range inputs {
				if ok, _ := e.cond(in.VisibleIf, env); !ok {
					continue
				}
				v, _ := c.Get(form + "." + in.Name)
				if e.required(in, env) && isEmpty(v) {
					missing = append(missing, FieldError{Path: form + "." + in.Name, Rule: "required", Message: titleOr(in.Label, in.Name) + " is required"})
				}
			}
		}
		if len(missing) > 0 {
			return nil, &ValidationError{Message: "the form is incomplete", Fields: missing}
		}
		ns.Status = NodePassed
		markActor()
	case n.Kind == NodeTask && (verb == "complete" || verb == "fail"):
		ns.Status = NodePassed
		if verb == "fail" {
			ns.Status = NodeFailed
		}
		ns.Result = input.Result
		markActor()
	case n.Kind == NodeAutomated && verb == "run":
		if e.Automation == nil {
			return nil, badState("no automation is configured")
		}
		if err := e.runNode(ctx, c, stage, n, ns); err != nil {
			return nil, err
		}
		markActor()
	case n.Kind == NodeCheck && verb == "recheck":
		e.refreshChecks(c, stage)
		markActor()
	case n.Kind == NodeCertificate && verb == "issue":
		if ns.Status != NodePassed {
			cert, err := e.issue(c, n.Certificate, actor)
			if err != nil {
				return nil, err
			}
			ns.Result = map[string]any{"certificate_id": cert.ID, "number": cert.Number}
			ns.Status = NodePassed
			entry.To = cert.Number
		}
		markActor()
	default:
		return nil, fmt.Errorf("%w: %q is not an operation of %s node %q", ErrNotFound, verb, n.Kind, node)
	}
	if entry.To == "" {
		entry.To = ns.Status
	}
	c.History = append(c.History, entry)
	if st.AutoAdvance && e.nodesSatisfied(st, ss) && len(e.missingRequired(c, st, ss, actor)) == 0 {
		if err := e.completeStage(ctx, c, stage, actor, "", 0); err != nil {
			return nil, err
		}
	}
	c.UpdatedAt = now
	return c, nil
}

// reviewPaths lists the "form.input" paths a review node must decide on: the
// visible inputs of its forms that hold a value.
func (e *Engine) reviewPaths(c *Case, n *Node) []string {
	env := e.Env(c, Actor{})
	var paths []string
	for _, form := range n.Forms {
		inputs, _ := e.C.FormInputs(form)
		cf := e.C.forms[form]
		for _, in := range inputs {
			if ok, _ := e.cond(in.VisibleIf, env); !ok {
				continue
			}
			path := form + "." + in.Name
			if cf.Repeatable {
				if list, _ := c.Data[form].([]any); len(list) > 0 {
					paths = append(paths, path)
				}
				continue
			}
			if v, _ := c.Get(path); !isEmpty(v) {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// violatesFourEyes reports whether the actor already acted where the node's
// distinct_from forbids it.
func (e *Engine) violatesFourEyes(c *Case, n *Node, actor Actor) bool {
	for _, other := range n.DistinctFrom {
		if other == "applicant" {
			if isApplicant(c, actor) {
				return true
			}
			continue
		}
		for _, ss := range c.Stages {
			if ns := ss.Nodes[other]; ns != nil && slices.Contains(ns.Actors, actor.ID) {
				return true
			}
		}
	}
	return false
}

// FormatNumber renders a number format: {year} {month} {day} {seq} {seq:N}.
func FormatNumber(format string, seq int64, now time.Time) string {
	if format == "" {
		format = "{year}-{seq:6}"
	}
	out := strings.NewReplacer(
		"{year}", fmt.Sprintf("%04d", now.Year()),
		"{month}", fmt.Sprintf("%02d", int(now.Month())),
		"{day}", fmt.Sprintf("%02d", now.Day()),
		"{seq}", strconv.FormatInt(seq, 10),
	).Replace(format)
	for {
		i := strings.Index(out, "{seq:")
		if i < 0 {
			return out
		}
		j := strings.Index(out[i:], "}")
		if j < 0 {
			return out
		}
		width, err := strconv.Atoi(out[i+5 : i+j])
		if err != nil || width < 1 || width > 20 {
			width = 1
		}
		out = out[:i] + fmt.Sprintf("%0*d", width, seq) + out[i+j+1:]
	}
}
