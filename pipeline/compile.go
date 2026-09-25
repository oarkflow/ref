package pipeline

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Compiled is a validated, indexed Definition. It is immutable and safe to
// share across goroutines.
type Compiled struct {
	Def          *Definition
	forms        map[string]*compiledForm
	stages       map[string]int
	certificates map[string]*CertificateSpec
	patterns     map[string]*regexp.Regexp // "form.input" -> compiled pattern
}

type compiledForm struct {
	Form
	inputs []Input // resolved, in display order
	stage  string  // "" for pipeline-level forms
}

// Compile validates def and builds its indexes. Every cross-reference — form
// to catalog input, group to form, node to form, action to stage, stage to
// certificate — is checked here, so a typo fails the deployment instead of a
// request.
func Compile(def *Definition) (*Compiled, error) {
	if def == nil || strings.TrimSpace(def.Name) == "" {
		return nil, fmt.Errorf("pipeline: a pipeline needs a name")
	}
	c := &Compiled{
		Def:          def,
		forms:        map[string]*compiledForm{},
		stages:       map[string]int{},
		certificates: map[string]*CertificateSpec{},
		patterns:     map[string]*regexp.Regexp{},
	}
	where := "pipeline " + def.Name
	catalog := map[string]Input{}
	for _, in := range def.Inputs {
		if err := checkInput(in); err != nil {
			return nil, fmt.Errorf("%s: input %q: %w", where, in.Name, err)
		}
		if _, dup := catalog[in.Name]; dup {
			return nil, fmt.Errorf("%s: input %q declared twice", where, in.Name)
		}
		catalog[in.Name] = in
	}
	addForm := func(f Form, stage string) error {
		if !validName(f.Name) {
			return fmt.Errorf("%s: invalid form name %q", where, f.Name)
		}
		if _, dup := c.forms[f.Name]; dup {
			return fmt.Errorf("%s: form %q declared twice (form names are global across stages)", where, f.Name)
		}
		cf := &compiledForm{Form: f, stage: stage}
		seen := map[string]bool{}
		for _, name := range f.Fields {
			in, ok := catalog[name]
			if !ok {
				return fmt.Errorf("%s: form %q uses unknown input %q", where, f.Name, name)
			}
			cf.inputs = append(cf.inputs, in)
			seen[name] = true
		}
		for _, in := range f.Inputs {
			if err := checkInput(in); err != nil {
				return fmt.Errorf("%s: form %q input %q: %w", where, f.Name, in.Name, err)
			}
			if seen[in.Name] {
				return fmt.Errorf("%s: form %q has input %q twice", where, f.Name, in.Name)
			}
			seen[in.Name] = true
			cf.inputs = append(cf.inputs, in)
		}
		if len(cf.inputs) == 0 {
			return fmt.Errorf("%s: form %q has no inputs", where, f.Name)
		}
		for _, in := range cf.inputs {
			if in.Pattern != "" {
				re, err := regexp.Compile(in.Pattern)
				if err != nil {
					return fmt.Errorf("%s: form %q input %q: bad pattern: %w", where, f.Name, in.Name, err)
				}
				c.patterns[f.Name+"."+in.Name] = re
			}
		}
		c.forms[f.Name] = cf
		return nil
	}
	for _, f := range def.Forms {
		if err := addForm(f, ""); err != nil {
			return nil, err
		}
	}
	for i := range def.Certificates {
		cert := &def.Certificates[i]
		if !validName(cert.Name) || len(cert.Fields) == 0 {
			return nil, fmt.Errorf("%s: certificate %q needs a name and fields", where, cert.Name)
		}
		if _, err := parseDuration(cert.Validity); err != nil {
			return nil, fmt.Errorf("%s: certificate %q validity: %w", where, cert.Name, err)
		}
		c.certificates[cert.Name] = cert
	}
	if len(def.Stages) == 0 {
		return nil, fmt.Errorf("%s: a pipeline needs at least one stage", where)
	}
	for i, st := range def.Stages {
		if !validName(st.Name) {
			return nil, fmt.Errorf("%s: invalid stage name %q", where, st.Name)
		}
		if _, dup := c.stages[st.Name]; dup {
			return nil, fmt.Errorf("%s: stage %q declared twice", where, st.Name)
		}
		c.stages[st.Name] = i
		for _, f := range st.Forms {
			if err := addForm(f, st.Name); err != nil {
				return nil, err
			}
		}
	}
	for _, st := range def.Stages {
		if err := c.checkStage(st); err != nil {
			return nil, fmt.Errorf("%s: stage %q: %w", where, st.Name, err)
		}
	}
	return c, nil
}

