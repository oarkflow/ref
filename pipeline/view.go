package pipeline

import (
	"fmt"
	"strings"
	"time"
)

// View is what a client renders for one stage of a case: the page with every
// group's effective mode for this actor, values (masked where sensitive), the
// fields sent back for correction, reviewer verdicts, node statuses and the
// actions the actor may take. It is plain JSON so any frontend — the bundled
// renderer, a mobile app, a portal — draws the same thing.
type View struct {
	Case         CaseSummary     `json:"case"`
	Stage        StageView       `json:"stage"`
	Progress     []StageProgress `json:"progress"`
	Page         *PageView       `json:"page,omitempty"`
	Nodes        []NodeView      `json:"nodes,omitempty"`
	Actions      []ActionView    `json:"actions,omitempty"`
	CanEdit      bool            `json:"can_edit"`
	Corrections  []Flag          `json:"corrections,omitempty"`
	Certificates []Certificate   `json:"certificates,omitempty"`
}

// CaseSummary is the case header.
type CaseSummary struct {
	ID        string    `json:"id"`
	Number    string    `json:"number"`
	Pipeline  string    `json:"pipeline"`
	Title     string    `json:"title,omitempty"`
	Status    string    `json:"status"`
	Stage     string    `json:"stage"`
	Revision  int64     `json:"revision"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StageView describes the viewed stage.
type StageView struct {
	Name        string     `json:"name"`
	Title       string     `json:"title,omitempty"`
	Kind        string     `json:"kind,omitempty"`
	Description string     `json:"description,omitempty"`
	Status      string     `json:"status"`
	DueAt       *time.Time `json:"due_at,omitempty"`
	Current     bool       `json:"current"`
}

// StageProgress is one step of the progress indicator.
type StageProgress struct {
	Name   string `json:"name"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
}

// PageView is a rendered page.
type PageView struct {
	Title       string      `json:"title,omitempty"`
	Description string      `json:"description,omitempty"`
	Layout      string      `json:"layout"`
	SubmitLabel string      `json:"submit_label,omitempty"`
	Groups      []GroupView `json:"groups"`
}

// GroupView is a rendered group.
type GroupView struct {
	Name        string     `json:"name"`
	Title       string     `json:"title,omitempty"`
	Description string     `json:"description,omitempty"`
	Mode        string     `json:"mode"`
	Layout      string     `json:"layout"`
	Columns     int        `json:"columns,omitempty"`
	Collapsed   bool       `json:"collapsed,omitempty"`
	Forms       []FormView `json:"forms"`
}

// FormView is a rendered form.
type FormView struct {
	Name        string           `json:"name"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Repeatable  bool             `json:"repeatable,omitempty"`
	MinItems    int              `json:"min_items,omitempty"`
	MaxItems    int              `json:"max_items,omitempty"`
	Columns     int              `json:"columns,omitempty"`
	Inputs      []InputView      `json:"inputs"`
	Entries     []map[string]any `json:"entries,omitempty"`
}

// InputView is a rendered input.
type InputView struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"`
	Help        string   `json:"help,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Editable    bool     `json:"editable"`
	Options     []string `json:"options,omitempty"`
	Lookup      string   `json:"lookup,omitempty"`
	Choices     []Option `json:"choices,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	MinLength   int      `json:"min_length,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
	Min         string   `json:"min,omitempty"`
	Max         string   `json:"max,omitempty"`
	Accept      []string `json:"accept,omitempty"`
	Span        int      `json:"span,omitempty"`
	Value       any      `json:"value,omitempty"`
	Masked      bool     `json:"masked,omitempty"`
	Flag        *Flag    `json:"flag,omitempty"`
	Verdict     *Verdict `json:"verdict,omitempty"`
}

