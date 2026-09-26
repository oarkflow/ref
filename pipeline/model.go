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

	// Calendars define working time (hours, holidays, timezone) for SLAs,
	// escalation and worker availability.
	Calendars []Calendar `bcl:"calendar,block" json:"calendars,omitempty"`
	// Workers is the directory routing assigns work from. A host may add to
	// it (see Engine.Directory).
	Workers []Worker `bcl:"worker,block" json:"workers,omitempty"`
	// On hooks run host intents when pipeline events happen (after the
	// change is saved): notifications, integrations, audit exports.
	On []EventHook `bcl:"on,block" json:"on,omitempty"`
	// Seal governs opening sealed inputs (bids, financial offers).
	Seal *SealPolicy `bcl:"seal" json:"seal,omitempty"`
	// Retention anonymises or purges finished cases after a period.
	Retention *Retention `bcl:"retention" json:"retention,omitempty"`
	// Subject lists the data paths that identify the data subject (for
	// right-to-erasure requests), e.g. ["applicant.national_id", "contact.email"].
	Subject []string `bcl:"subject" json:"subject,omitempty"`
	// Notes configures case notes.
	Notes *NotesPolicy `bcl:"notes" json:"notes,omitempty"`
	// Notify turns pipeline events into notifications for people, delivered
	// over the host's channels under each recipient's preferences.
	Notify []NotifyRule `bcl:"notify,block" json:"notify,omitempty"`
}

// Calendar is working time: weekly hours, holidays and a timezone.
type Calendar struct {
	Name     string `bcl:",id" json:"name"`
	Timezone string `bcl:"timezone" json:"timezone,omitempty"`
	// Hours is a weekly schedule, e.g. "sun-thu 10:00-17:00; fri 10:00-15:00",
	// or "24x7".
	Hours string `bcl:"hours" json:"hours,omitempty"`
	// Holidays are dates ("2026-10-20") or yearly dates ("01-01").
	Holidays []string `bcl:"holidays" json:"holidays,omitempty"`
}

// Worker is a person work can be routed to.
type Worker struct {
	ID       string   `bcl:",id" json:"id"`
	Name     string   `bcl:"name" json:"name,omitempty"`
	Roles    []string `bcl:"roles" json:"roles,omitempty"`
	Skills   []string `bcl:"skills" json:"skills,omitempty"`
	OrgUnits []string `bcl:"org_units" json:"org_units,omitempty"`
	// Capacity caps the worker's open assignments (0 = the routing default).
	Capacity int `bcl:"capacity" json:"capacity,omitempty"`
	// Calendar is the worker's working time; routing with respect_hours
	// skips workers who are off.
	Calendar string `bcl:"calendar" json:"calendar,omitempty"`
	Inactive bool   `bcl:"inactive" json:"inactive,omitempty"`
	// Away lists absences; work is not routed to an absent worker.
	Away []Absence `bcl:"away,block" json:"away,omitempty"`
	// DelegateTo receives this worker's routed work while they are away.
	DelegateTo string `bcl:"delegate_to" json:"delegate_to,omitempty"`
}

// Absence is a period a worker is unavailable (leave, training).
type Absence struct {
	Name   string `bcl:",id" json:"name"`
	From   string `bcl:"from" json:"from"`
	Until  string `bcl:"until" json:"until"`
	Reason string `bcl:"reason" json:"reason,omitempty"`
}

// Routing strategies.
const (
	RouteManual      = "manual"
	RouteRoundRobin  = "round_robin"
	RouteLeastLoaded = "least_loaded"
	RouteSkills      = "skill_based"
)

// Routing assigns a stage's work to one person when the stage opens.
type Routing struct {
	// Strategy: manual (claim from a queue), round_robin, least_loaded or
	// skill_based.
	Strategy string `bcl:"strategy,ident" json:"strategy,omitempty"`
	// Roles narrows candidates (default: the stage's roles).
	Roles          []string `bcl:"roles" json:"roles,omitempty"`
	RequiredSkills []string `bcl:"required_skills" json:"required_skills,omitempty"`
	// SkillsFrom is a data path holding extra required skills.
	SkillsFrom      string   `bcl:"skills_from" json:"skills_from,omitempty"`
	PreferredSkills []string `bcl:"preferred_skills" json:"preferred_skills,omitempty"`
	// Capacity is the default per-worker cap of open assignments.
	Capacity int `bcl:"capacity" json:"capacity,omitempty"`
	// Sticky sends a case back to whoever worked the stage before (after a
	// correction loop), if they are still eligible.
	Sticky bool `bcl:"sticky" json:"sticky,omitempty"`
	// RespectHours skips workers outside their working hours.
	RespectHours bool `bcl:"respect_hours" json:"respect_hours,omitempty"`
	// SameOrgUnit restricts candidates to workers assigned to the case's org
	// unit (or one of its ancestors, as resolved by the host).
	SameOrgUnit bool `bcl:"same_org_unit" json:"same_org_unit,omitempty"`
}