func (c *Compiled) checkStage(st Stage) error {
	stageExists := func(name string) bool { _, ok := c.stages[name]; return ok }
	for _, r := range st.Requires {
		if !stageExists(r) {
			return fmt.Errorf("requires unknown stage %q", r)
		}
	}
	if st.Next != "" && !stageExists(st.Next) {
		return fmt.Errorf("next names unknown stage %q", st.Next)
	}
	if st.Certificate != "" && c.certificates[st.Certificate] == nil {
		return fmt.Errorf("issues unknown certificate %q", st.Certificate)
	}
	if _, err := parseDuration(st.Due); err != nil {
		return fmt.Errorf("due: %w", err)
	}
	switch st.Complete {
	case "", "all", "any":
	case "quorum":
		if st.Quorum < 1 {
			return fmt.Errorf("complete quorum needs quorum >= 1")
		}
	default:
		return fmt.Errorf("complete must be all, any or quorum")
	}
	if st.Page != nil {
		if !oneOf(st.Page.Layout, "", LayoutStacked, LayoutTabbed, LayoutWizard, LayoutAccordion, LayoutGrid) {
			return fmt.Errorf("page layout %q is not stacked, tabbed, wizard, accordion or grid", st.Page.Layout)
		}
		seen := map[string]bool{}
		for _, g := range st.Page.Groups {
			if !validName(g.Name) || seen[g.Name] {
				return fmt.Errorf("group %q: missing or duplicate name", g.Name)
			}
			seen[g.Name] = true
			if !oneOf(g.Mode, "", ModeEditable, ModeReadonly, ModeSummary, ModeHidden) {
				return fmt.Errorf("group %q: mode %q is not editable, readonly, summary or hidden", g.Name, g.Mode)
			}
			if !oneOf(g.Layout, "", LayoutStacked, LayoutTabbed, LayoutWizard, LayoutAccordion, LayoutGrid) {
				return fmt.Errorf("group %q: layout %q is not stacked, tabbed, wizard, accordion or grid", g.Name, g.Layout)
			}
			if len(g.Forms) == 0 {
				return fmt.Errorf("group %q lists no forms", g.Name)
			}
			for _, f := range g.Forms {
				if c.forms[f] == nil {
					return fmt.Errorf("group %q uses unknown form %q", g.Name, f)
				}
			}
		}
	}
	nodes := map[string]bool{}
	for _, n := range st.Nodes {
		if !validName(n.Name) || nodes[n.Name] {
			return fmt.Errorf("node %q: missing or duplicate name", n.Name)
		}
		nodes[n.Name] = true
		switch n.Kind {
		case NodeForm, NodeReview:
			if len(n.Forms) == 0 {
				return fmt.Errorf("node %q (%s) needs forms", n.Name, n.Kind)
			}
		case NodeApproval, NodeTask:
		case NodeCheck:
			if n.Check == "" {
				return fmt.Errorf("check node %q needs a check expression", n.Name)
			}
		case NodeAutomated:
			if n.Hook == "" {
				return fmt.Errorf("automated node %q needs a hook", n.Name)
			}
		case NodeCertificate:
			if c.certificates[n.Certificate] == nil {
				return fmt.Errorf("certificate node %q names unknown certificate %q", n.Name, n.Certificate)
			}
		default:
			return fmt.Errorf("node %q: kind %q is not form, review, approval, check, automated, certificate or task", n.Name, n.Kind)
		}
		for _, f := range n.Forms {
			if c.forms[f] == nil {
				return fmt.Errorf("node %q uses unknown form %q", n.Name, f)
			}
		}
	}
	for _, n := range st.Nodes {
		for _, d := range n.DistinctFrom {
			if d != "applicant" && !nodes[d] && !c.nodeExistsAnywhere(d) {
				return fmt.Errorf("node %q: distinct_from names unknown node %q", n.Name, d)
			}
		}
	}
	actions := map[string]bool{}
	for _, a := range st.Actions {
		if !validName(a.Name) || actions[a.Name] {
			return fmt.Errorf("action %q: missing or duplicate name", a.Name)
		}
		actions[a.Name] = true
		if !oneOf(a.Outcome, "", OutcomeAdvance, OutcomeReturn, OutcomeReject, OutcomeApprove, OutcomeWithdraw, OutcomeHold) {
			return fmt.Errorf("action %q: outcome %q is not advance, return, reject, approve, withdraw or hold", a.Name, a.Outcome)
		}
		if a.ReturnTo != "" && !stageExists(a.ReturnTo) {
			return fmt.Errorf("action %q returns to unknown stage %q", a.Name, a.ReturnTo)
		}
		if a.Next != "" && !stageExists(a.Next) {
			return fmt.Errorf("action %q: next names unknown stage %q", a.Name, a.Next)
		}
	}
	for _, as := range st.Assign {
		if strings.TrimSpace(as.Path) == "" || strings.TrimSpace(as.Value) == "" {
			return fmt.Errorf("assign needs a path and a value")
		}
	}
	return nil
}