// NodeView is a node with its status and the operations the actor may use.
type NodeView struct {
	Name        string     `json:"name"`
	Title       string     `json:"title,omitempty"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Optional    bool       `json:"optional,omitempty"`
	Approvals   []Approval `json:"approvals,omitempty"`
	Required    int        `json:"required_approvals,omitempty"`
	Comment     string     `json:"comment,omitempty"`
	Result      any        `json:"result,omitempty"`
	Forms       []string   `json:"forms,omitempty"`
	Operations  []string   `json:"operations,omitempty"`
	Description string     `json:"description,omitempty"`
}

// ActionView is an available stage action.
type ActionView struct {
	Name            string `json:"name"`
	Label           string `json:"label"`
	Outcome         string `json:"outcome"`
	CommentRequired bool   `json:"comment_required,omitempty"`
	Confirm         string `json:"confirm,omitempty"`
}

// View renders a stage of a case for an actor. An empty stage means the
// case's current stage.
func (e *Engine) View(c *Case, actor Actor, stage string) (*View, error) {
	if stage == "" {
		stage = c.Stage
		// An applicant following a case through internal stages sees the
		// public stage they filled in (read-only once it has moved on), with
		// the progress of the whole case.
		if st, ok := e.C.Stage(stage); ok && !e.CanView(c, st, actor) && !st.Public && isApplicant(c, actor) {
			for _, s := range e.C.Def.Stages {
				if x := c.Stages[s.Name]; s.Public && x != nil && x.Status != StagePending {
					stage = s.Name
				}
			}
		}
	}
	st, ok := e.C.Stage(stage)
	if !ok {
		return nil, fmt.Errorf("%w: stage %q", ErrNotFound, stage)
	}
	applicant := isApplicant(c, actor)
	if !e.CanView(c, st, actor) && !(applicant && st.Public) {
		return nil, forbidden("you may not view stage %q", stage)
	}
	ss := c.Stages[stage]
	if ss == nil {
		ss = &StageState{Status: StagePending}
	}
	v := &View{
		Case: CaseSummary{ID: c.ID, Number: c.Number, Pipeline: c.Pipeline, Title: e.C.Def.Title, Status: c.Status,
			Stage: c.Stage, Revision: c.Revision, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt},
		Stage: StageView{Name: st.Name, Title: st.Title, Kind: st.Kind, Description: st.Description, Status: ss.Status,
			DueAt: ss.DueAt, Current: c.Stage == stage},
	}
	for _, s := range e.C.Def.Stages {
		status := StagePending
		if x := c.Stages[s.Name]; x != nil {
			status = x.Status
		}
		v.Progress = append(v.Progress, StageProgress{Name: s.Name, Title: s.Title, Status: status})
	}
	open := c.Stage == stage && !c.Terminal() && (ss.Status == StageActive || ss.Status == StageReturned)
	editable := map[string]map[string]Input{}
	if open {
		editable = e.editable(c, st, ss, actor)
	}
	v.CanEdit = len(editable) > 0
	for _, f := range ss.Flags {
		v.Corrections = append(v.Corrections, f)
	}
	reveal := applicant || actor.HasAnyRole(e.C.Def.RevealRoles)
	verdicts := map[string]Verdict{}
	for _, n := range st.Nodes {
		if ns := ss.Nodes[n.Name]; ns != nil && n.Kind == NodeReview && e.canActNode(c, st, &n, actor) {
			for path, vd := range ns.Verdicts {
				verdicts[path] = vd
			}
		}
	}
	env := e.Env(c, actor)
	if st.Page != nil {
		page := &PageView{Title: st.Page.Title, Description: st.Page.Description, Layout: orDefault(st.Page.Layout, LayoutStacked), SubmitLabel: st.Page.SubmitLabel}
		for _, g := range st.Page.Groups {
			if len(g.Roles) > 0 && !actor.HasAnyRole(g.Roles) {
				continue
			}
			if ok, err := e.cond(g.VisibleIf, env); err != nil || !ok {
				continue
			}
			mode := orDefault(g.Mode, ModeEditable)
			if mode == ModeHidden {
				continue
			}
			gv := GroupView{Name: g.Name, Title: g.Title, Description: g.Description, Layout: orDefault(g.Layout, LayoutStacked), Columns: g.Columns, Collapsed: g.Collapsed}
			anyEditable := false
			for _, form := range g.Forms {
				cf := e.C.forms[form]
				fv := FormView{Name: form, Title: cf.Title, Description: cf.Description, Repeatable: cf.Repeatable, MinItems: cf.MinItems, MaxItems: cf.MaxItems, Columns: cf.Columns}
				for _, in := range cf.inputs {
					if ok, err := e.cond(in.VisibleIf, env); err != nil || !ok {
						continue
					}
					path := form + "." + in.Name
					_, canEdit := editable[form][in.Name]
					canEdit = canEdit && mode == ModeEditable
					anyEditable = anyEditable || canEdit
					iv := InputView{Name: in.Name, Path: path, Label: orDefault(in.Label, in.Name), Kind: orDefault(in.Kind, KindText),
						Help: in.Help, Placeholder: in.Placeholder, Required: e.required(in, env), Editable: canEdit,
						Options: in.Options, Lookup: in.Lookup, Pattern: in.Pattern, MinLength: in.MinLength, MaxLength: in.MaxLength,
						Min: in.Min, Max: in.Max, Accept: in.Accept, Span: in.Span}
					if in.Lookup != "" && e.Lookup != nil {
						iv.Choices = e.Lookup(in.Lookup, c)
					} else {
						for _, o := range in.Options {
							iv.Choices = append(iv.Choices, Option{Value: o, Label: o})
						}
					}
					if !cf.Repeatable {
						if val, ok := c.Get(path); ok {
							iv.Value = val
						} else if canEdit && in.Default != "" {
							iv.Value = in.Default
						}
						if in.Sensitive && !reveal && iv.Value != nil {
							iv.Value, iv.Masked = mask(fmt.Sprint(iv.Value)), true
						}
					}
					if f, ok := ss.Flags[path]; ok {
						flag := f
						iv.Flag = &flag
					}
					if vd, ok := verdicts[path]; ok {
						verdict := vd
						iv.Verdict = &verdict
					}
					fv.Inputs = append(fv.Inputs, iv)
				}
				if cf.Repeatable {
					list, _ := c.Data[form].([]any)
					for _, raw := range list {
						entry, _ := raw.(map[string]any)
						row := map[string]any{}
						for _, in := range cf.inputs {
							val := entry[in.Name]
							if in.Sensitive && !reveal && val != nil {
								val = mask(fmt.Sprint(val))
							}
							if val != nil {
								row[in.Name] = val
							}
						}
						fv.Entries = append(fv.Entries, row)
					}
				}
				if len(fv.Inputs) > 0 {
					gv.Forms = append(gv.Forms, fv)
				}
			}
			switch {
			case mode == ModeSummary:
				gv.Mode = ModeSummary
			case mode == ModeEditable && anyEditable:
				gv.Mode = ModeEditable
			default:
				gv.Mode = ModeReadonly
			}
			if len(gv.Forms) > 0 {
				page.Groups = append(page.Groups, gv)
			}
		}
		v.Page = page
	}
	for i := range st.Nodes {
		n := &st.Nodes[i]
		ns := ss.Nodes[n.Name]
		if ns == nil {
			ns = &NodeState{Status: NodePending}
		}
		nv := NodeView{Name: n.Name, Title: n.Title, Kind: n.Kind, Status: ns.Status, Optional: n.Optional, Approvals: ns.Approvals,
			Comment: ns.Comment, Forms: n.Forms, Description: n.Description}
		if n.Kind == NodeApproval {
			nv.Required = max(1, n.Approvals)
		}
		if ns.Result != nil {
			nv.Result = ns.Result
		}
		if open && ns.Status != NodeSkipped {
			nv.Operations = e.nodeOperations(c, st, n, ns, actor)
		}
		v.Nodes = append(v.Nodes, nv)
	}
	if open {
		for _, a := range e.Actions(c, actor) {
			v.Actions = append(v.Actions, ActionView{Name: a.Name, Label: orDefault(a.Label, a.Name), Outcome: orDefault(a.Outcome, OutcomeAdvance),
				CommentRequired: a.CommentRequired, Confirm: a.Confirm})
		}
	}
	if c.Terminal() || applicant || e.CanView(c, st, actor) {
		v.Certificates = c.Certificates
	}
	return v, nil
}

func (e *Engine) nodeOperations(c *Case, st *Stage, n *Node, ns *NodeState, actor Actor) []string {
	var ops []string
	if actor.HasAnyRole(n.WaiveRoles) && ns.Status != NodeWaived && ns.Status != NodePassed {
		ops = append(ops, "waive")
	}
	if !e.canActNode(c, st, n, actor) || e.violatesFourEyes(c, n, actor) {
		return ops
	}
	switch n.Kind {
	case NodeReview:
		if ns.Status != NodePassed {
			ops = append(ops, "verify", "complete")
		}
	case NodeApproval:
		approved := false
		for _, a := range ns.Approvals {
			approved = approved || a.By == actor.ID
		}
		if !approved && ns.Status != NodePassed && ns.Status != NodeFailed {
			ops = append(ops, "approve", "reject")
		}
	case NodeForm, NodeTask:
		if ns.Status != NodePassed {
			ops = append(ops, "complete")
			if n.Kind == NodeTask {
				ops = append(ops, "fail")
			}
		}
	case NodeAutomated:
		if ns.Status != NodePassed && e.Automation != nil {
			ops = append(ops, "run")
		}
	case NodeCheck:
		ops = append(ops, "recheck")
	case NodeCertificate:
		if ns.Status != NodePassed {
			ops = append(ops, "issue")
		}
	}
	return ops
}

func mask(s string) string {
	r := []rune(s)
	if len(r) <= 4 {
		return strings.Repeat("•", len(r))
	}
	return strings.Repeat("•", len(r)-4) + string(r[len(r)-4:])
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