// SLA is a stage deadline with a warning, a breach action and escalation.
type SLA struct {
	// Duration is the time allowed, e.g. "72h", "3d" (3 working days with
	// a calendar). With a calendar, time is counted in working time.
	Duration   string `bcl:"duration" json:"duration"`
	WarnBefore string `bcl:"warn_before" json:"warn_before,omitempty"`
	Calendar   string `bcl:"calendar" json:"calendar,omitempty"`
	// OnBreach: notify (default), reassign, return (to ReturnTo) or an
	// action name of the stage to take automatically.
	OnBreach string `bcl:"on_breach" json:"on_breach,omitempty"`
	ReturnTo string `bcl:"return_to" json:"return_to,omitempty"`
	// Escalate lists escalation levels after the breach.
	Escalate []Escalation `bcl:"escalate,block" json:"escalate,omitempty"`
}

// Escalation is one level of an escalation chain.
type Escalation struct {
	Name string `bcl:",id" json:"name"`
	// After is measured from the breach (working time with the SLA calendar).
	After string `bcl:"after" json:"after"`
	// AssignRoles routes the case to someone holding one of these roles.
	AssignRoles []string `bcl:"assign_roles" json:"assign_roles,omitempty"`
	// Notify names who is told (passed to event hooks as recipients).
	Notify []string `bcl:"notify" json:"notify,omitempty"`
}

// Rule is a cross-field validation rule, checked when a stage advances.
type Rule struct {
	Name    string `bcl:",id" json:"name"`
	Check   string `bcl:"check" json:"check"`
	Message string `bcl:"message" json:"message"`
	// Path is where the error is reported (default: the rule name).
	Path string `bcl:"path" json:"path,omitempty"`
}

// EventHook runs a host hook (in REF: an intent) on a pipeline event.
type EventHook struct {
	// Event: case.started, stage.entered, stage.completed, stage.skipped,
	// case.returned, case.rejected, case.withdrawn, case.completed,
	// case.approved, assigned, claimed, released, delegated, queued,
	// sla.warning, sla.breached, sla.escalated, suspended, resumed,
	// note.added, file.uploaded, sealed.opened, erased, retention.applied —
	// or "*".
	Event string `bcl:",id" json:"event"`
	Hook  string `bcl:"hook" json:"hook"`
	// Stage limits the hook to one stage.
	Stage string `bcl:"stage" json:"stage,omitempty"`
	When  string `bcl:"when" json:"when,omitempty"`
}

// SealPolicy governs opening sealed inputs.
type SealPolicy struct {
	// OpenRoles may approve an opening; Quorum distinct approvers are needed.
	OpenRoles []string `bcl:"open_roles" json:"open_roles"`
	Quorum    int      `bcl:"quorum" json:"quorum,omitempty"`
	// OpenAfter is an RFC 3339 time or a data path holding one before which
	// sealed values cannot be opened (a bid deadline).
	OpenAfter string `bcl:"open_after" json:"open_after,omitempty"`
}

// Retention applies to finished cases.
type Retention struct {
	// After is how long after the case finished, e.g. "8760h".
	After string `bcl:"after" json:"after"`
	// Action: anonymize (default) or purge.
	Action string `bcl:"action,ident" json:"action,omitempty"`
}

// NotesPolicy configures case notes.
type NotesPolicy struct {
	// InternalRoles may write and read internal notes (default: every staff
	// role of the pipeline).
	InternalRoles []string `bcl:"internal_roles" json:"internal_roles,omitempty"`
	// ApplicantMayWrite lets the applicant add public notes.
	ApplicantMayWrite bool `bcl:"applicant_may_write" json:"applicant_may_write,omitempty"`
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
	Sensitive bool `bcl:"sensitive" json:"sensitive,omitempty"`
	// Accept lists the content types a file input takes ("image/png",
	// "image/*", ".pdf"). Uploads are checked by sniffing their bytes, never
	// by trusting the declared type.
	Accept []string `bcl:"accept" json:"accept,omitempty"`
	// MaxBytes caps each uploaded file (default: the engine's
	// MaxUploadBytes). MaxFiles is how many files the input holds; above 1
	// its value is a list.
	MaxBytes int64 `bcl:"max_bytes" json:"max_bytes,omitempty"`
	MaxFiles int   `bcl:"max_files" json:"max_files,omitempty"`
	// Span is the grid columns the input occupies (layout hint).
	Span int `bcl:"span" json:"span,omitempty"`
	// Compute makes the input derived: the expression is evaluated after
	// every change and the input is never editable.
	Compute string `bcl:"compute" json:"compute,omitempty"`
	// PII marks personal data for retention and erasure.
	PII bool `bcl:"pii" json:"pii,omitempty"`
	// Sealed values are encrypted when saved and can only be read after an
	// opening approved under the pipeline's seal policy.
	Sealed bool `bcl:"sealed" json:"sealed,omitempty"`
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
	// Rules are cross-field checks applied when a stage editing this form
	// advances.
	Rules []Rule `bcl:"rule,block" json:"rules,omitempty"`
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
	// Info blocks are informational content shown on the page (guidance,
	// a privacy notice, fees).
	Info []InfoBlock `bcl:"info,block" json:"info,omitempty"`
	// Acknowledgements are statements the person submitting the stage must
	// accept. Each acceptance is recorded with who, when and a SHA-256 of
	// the exact wording.
	Acknowledgements []AcknowledgementSpec `bcl:"acknowledge,block" json:"acknowledgements,omitempty"`
}