func (c *Compiled) nodeExistsAnywhere(name string) bool {
	for _, st := range c.Def.Stages {
		for _, n := range st.Nodes {
			if n.Name == name {
				return true
			}
		}
	}
	return false
}

// Stage returns a stage by name.
func (c *Compiled) Stage(name string) (*Stage, bool) {
	i, ok := c.stages[name]
	if !ok {
		return nil, false
	}
	return &c.Def.Stages[i], true
}

func (c *Compiled) stageIndex(name string) int {
	if i, ok := c.stages[name]; ok {
		return i
	}
	return -1
}

// FormInputs returns a form's resolved inputs.
func (c *Compiled) FormInputs(form string) ([]Input, bool) {
	f := c.forms[form]
	if f == nil {
		return nil, false
	}
	return f.inputs, true
}

// Certificate returns a certificate spec by name.
func (c *Compiled) Certificate(name string) (*CertificateSpec, bool) {
	spec := c.certificates[name]
	return spec, spec != nil
}

var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_\-]*$`)

func validName(s string) bool { return nameRe.MatchString(s) }

func oneOf(v string, allowed ...string) bool { return slices.Contains(allowed, v) }

func checkInput(in Input) error {
	if !validName(in.Name) {
		return fmt.Errorf("invalid input name")
	}
	switch in.Kind {
	case "", KindText, KindTextarea, KindNumber, KindInteger, KindBoolean, KindDate, KindDateTime,
		KindEmail, KindPhone, KindSelect, KindMultiSelect, KindRadio, KindFile:
	default:
		return fmt.Errorf("kind %q is not a supported input kind", in.Kind)
	}
	if (in.Kind == KindSelect || in.Kind == KindMultiSelect || in.Kind == KindRadio) && len(in.Options) == 0 && in.Lookup == "" {
		return fmt.Errorf("%s input needs options or a lookup", in.Kind)
	}
	if in.MinLength < 0 || in.MaxLength < 0 || (in.MaxLength > 0 && in.MinLength > in.MaxLength) {
		return fmt.Errorf("min_length/max_length are inconsistent")
	}
	return nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return d, nil
}

// Expressions returns every expression the definition declares, keyed by
// where it appears, so a host can compile them all at load time and fail a
// deployment on a syntax error instead of on the request that reaches it.
func (c *Compiled) Expressions() map[string]string {
	out := map[string]string{}
	add := func(where, expr string) {
		if strings.TrimSpace(expr) != "" {
			out[where] = expr
		}
	}
	for name, cf := range c.forms {
		for _, in := range cf.inputs {
			add("form "+name+" input "+in.Name+" required_if", in.RequiredIf)
			add("form "+name+" input "+in.Name+" visible_if", in.VisibleIf)
		}
	}
	for _, st := range c.Def.Stages {
		where := "stage " + st.Name
		add(where+" skip_if", st.SkipIf)
		for i, cond := range st.EntryConditions {
			add(fmt.Sprintf("%s entry_conditions[%d]", where, i), cond)
		}
		if st.Page != nil {
			for _, g := range st.Page.Groups {
				add(where+" group "+g.Name+" visible_if", g.VisibleIf)
			}
		}
		for _, n := range st.Nodes {
			add(where+" node "+n.Name+" applies_if", n.AppliesIf)
			add(where+" node "+n.Name+" check", n.Check)
		}
		for _, a := range st.Actions {
			add(where+" action "+a.Name+" condition", a.Condition)
		}
		for _, as := range st.Assign {
			add(where+" assign "+as.Path, as.Value)
		}
	}
	return out
}
