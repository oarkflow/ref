// Package pipeline runs multi-stage data verification workflows: a case (an
// application, a claim, a registration) moves through stages, each with its
// own page of form groups, its own people, its own checks and its own status,
// until it is approved, rejected or withdrawn.
//
// A "Passport Request" is the canonical shape:
//
//	submission   the applicant fills a public page (tabbed/wizard form groups)
//	verification an officer reviews every submitted field, verifying or flagging
//	             each; flagged fields go back to the applicant, who may edit
//	             only those
//	biometrics   a node per capture, each with its own status
//	approval     two distinct supervisors approve (four-eyes)
//	issuance     a signed, verifiable certificate is issued
//
// Everything is declared as data (see Definition). The engine is pure: it takes
// a case and an actor, applies one operation, and returns the new case plus
// the history entry, so the same rules run in a request, a test or a replay.
// Storage, expressions and automation are injected (see Store, Evaluator,
// Automation), which keeps this package free of any transport or database.
//
// BCL notes: `field` and `type` do not bind in BCL, so fields are `input`
// blocks and discriminators are spelled `kind`.
package pipeline

// Definition is one pipeline.
type Definition struct {
	Name        string `bcl:",id" json:"name"`
	Title       string `bcl:"title" json:"title,omitempty"`
	Description string `bcl:"description" json:"description,omitempty"`
	Version     string `bcl:"version" json:"version,omitempty"`
	// NumberFormat renders the human case number: {year}, {month}, {day},
	// {seq} or {seq:6} (zero padded), and literal text, e.g. "PP-{year}-{seq:6}".
	NumberFormat string `bcl:"number_format" json:"number_format,omitempty"`
	// RevealRoles may see sensitive inputs unmasked in every stage.
	RevealRoles []string `bcl:"reveal_roles" json:"reveal_roles,omitempty"`

	// Inputs is the shared field catalog. A form lists catalog inputs by name
	// (fields [...]) and/or declares its own inline input blocks.
	Inputs []Input `bcl:"input,block" json:"inputs,omitempty"`
	// Forms are reusable groups of inputs — the snippets stages compose.
	Forms        []Form            `bcl:"form,block" json:"forms,omitempty"`
	Stages       []Stage           `bcl:"stage,block" json:"stages"`
	Certificates []CertificateSpec `bcl:"certificate,block" json:"certificates,omitempty"`
}

// Input kinds.
const (
	KindText        = "text"
	KindTextarea    = "textarea"
	KindNumber      = "number"
	KindInteger     = "integer"
	KindBoolean     = "boolean"
	KindDate        = "date"
	KindDateTime    = "datetime"
	KindEmail       = "email"
	KindPhone       = "phone"
	KindSelect      = "select"
	KindMultiSelect = "multiselect"
	KindRadio       = "radio"
	KindFile        = "file"
)

// Input is one field.
type Input struct {
	Name        string `bcl:",id" json:"name"`
	Label       string `bcl:"label" json:"label,omitempty"`
	Kind        string `bcl:"kind,ident" json:"kind"`
	Help        string `bcl:"help" json:"help,omitempty"`
	Placeholder string `bcl:"placeholder" json:"placeholder,omitempty"`
	Required    bool   `bcl:"required" json:"required,omitempty"`
	// RequiredIf / VisibleIf are expressions over the case (data, actor, …).
	// A hidden input is neither validated nor accepted on save.
	RequiredIf string   `bcl:"required_if" json:"required_if,omitempty"`
	VisibleIf  string   `bcl:"visible_if" json:"visible_if,omitempty"`
	Pattern    string   `bcl:"pattern" json:"pattern,omitempty"`
	MinLength  int      `bcl:"min_length" json:"min_length,omitempty"`
	MaxLength  int      `bcl:"max_length" json:"max_length,omitempty"`
	Min        string   `bcl:"min" json:"min,omitempty"` // number, or a date for date kinds
	Max        string   `bcl:"max" json:"max,omitempty"`
	Options    []string `bcl:"options" json:"options,omitempty"`
	// Lookup names a reference-data set the host resolves into options.
	Lookup  string `bcl:"lookup" json:"lookup,omitempty"`
	Default string `bcl:"default" json:"default,omitempty"`
	// Sensitive values are masked for everyone without a reveal role.
	Sensitive bool     `bcl:"sensitive" json:"sensitive,omitempty"`
	Accept    []string `bcl:"accept" json:"accept,omitempty"` // file types
	// Span is the grid columns the input occupies (layout hint).
	Span int `bcl:"span" json:"span,omitempty"`
}

