package platform

// Dynamic definitions: node types, helpers and handlers written in BCL and SPL,
// with no Go and no rebuild. They are expanded into the ordinary document model
// (scripts, intents, routes, nodes) before validation, so everything below the
// expansion — the DAG compiler, the effect barrier, authorization, the sandbox
// — treats them like any other block (see definitions.go).
//
// Every definition is governed the same way:
//
//   - it runs only in the SPL sandbox, which has every host capability denied;
//   - its limits (timeout, steps, heap) are clamped by `definition_policy`;
//   - it may name a `publisher` whose roles/permissions must allow publishing
//     it, and with `require_signature` it must carry that publisher's Ed25519
//     signature over its full declaration, or the application does not start.

// DefinitionPolicySpec is the application-wide rule set for dynamic
// definitions. Without the block, definitions load unsigned (development);
// production declares `require_signature true`.
//
//	definition_policy {
//	  require_signature true
//	  publish_permission "definition:publish"   # default
//	  max_timeout "2s"
//	  max_steps 1000000
//	}
type DefinitionPolicySpec struct {
	// RequireSignature refuses any definition without a valid signature from an
	// enabled publisher allowed to publish it.
	RequireSignature bool `bcl:"require_signature"`
	// PublishPermission is the permission a publisher must hold (directly, via a
	// role, or as "*") for definitions of any kind. A publisher may instead hold
	// "<permission>:<kind>" (kind is node_type, helper or handler) to publish one
	// kind only. Default "definition:publish".
	PublishPermission string `bcl:"publish_permission"`
	// MaxTimeout, MaxSteps and MaxHeapMB are ceilings. A definition that asks for
	// more is refused at load; one that asks for nothing gets the ceiling.
	MaxTimeout string `bcl:"max_timeout"`
	MaxSteps   int64  `bcl:"max_steps"`
	MaxHeapMB  int64  `bcl:"max_heap_mb"`
	// AllowPublic permits handlers declared `public true`. Default false: a
	// production policy that forgot to opt in cannot ship an open handler.
	// (With no definition_policy block, public handlers are allowed.)
	AllowPublic *bool `bcl:"allow_public"`
}

// PublisherSpec is an identity allowed to publish definitions. Its authority
// comes from the same role model as everything else: permissions listed here
// plus those of its roles (and their `inherits`).
//
//	publisher "platform_team" {
//	  public_key file or inline PEM (Ed25519 public key)
//	  roles ["definition_admin"]
//	}
type PublisherSpec struct {
	Name        string   `bcl:",id"`
	Description string   `bcl:"description"`
	PublicKey   string   `bcl:"public_key"`
	Algorithm   string   `bcl:"algorithm"`
	Roles       []string `bcl:"roles"`
	Permissions []string `bcl:"permissions"`
	Disabled    bool     `bcl:"disabled"`
}

// DefParamSpec declares one configuration key of a node type, checked at load
// time at every node that uses the type.
type DefParamSpec struct {
	Name     string `bcl:",id"`
	Type     string `bcl:"type"` // string, number, bool, list, object, any (default)
	Required bool   `bcl:"required"`
	Default  any    `bcl:"default"`
	Summary  string `bcl:"summary"`
}

// NodeTypeSpec defines a reusable node implemented by an SPL script. A node
// that says `uses "<name>"` is rewritten to the script action with the node's
// config as the script's `params`, after the config is checked against `param`
// blocks.
//
//	node_type "risk.score" {
//	  description "Score an order against a threshold"
//	  param "threshold" { type "number" default 100 }
//	  authz { permissions ["risk:score"] }
//	  file "scripts/risk_score.spl"   # {"high": order.total > params.threshold}, as plain SPL
//	}
type NodeTypeSpec struct {
	Name        string `bcl:",id"`
	Description string `bcl:"description"`
	// Kind is pure (default) or decision.
	Kind   string         `bcl:"kind,ident"`
	Params []DefParamSpec `bcl:"param,block"`
	// Authz is enforced on every node of this type. A node cannot declare its
	// own (it could only weaken it).
	Authz *AuthzSpec `bcl:"authz"`

	Source    string `bcl:"source"`
	File      string `bcl:"file"`
	Timeout   string `bcl:"timeout"`
	MaxSteps  int64  `bcl:"max_steps"`
	MaxHeapMB int64  `bcl:"max_heap_mb"`

	Publisher string `bcl:"publisher"`
	Signature string `bcl:"signature"`
	Disabled  bool   `bcl:"disabled"`
}