// Info block styles.
const (
	StyleInfo    = "info"
	StyleWarning = "warning"
	StyleSuccess = "success"
	StyleDanger  = "danger"
)

// InfoBlock is informational content on a page.
type InfoBlock struct {
	Name  string `bcl:",id" json:"name"`
	Title string `bcl:"title" json:"title,omitempty"`
	Body  string `bcl:"body" json:"body"`
	// Style is info (default), warning, success or danger.
	Style string `bcl:"style,ident" json:"style,omitempty"`
	// Before names the group the block is shown above (default: the top of
	// the page).
	Before    string   `bcl:"before" json:"before,omitempty"`
	VisibleIf string   `bcl:"visible_if" json:"visible_if,omitempty"`
	Roles     []string `bcl:"roles" json:"roles,omitempty"`
}

// AcknowledgementSpec is a statement that must be accepted before the stage
// is submitted (advanced or approved).
type AcknowledgementSpec struct {
	Name  string `bcl:",id" json:"name"`
	Title string `bcl:"title" json:"title,omitempty"`
	// Text is the exact wording accepted; its SHA-256 is recorded.
	Text string `bcl:"text" json:"text"`
	// Before names the group the checkbox is shown above (default: the end
	// of the page, next to the submit button).
	Before    string `bcl:"before" json:"before,omitempty"`
	VisibleIf string `bcl:"visible_if" json:"visible_if,omitempty"`
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

	// Claimable stages are worked by one person at a time: someone claims
	// (or is routed) the case and only they may act until they release it.
	Claimable bool `bcl:"claimable" json:"claimable,omitempty"`
	// AssignRoles may assign or reassign the stage's work to someone else.
	AssignRoles []string `bcl:"assign_roles" json:"assign_roles,omitempty"`
	// Routing assigns work automatically on entry (implies claimable).
	Routing *Routing `bcl:"routing" json:"routing,omitempty"`
	// SLA replaces Due with working-time deadlines and escalation.
	SLA *SLA `bcl:"sla" json:"sla,omitempty"`
	// SuspendRoles may put the case on hold at this stage (awaiting
	// documents, an inspection) and resume it.
	SuspendRoles []string `bcl:"suspend_roles" json:"suspend_roles,omitempty"`
	// ExternalRoles may issue signed links that let an outside party
	// (a referee, an employer) fill selected inputs of this stage.
	ExternalRoles []string `bcl:"external_roles" json:"external_roles,omitempty"`
	// Rules are cross-field checks applied when the stage advances.
	Rules []Rule `bcl:"rule,block" json:"rules,omitempty"`
	// Reviews are human-in-the-loop review modes of the stage (diff, gate,
	// triage, sampling); several may be combined.
	Reviews []Review `bcl:"review,block" json:"reviews,omitempty"`
	// ConfirmSubmit makes the stage's advance and approve actions two-step:
	// the first submit validates and returns a review with a confirmation
	// token, the second (with the token) commits.
	ConfirmSubmit bool `bcl:"confirm_submit" json:"confirm_submit,omitempty"`
}

// Review modes.
const (
	ReviewDiff     = "diff"
	ReviewGate     = "gate"
	ReviewTriage   = "triage"
	ReviewSampling = "sampling"
)