// Form is a reusable group of inputs.
type Form struct {
	Name        string   `bcl:",id" json:"name"`
	Title       string   `bcl:"title" json:"title,omitempty"`
	Description string   `bcl:"description" json:"description,omitempty"`
	Fields      []string `bcl:"fields" json:"fields,omitempty"` // catalog inputs, in order
	Inputs      []Input  `bcl:"input,block" json:"inputs,omitempty"`
	// Repeatable forms hold a list of entries (previous passports, children).
	Repeatable bool `bcl:"repeatable" json:"repeatable,omitempty"`
	MinItems   int  `bcl:"min_items" json:"min_items,omitempty"`
	MaxItems   int  `bcl:"max_items" json:"max_items,omitempty"`
	Columns    int  `bcl:"columns" json:"columns,omitempty"`
}

// Group modes and layouts.
const (
	ModeEditable = "editable"
	ModeReadonly = "readonly"
	ModeSummary  = "summary"
	ModeHidden   = "hidden"

	LayoutStacked   = "stacked"
	LayoutTabbed    = "tabbed"
	LayoutWizard    = "wizard"
	LayoutAccordion = "accordion"
	LayoutGrid      = "grid"
)

// Page is what a stage shows.
type Page struct {
	Title       string  `bcl:"title" json:"title,omitempty"`
	Description string  `bcl:"description" json:"description,omitempty"`
	Layout      string  `bcl:"layout,ident" json:"layout,omitempty"`
	SubmitLabel string  `bcl:"submit_label" json:"submit_label,omitempty"`
	Groups      []Group `bcl:"group,block" json:"groups"`
}

// Group is a section of a page holding one or more forms.
type Group struct {
	Name        string   `bcl:",id" json:"name"`
	Title       string   `bcl:"title" json:"title,omitempty"`
	Description string   `bcl:"description" json:"description,omitempty"`
	Mode        string   `bcl:"mode,ident" json:"mode,omitempty"`
	Layout      string   `bcl:"layout,ident" json:"layout,omitempty"`
	Forms       []string `bcl:"forms" json:"forms"`
	Columns     int      `bcl:"columns" json:"columns,omitempty"`
	VisibleIf   string   `bcl:"visible_if" json:"visible_if,omitempty"`
	// Roles, when set, restricts who sees the group at all.
	Roles     []string `bcl:"roles" json:"roles,omitempty"`
	Collapsed bool     `bcl:"collapsed" json:"collapsed,omitempty"`
}

// Stage is one step of the pipeline.
type Stage struct {
	Name        string `bcl:",id" json:"name"`
	Title       string `bcl:"title" json:"title,omitempty"`
	Description string `bcl:"description" json:"description,omitempty"`
	// Kind is a hint for UIs and reports: submission, review, approval,
	// processing, issuance.
	Kind string `bcl:"kind,ident" json:"kind,omitempty"`
	// Public stages may be acted on by the case's applicant (its creator),
	// including anonymous starts when the host allows it.
	Public bool `bcl:"public" json:"public,omitempty"`
	// Roles act at this stage; ViewRoles may only look.
	Roles     []string `bcl:"roles" json:"roles,omitempty"`
	ViewRoles []string `bcl:"view_roles" json:"view_roles,omitempty"`
	// Requires lists stages that must be completed before this one opens, and
	// EntryConditions expressions that must hold.
	Requires        []string `bcl:"requires" json:"requires,omitempty"`
	EntryConditions []string `bcl:"entry_conditions" json:"entry_conditions,omitempty"`
	// SkipIf skips the stage entirely (e.g. no biometrics for a renewal).
	SkipIf string `bcl:"skip_if" json:"skip_if,omitempty"`

	Page  *Page  `bcl:"page" json:"page,omitempty"`
	Forms []Form `bcl:"form,block" json:"forms,omitempty"` // stage-local extra forms
	Nodes []Node `bcl:"node,block" json:"nodes,omitempty"`
	// Complete is the node completion rule: all (default), any or quorum.
	Complete string `bcl:"complete,ident" json:"complete,omitempty"`
	Quorum   int    `bcl:"quorum" json:"quorum,omitempty"`
	// AutoAdvance completes the stage as soon as its nodes are done, without
	// an explicit action.
	AutoAdvance bool `bcl:"auto_advance" json:"auto_advance,omitempty"`

	Actions []ActionSpec `bcl:"action,block" json:"actions,omitempty"`
	// Next is the stage entered on completion (default: the following one).
	Next string `bcl:"next" json:"next,omitempty"`
	// Assign sets case data when the stage completes.
	Assign []Assignment `bcl:"assign,block" json:"assign,omitempty"`
	// Certificate issues the named certificate when the stage completes.
	Certificate string `bcl:"certificate" json:"certificate,omitempty"`
	// Due is the stage SLA, e.g. "72h".
	Due string `bcl:"due" json:"due,omitempty"`
	// OnEnter / OnComplete name host hooks (in REF: intents) to run.
	OnEnter    string `bcl:"on_enter" json:"on_enter,omitempty"`
	OnComplete string `bcl:"on_complete" json:"on_complete,omitempty"`
}