// HelperSpec defines a pure function, written as a BCL expression (`expr`) or an
// SPL script, called by the `helper.call` action from any intent or pipeline.
//
//	helper "vat" {
//	  params [amount, rate]
//	  expr "amount * rate / 100"
//	}
type HelperSpec struct {
	Name        string   `bcl:",id"`
	Description string   `bcl:"description"`
	Params      []string `bcl:"params"`
	Expr        string   `bcl:"expr"`

	Source    string `bcl:"source"`
	File      string `bcl:"file"`
	Timeout   string `bcl:"timeout"`
	MaxSteps  int64  `bcl:"max_steps"`
	MaxHeapMB int64  `bcl:"max_heap_mb"`

	Publisher string `bcl:"publisher"`
	Signature string `bcl:"signature"`
	Disabled  bool   `bcl:"disabled"`
}

// HandlerSpec defines an HTTP endpoint implemented by a script or a helper. It
// expands into one intent and one route. It is closed by default: a handler
// must name an authenticator and an authz rule, or say `public true`.
//
//	handler "quote" {
//	  method POST
//	  path "/api/quote"
//	  auth "bearer"
//	  authz { permissions ["quote:create"] authorizer "rbac" }
//	  file "scripts/quote.spl"        # {"total": input.qty * input.price}, as plain SPL
//	}
type HandlerSpec struct {
	Name        string   `bcl:",id"`
	Description string   `bcl:"description"`
	Method      string   `bcl:"method,ident"`
	Path        string   `bcl:"path"`
	Tags        []string `bcl:"tags"`

	Session string `bcl:"session"`
	Auth    string `bcl:"auth"`
	// Public marks the handler as reachable without authentication.
	Public      bool             `bcl:"public"`
	Authz       *AuthzSpec       `bcl:"authz"`
	RateLimit   *RateLimitSpec   `bcl:"rate_limit"`
	Idempotency *IdempotencySpec `bcl:"idempotency"`
	// Audit names the audit resource every call is recorded in.
	Audit string `bcl:"audit"`

	InputSchema  string   `bcl:"input_schema"`
	OutputSchema string   `bcl:"output_schema"`
	Timeout      Duration `bcl:"timeout"`
	MaxBodyBytes int64    `bcl:"max_body_bytes"`
	Status       int      `bcl:"status"`

	// Exactly one of Source, File or Helper implements the handler. A helper's
	// arguments come from the same-named keys of the request body.
	Source string `bcl:"source"`
	File   string `bcl:"file"`
	Helper string `bcl:"helper"`

	ScriptTimeout string `bcl:"script_timeout"`
	MaxSteps      int64  `bcl:"max_steps"`
	MaxHeapMB     int64  `bcl:"max_heap_mb"`

	Publisher string `bcl:"publisher"`
	Signature string `bcl:"signature"`
	Disabled  bool   `bcl:"disabled"`
}

// DefinitionInfo describes one loaded dynamic definition, for inventory,
// audit and the console. Hash covers the resolved source.
type DefinitionInfo struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Publisher string `json:"publisher,omitempty"`
	Signed    bool   `json:"signed"`
	Hash      string `json:"hash"`
	// Description and Params let a console list what a definition offers.
	Description string   `json:"description,omitempty"`
	Params      []string `json:"params,omitempty"`
}