// Review is one human-in-the-loop review mode of a stage, named by its mode:
//
//	review "diff"     { against approved }                  what changed since the last submission or approval
//	review "gate"     { approvals 2  roles ["senior"] }     N distinct approvals before the case can advance
//	review "triage"   { bucket "urgent" { condition "..."  priority 1  queue "urgent" } }
//	review "sampling" { percent 20  always_review_if ["..."] }
type Review struct {
	Mode string `bcl:",id" json:"mode"`

	// Against (diff) is the baseline: submission (the previous submission,
	// the default) or approved (the last approved version).
	Against string `bcl:"against,ident" json:"against,omitempty"`

	// Approvals (gate) is how many distinct reviewers must approve; Roles
	// restricts who counts. Node names the gate node (default "gate").
	Approvals int      `bcl:"approvals" json:"approvals,omitempty"`
	Roles     []string `bcl:"roles" json:"roles,omitempty"`
	Node      string   `bcl:"node" json:"node,omitempty"`

	// Buckets (triage) classify a case on arrival, first match wins; a case
	// matching none gets DefaultPriority and DefaultQueue.
	Buckets         []TriageBucket `bcl:"bucket,block" json:"buckets,omitempty"`
	DefaultPriority int            `bcl:"default_priority" json:"default_priority,omitempty"`
	DefaultQueue    string         `bcl:"default_queue" json:"default_queue,omitempty"`

	// Percent (sampling) of cases, chosen deterministically from a hash of
	// the case id (and Salt), require human review; so do cases matching
	// SampleIf and, whatever the sample, AlwaysReviewIf. The rest pass
	// without review.
	Percent        float64  `bcl:"percent" json:"percent,omitempty"`
	SampleIf       string   `bcl:"sample_if" json:"sample_if,omitempty"`
	AlwaysReviewIf []string `bcl:"always_review_if" json:"always_review_if,omitempty"`
	Salt           string   `bcl:"salt" json:"salt,omitempty"`
}

// TriageBucket is one priority/queue class of a triage review.
type TriageBucket struct {
	Name string `bcl:",id" json:"name"`
	// Condition classifies a case into the bucket (empty matches every
	// case). ("when" is a BCL keyword.)
	Condition string `bcl:"condition" json:"condition,omitempty"`
	// Priority orders work lists: 1 is the most urgent.
	Priority int    `bcl:"priority" json:"priority"`
	Queue    string `bcl:"queue" json:"queue,omitempty"`
	// Roles narrow routing of the bucket's cases (e.g. senior officers for
	// urgent work) when the stage routes automatically.
	Roles []string `bcl:"roles" json:"roles,omitempty"`
}

// Notification severities. Urgent and critical notifications bypass quiet
// hours and digests.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityUrgent   = "urgent"
	SeverityCritical = "critical"
)

// NotifyRule sends a notification to people when an event happens.
type NotifyRule struct {
	// Event is an event name, a prefix pattern ("sla.*") or "*".
	Event string `bcl:",id" json:"event"`
	// To names recipients: assignee, previous_assignee, applicant, actor,
	// role:<role> (every directory worker holding it) or a user id.
	To []string `bcl:"to" json:"to"`
	// Channels are delivered by default; a recipient may opt out of them, or
	// into the host's other channels, per event.
	Channels []string `bcl:"channels" json:"channels,omitempty"`
	// Severity: info (default), warning, urgent or critical.
	Severity string `bcl:"severity,ident" json:"severity,omitempty"`
	// Subject and Body are templates: {event}, {stage}, {actor},
	// {case.number}, {case.id}, {case.status} and {data.<form>.<input>}.
	Subject string `bcl:"subject" json:"subject,omitempty"`
	Body    string `bcl:"body" json:"body,omitempty"`
	Stage   string `bcl:"stage" json:"stage,omitempty"`
	// Condition must hold for the rule to fire; the environment is the case
	// plus event {name, stage, actor, detail}.
	Condition string `bcl:"condition" json:"condition,omitempty"`
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
	NodeVote        = "vote"
	// NodeGate is a review gate: the stage cannot advance until enough
	// distinct reviewers approve. A review "gate" block declares one.
	NodeGate = "gate"
)

// Consensus policies of a vote node.
const (
	ConsensusUnanimous = "unanimous"
	ConsensusMajority  = "majority"
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
	// Voters is how many distinct people a vote node needs; Consensus is
	// unanimous (default), majority, or a number of approving votes.
	Voters    int    `bcl:"voters" json:"voters,omitempty"`
	Consensus string `bcl:"consensus" json:"consensus,omitempty"`
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
	SkipNodes bool `bcl:"skip_nodes" json:"skip_nodes,omitempty"`
	// Confirm is the question a client asks before taking the action. With
	// ConfirmSubmit the server enforces it: the first request returns a
	// review and a confirmation token, and only a second request carrying
	// the token takes the action.
	Confirm       string `bcl:"confirm" json:"confirm,omitempty"`
	ConfirmSubmit bool   `bcl:"confirm_submit" json:"confirm_submit,omitempty"`
	Condition     string `bcl:"condition" json:"condition,omitempty"`
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