// Node kinds.
const (
	NodeForm        = "form"
	NodeReview      = "review"
	NodeApproval    = "approval"
	NodeCheck       = "check"
	NodeAutomated   = "automated"
	NodeCertificate = "certificate"
	NodeTask        = "task"
)

// Node is a unit of work inside a stage with its own status.
type Node struct {
	Name        string `bcl:",id" json:"name"`
	Title       string `bcl:"title" json:"title,omitempty"`
	Description string `bcl:"description" json:"description,omitempty"`
	Kind        string `bcl:"kind,ident" json:"kind"`
	// Roles may act on the node (default: the stage's roles).
	Roles []string `bcl:"roles" json:"roles,omitempty"`
	// Optional nodes do not block stage completion.
	Optional bool `bcl:"optional" json:"optional,omitempty"`
	// AppliesIf skips the node when false.
	AppliesIf string `bcl:"applies_if" json:"applies_if,omitempty"`
	// Forms: a form node validates them; a review node verifies their inputs.
	Forms []string `bcl:"forms" json:"forms,omitempty"`
	// Approvals is how many distinct approvers an approval node needs.
	Approvals int `bcl:"approvals" json:"approvals,omitempty"`
	// DistinctFrom names nodes (or "applicant") whose actors may not act here
	// — the four-eyes rule.
	DistinctFrom []string `bcl:"distinct_from" json:"distinct_from,omitempty"`
	// Check is the pass condition of a check node, re-evaluated on every save.
	Check string `bcl:"check" json:"check,omitempty"`
	// Hook names the host automation an automated node runs.
	Hook string `bcl:"hook" json:"hook,omitempty"`
	// Certificate names the certificate a certificate node issues.
	Certificate string `bcl:"certificate" json:"certificate,omitempty"`
	// Auto issues a certificate node's certificate as soon as the stage is
	// entered, instead of waiting for someone to issue it.
	Auto bool `bcl:"auto" json:"auto,omitempty"`
	// WaiveRoles may waive the node.
	WaiveRoles []string `bcl:"waive_roles" json:"waive_roles,omitempty"`
}

// Action outcomes.
const (
	OutcomeAdvance  = "advance"  // complete the stage and move on
	OutcomeReturn   = "return"   // send the case back to an earlier stage
	OutcomeReject   = "reject"   // terminal: rejected
	OutcomeApprove  = "approve"  // terminal: approved (after completing the stage)
	OutcomeWithdraw = "withdraw" // terminal: withdrawn
	OutcomeHold     = "hold"     // record only (request info, comment)
)

// ActionSpec is a stage-level action a person can take.
type ActionSpec struct {
	Name  string `bcl:",id" json:"name"`
	Label string `bcl:"label" json:"label,omitempty"`
	// Outcome: advance (default), return, reject, approve, withdraw, hold.
	Outcome string   `bcl:"outcome,ident" json:"outcome,omitempty"`
	Roles   []string `bcl:"roles" json:"roles,omitempty"`
	// ReturnTo is the stage a return outcome reopens (default: the first).
	ReturnTo        string `bcl:"return_to" json:"return_to,omitempty"`
	Next            string `bcl:"next" json:"next,omitempty"`
	CommentRequired bool   `bcl:"comment_required" json:"comment_required,omitempty"`
	// SkipNodes lets the action complete the stage with open nodes.
	SkipNodes bool   `bcl:"skip_nodes" json:"skip_nodes,omitempty"`
	Confirm   string `bcl:"confirm" json:"confirm,omitempty"`
	Condition string `bcl:"condition" json:"condition,omitempty"`
}

// Assignment sets a data path from an expression.
type Assignment struct {
	Path  string `bcl:",id" json:"path"`
	Value string `bcl:"value" json:"value"`
}

// CertificateSpec describes an issued document.
type CertificateSpec struct {
	Name  string `bcl:",id" json:"name"`
	Title string `bcl:"title" json:"title,omitempty"`
	// NumberFormat as for Definition.NumberFormat.
	NumberFormat string `bcl:"number_format" json:"number_format,omitempty"`
	// Fields are data paths copied into the certificate ("applicant.full_name").
	Fields []string `bcl:"fields" json:"fields"`
	// Validity is how long the certificate is valid, e.g. "87600h" (10y).
	Validity string `bcl:"validity" json:"validity,omitempty"`
}
