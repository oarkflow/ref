package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/ref/signing"
)

const (
	defKindNodeType = "node_type"
	defKindHelper   = "helper"
	defKindHandler  = "handler"

	defaultPublishPermission = "definition:publish"
	defaultHandlerBodyLimit  = 1 << 20
)

// definitionSet is what expansion leaves on the Platform: the helpers the
// `helper.call` action runs and the inventory of everything that was loaded.
type definitionSet struct {
	helpers map[string]*Helper
	info    []DefinitionInfo
}

// Helper is a compiled pure function: a BCL expression or a script id.
type Helper struct {
	Name   string
	Params []string
	expr   *Expression
	script string
}

// Definitions lists every dynamic definition loaded into this generation.
func (p *Platform) Definitions() []DefinitionInfo {
	if p == nil || p.defs == nil {
		return nil
	}
	return slices.Clone(p.defs.info)
}

// callHelper evaluates a helper over its named arguments.
func (p *Platform) callHelper(name string, args map[string]any) (any, error) {
	var h *Helper
	if p != nil && p.defs != nil {
		h = p.defs.helpers[name]
	}
	if h == nil {
		return nil, fmt.Errorf("unknown helper %q", name)
	}
	for _, param := range h.Params {
		if _, ok := args[param]; !ok {
			return nil, fmt.Errorf("helper %s: missing argument %q", name, param)
		}
	}
	if h.expr != nil {
		return h.expr.Eval(Env(args))
	}
	sc := p.scripts[h.script]
	if sc == nil {
		return nil, fmt.Errorf("helper %s: script %q is not loaded", name, h.script)
	}
	data := make(map[string]any, len(args))
	for k, v := range args {
		data[k] = v
	}
	data, err := plainData(data)
	if err != nil {
		return nil, fmt.Errorf("helper %s: %w", name, err)
	}
	return sc.Run(data)
}

// definition is the common view of the three kinds during expansion.
type definition struct {
	kind, name        string
	publisher, sig    string
	source            string
	spec              any // normalized copy that the signature covers
	timeout           string
	maxSteps, maxHeap int64
	description       string
	params            []string
}

// expandDefinitions verifies every dynamic definition against the policy, then
// rewrites the document: scripts are added for their sources, node types are
// replaced at their use sites, handlers become intents and routes. It runs
// before script compilation and before validation, so a definition that is
// unsigned, unauthorised or malformed stops the load.
func expandDefinitions(doc Document, fsys fs.FS, reg *Registry) (Document, *definitionSet, error) {
	set := &definitionSet{helpers: map[string]*Helper{}}
	if len(doc.NodeTypes)+len(doc.Helpers)+len(doc.Handlers) == 0 {
		if len(doc.Publishers) > 0 || doc.DefinitionPolicy != nil {
			// Nothing to govern yet; still validate the publishers so a bad key
			// is caught the day it is added, not the day the first definition is.
			if _, err := loadPublishers(doc); err != nil {
				return doc, nil, err
			}
		}
		return doc, set, nil
	}
	policy := doc.DefinitionPolicy
	if policy == nil {
		policy = &DefinitionPolicySpec{}
	}
	publishers, err := loadPublishers(doc)
	if err != nil {
		return doc, nil, err
	}
	var maxTimeout time.Duration
	if policy.MaxTimeout != "" {
		if maxTimeout, err = time.ParseDuration(policy.MaxTimeout); err != nil || maxTimeout <= 0 {
			return doc, nil, fmt.Errorf("definition_policy: max_timeout %q is not a duration", policy.MaxTimeout)
		}
	}

	taken := map[string]string{}
	claim := func(kind, name string) error {
		if name == "" {
			return fmt.Errorf("%s block needs a name", kind)
		}
		key := kind + "/" + name
		if _, dup := taken[key]; dup {
			return fmt.Errorf("%s %q is declared twice", kind, name)
		}
		taken[key] = name
		return nil
	}

	var (
		defs      []definition
		nodeTypes = map[string]NodeTypeSpec{}
		handlers  []HandlerSpec
		helperSrc = map[string]HelperSpec{}
	)
	for _, nt := range doc.NodeTypes {
		if nt.Disabled {
			continue
		}
		if err := claim(defKindNodeType, nt.Name); err != nil {
			return doc, nil, err
		}
		if reg != nil {
			if _, builtin := reg.action(nt.Name); builtin {
				return doc, nil, fmt.Errorf("node_type %q would shadow the built-in action of that name; choose another name", nt.Name)
			}
		}
		if nt.Kind == "" {
			nt.Kind = "pure"
		}
		if nt.Kind != "pure" && nt.Kind != "decision" {
			return doc, nil, fmt.Errorf("node_type %q: kind %q is not allowed; a scripted node is pure or decision", nt.Name, nt.Kind)
		}
		if nt.Authz != nil && nt.Authz.Empty() {
			return doc, nil, fmt.Errorf("node_type %q: authz declares no rule", nt.Name)
		}
		if err := checkParams(nt); err != nil {
			return doc, nil, err
		}
		src, err := definitionSource(fsys, defKindNodeType, nt.Name, nt.Source, nt.File)
		if err != nil {
			return doc, nil, err
		}
		norm := nt
		norm.Source, norm.File, norm.Signature = src, "", ""
		nodeTypes[nt.Name] = nt
		defs = append(defs, definition{kind: defKindNodeType, name: nt.Name, publisher: nt.Publisher, sig: nt.Signature,
			source: src, spec: norm, timeout: nt.Timeout, maxSteps: nt.MaxSteps, maxHeap: nt.MaxHeapMB,
			description: nt.Description, params: paramNames(nt)})
	}
	for _, h := range doc.Helpers {
		if h.Disabled {
			continue
		}
		if err := claim(defKindHelper, h.Name); err != nil {
			return doc, nil, err
		}
		src := ""
		switch {
		case h.Expr != "" && (h.Source != "" || h.File != ""):
			return doc, nil, fmt.Errorf("helper %q: set expr or source/file, not both", h.Name)
		case h.Expr != "":
			src = h.Expr
			if _, err := CompileExpr(h.Expr); err != nil {
				return doc, nil, fmt.Errorf("helper %q: %w", h.Name, err)
			}
		default:
			var err error
			if src, err = definitionSource(fsys, defKindHelper, h.Name, h.Source, h.File); err != nil {
				return doc, nil, err
			}
		}
		norm := h
		norm.Source, norm.File, norm.Signature = strings.TrimSpace(h.Source), "", ""
		if h.Expr == "" {
			norm.Source = src
		}
		helperSrc[h.Name] = h
		defs = append(defs, definition{kind: defKindHelper, name: h.Name, publisher: h.Publisher, sig: h.Signature,
			source: src, spec: norm, timeout: h.Timeout, maxSteps: h.MaxSteps, maxHeap: h.MaxHeapMB,
			description: h.Description, params: slices.Clone(h.Params)})
	}
	for _, h := range doc.Handlers {
		if h.Disabled {
			continue
		}
		if err := claim(defKindHandler, h.Name); err != nil {
			return doc, nil, err
		}
		if err := checkHandler(h, policy); err != nil {
			return doc, nil, err
		}
		src := ""
		if h.Helper == "" {
			var err error
			if src, err = definitionSource(fsys, defKindHandler, h.Name, h.Source, h.File); err != nil {
				return doc, nil, err
			}
		}
		norm := h
		norm.Source, norm.File, norm.Signature = src, "", ""
		handlers = append(handlers, h)
		defs = append(defs, definition{kind: defKindHandler, name: h.Name, publisher: h.Publisher, sig: h.Signature,
			source: src, spec: norm, timeout: h.ScriptTimeout, maxSteps: h.MaxSteps, maxHeap: h.MaxHeapMB,
			description: h.Description})
	}

	// Policy: publisher authority and signatures, before anything is built.
	publishPerm := policy.PublishPermission
	if publishPerm == "" {
		publishPerm = defaultPublishPermission
	}
	for i := range defs {
		d := &defs[i]
		hash := sha256.Sum256([]byte(d.source))
		info := DefinitionInfo{Kind: d.kind, Name: d.name, Publisher: d.publisher, Hash: hex.EncodeToString(hash[:])}
		if policy.RequireSignature && (d.publisher == "" || d.sig == "") {
			return doc, nil, fmt.Errorf("%s %q: definition_policy requires a publisher and signature", d.kind, d.name)
		}
		if d.sig != "" && d.publisher == "" {
			return doc, nil, fmt.Errorf("%s %q: a signature needs a publisher", d.kind, d.name)
		}
		if d.publisher != "" {
			pub, ok := publishers[d.publisher]
			if !ok {
				return doc, nil, fmt.Errorf("%s %q: publisher %q is not declared or is disabled", d.kind, d.name, d.publisher)
			}
			if !pub.perms.allows(publishPerm) && !pub.perms.allows(publishPerm+":"+d.kind) {
				return doc, nil, fmt.Errorf("%s %q: publisher %q does not hold %q", d.kind, d.name, d.publisher, publishPerm)
			}
			if d.sig != "" {
				if err := verifyDefinition(pub.key, d.kind, d.spec, d.sig); err != nil {
					return doc, nil, fmt.Errorf("%s %q: signature by %q does not match the definition: %w", d.kind, d.name, d.publisher, err)
				}
				info.Signed = true
			}
		}
		info.Description, info.Params = d.description, d.params
		set.info = append(set.info, info)
		slog.Info("definition loaded", "kind", d.kind, "name", d.name, "publisher", d.publisher, "signed", info.Signed, "sha256", info.Hash)
	}

	// Build scripts. Limits are clamped by the policy.
	scripts := slices.Clone(doc.Scripts)
	scriptID := func(kind, name string) string { return kind + "." + name }
	for _, d := range defs {
		if d.kind == defKindHelper && helperSrc[d.name].Expr != "" {
			continue
		}
		if d.kind == defKindHandler && d.source == "" {
			continue
		}
		timeout, steps, heap, err := clampLimits(d, policy, maxTimeout)
		if err != nil {
			return doc, nil, err
		}
		scripts = append(scripts, ScriptSpec{
			Name: scriptID(d.kind, d.name), Description: d.kind + " " + d.name,
			Source: d.source, Timeout: timeout, MaxSteps: steps, MaxHeapMB: heap,
		})
	}
	doc.Scripts = scripts

	// Helpers.
	for name, h := range helperSrc {
		ch := &Helper{Name: name, Params: slices.Clone(h.Params)}
		if h.Expr != "" {
			ch.expr, _ = CompileExpr(h.Expr)
		} else {
			ch.script = scriptID(defKindHelper, name)
		}
		set.helpers[name] = ch
	}
	for _, h := range handlers {
		if h.Helper != "" {
			if _, ok := set.helpers[h.Helper]; !ok {
				return doc, nil, fmt.Errorf("handler %q: unknown helper %q", h.Name, h.Helper)
			}
		}
	}

	// Handlers become intents and routes.
	for _, h := range handlers {
		intent, route := expandHandler(h, scriptID(defKindHandler, h.Name), set.helpers[h.Helper])
		doc.Intents = append(doc.Intents, intent)
		doc.Routes = append(doc.Routes, route)
	}

	// Node types are rewritten where they are used.
	if len(nodeTypes) > 0 {
		var err error
		if doc.Intents, err = rewriteIntentNodes(doc.Intents, nodeTypes, scriptID); err != nil {
			return doc, nil, err
		}
		exts := slices.Clone(doc.Extensions)
		for i := range exts {
			nodes, err := rewriteNodes("extend "+exts[i].Name, exts[i].Nodes, nodeTypes, scriptID)
			if err != nil {
				return doc, nil, err
			}
			exts[i].Nodes = nodes
		}
		doc.Extensions = exts
	}

	doc.NodeTypes, doc.Helpers, doc.Handlers = nil, nil, nil
	return doc, set, nil
}

// clampLimits applies the policy ceilings. A definition asking for more than a
// ceiling is refused; one asking for nothing takes the ceiling.
func clampLimits(d definition, policy *DefinitionPolicySpec, maxTimeout time.Duration) (string, int64, int64, error) {
	timeout, steps, heap := d.timeout, d.maxSteps, d.maxHeap
	if maxTimeout > 0 {
		if timeout == "" {
			timeout = maxTimeout.String()
		} else if v, err := time.ParseDuration(timeout); err != nil || v <= 0 {
			return "", 0, 0, fmt.Errorf("%s %q: timeout %q is not a duration", d.kind, d.name, timeout)
		} else if v > maxTimeout {
			return "", 0, 0, fmt.Errorf("%s %q: timeout %s is over the policy ceiling %s", d.kind, d.name, v, maxTimeout)
		}
	}
	if policy.MaxSteps > 0 {
		if steps == 0 {
			steps = policy.MaxSteps
		} else if steps > policy.MaxSteps {
			return "", 0, 0, fmt.Errorf("%s %q: max_steps %d is over the policy ceiling %d", d.kind, d.name, steps, policy.MaxSteps)
		}
	}
	if policy.MaxHeapMB > 0 {
		if heap == 0 {
			heap = policy.MaxHeapMB
		} else if heap > policy.MaxHeapMB {
			return "", 0, 0, fmt.Errorf("%s %q: max_heap_mb %d is over the policy ceiling %d", d.kind, d.name, heap, policy.MaxHeapMB)
		}
	}
	return timeout, steps, heap, nil
}

func definitionSource(fsys fs.FS, kind, name, source, file string) (string, error) {
	switch {
	case source != "" && file != "":
		return "", fmt.Errorf("%s %q: set source or file, not both", kind, name)
	case source == "" && file == "":
		return "", fmt.Errorf("%s %q: needs source or file", kind, name)
	case file != "":
		if !strings.HasSuffix(file, ".spl") {
			return "", fmt.Errorf("%s %q: file %q must be an .spl file", kind, name, file)
		}
		src, err := readScriptFile(fsys, file)
		if err != nil {
			return "", fmt.Errorf("%s %q: %w", kind, name, err)
		}
		return src, nil
	}
	return source, nil
}

// ---------------------------------------------------------------------------
// Publishers and authority
// ---------------------------------------------------------------------------

type permissionSet map[string]bool

func (s permissionSet) allows(permission string) bool { return s["*"] || s[permission] }

type loadedPublisher struct {
	key   *signing.Key
	perms permissionSet
}

func loadPublishers(doc Document) (map[string]loadedPublisher, error) {
	roles := map[string]RoleSpec{}
	for _, r := range doc.Roles {
		roles[r.Name] = r
	}
	var flatten func(name string, seen map[string]bool, into permissionSet) error
	flatten = func(name string, seen map[string]bool, into permissionSet) error {
		role, ok := roles[name]
		if !ok {
			return fmt.Errorf("role %q is not declared", name)
		}
		if seen[name] {
			return nil
		}
		seen[name] = true
		for _, p := range role.Permissions {
			into[p] = true
		}
		for _, parent := range role.Inherits {
			if err := flatten(parent, seen, into); err != nil {
				return err
			}
		}
		return nil
	}
	out := map[string]loadedPublisher{}
	for _, p := range doc.Publishers {
		if p.Name == "" {
			return nil, fmt.Errorf("publisher block needs a name")
		}
		if _, dup := out[p.Name]; dup {
			return nil, fmt.Errorf("publisher %q is declared twice", p.Name)
		}
		if p.Disabled {
			continue
		}
		var key *signing.Key
		if p.PublicKey != "" {
			alg, err := signing.ParseAlgorithm(p.Algorithm)
			if err != nil {
				return nil, fmt.Errorf("publisher %q: %w", p.Name, err)
			}
			if key, err = signing.ParsePublicKeyPEM(p.Name, alg, []byte(p.PublicKey)); err != nil {
				return nil, fmt.Errorf("publisher %q: %w", p.Name, err)
			}
		}
		perms := permissionSet{}
		for _, perm := range p.Permissions {
			perms[perm] = true
		}
		for _, role := range p.Roles {
			if err := flatten(role, map[string]bool{}, perms); err != nil {
				return nil, fmt.Errorf("publisher %q: %w", p.Name, err)
			}
		}
		out[p.Name] = loadedPublisher{key: key, perms: perms}
	}
	return out, nil
}

// definitionPayload is the canonical bytes a signature covers: the kind and the
// whole declaration, with the source resolved from its file and the signature
// itself cleared. Changing any field — the authz rule, a limit, the path of a
// handler — invalidates it.
func definitionPayload(kind string, spec any) ([]byte, error) {
	return signing.Canonical(map[string]any{"kind": kind, "definition": spec})
}

func verifyDefinition(key *signing.Key, kind string, spec any, signature string) error {
	if key == nil {
		return fmt.Errorf("the publisher has no public_key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("the signature is not base64url")
	}
	payload, err := definitionPayload(kind, spec)
	if err != nil {
		return err
	}
	if !key.Verify(payload, raw) {
		return signing.ErrInvalidSignature
	}
	return nil
}

// SignDefinition signs a NodeTypeSpec, HelperSpec or HandlerSpec for its
// `signature` attribute. A `file` source is read from fsys and signed by
// content, so the file cannot change afterwards without invalidating it.
func SignDefinition(key *signing.Key, def any, fsys fs.FS) (string, error) {
	var kind string
	var spec any
	switch d := def.(type) {
	case NodeTypeSpec:
		src, err := definitionSource(fsys, defKindNodeType, d.Name, d.Source, d.File)
		if err != nil {
			return "", err
		}
		d.Source, d.File, d.Signature = src, "", ""
		kind, spec = defKindNodeType, d
	case HelperSpec:
		if d.Expr == "" {
			src, err := definitionSource(fsys, defKindHelper, d.Name, d.Source, d.File)
			if err != nil {
				return "", err
			}
			d.Source = src
		}
		d.File, d.Signature = "", ""
		kind, spec = defKindHelper, d
	case HandlerSpec:
		if d.Helper == "" {
			src, err := definitionSource(fsys, defKindHandler, d.Name, d.Source, d.File)
			if err != nil {
				return "", err
			}
			d.Source = src
		}
		d.File, d.Signature = "", ""
		kind, spec = defKindHandler, d
	default:
		return "", fmt.Errorf("cannot sign a %T", def)
	}
	payload, err := definitionPayload(kind, spec)
	if err != nil {
		return "", err
	}
	sig, err := key.Sign(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// ---------------------------------------------------------------------------
// Node types
// ---------------------------------------------------------------------------

var paramTypes = map[string]bool{"": true, "any": true, "string": true, "number": true, "bool": true, "list": true, "object": true}

func checkParams(nt NodeTypeSpec) error {
	seen := map[string]bool{}
	for _, p := range nt.Params {
		if p.Name == "" || seen[p.Name] {
			return fmt.Errorf("node_type %q: param needs a unique name", nt.Name)
		}
		seen[p.Name] = true
		if !paramTypes[p.Type] {
			return fmt.Errorf("node_type %q: param %q has unknown type %q", nt.Name, p.Name, p.Type)
		}
		if p.Default != nil && !paramFits(p.Type, p.Default) {
			return fmt.Errorf("node_type %q: default of param %q is not a %s", nt.Name, p.Name, p.Type)
		}
	}
	return nil
}

func paramFits(typ string, v any) bool {
	switch typ {
	case "", "any":
		return true
	case "string":
		_, ok := v.(string)
		return ok
	case "bool":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := ToFloat(v)
		_, isStr := v.(string)
		return ok && !isStr
	case "list":
		switch v.(type) {
		case []any, []string:
			return true
		}
		return false
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}

func rewriteIntentNodes(intents []IntentSpec, types map[string]NodeTypeSpec, id func(kind, name string) string) ([]IntentSpec, error) {
	out := slices.Clone(intents)
	for i := range out {
		nodes, err := rewriteNodes("intent "+out[i].Name, out[i].Nodes, types, id)
		if err != nil {
			return nil, err
		}
		out[i].Nodes = nodes
	}
	return out, nil
}

func rewriteNodes(where string, nodes []NodeSpec, types map[string]NodeTypeSpec, id func(kind, name string) string) ([]NodeSpec, error) {
	out := slices.Clone(nodes)
	for i := range out {
		n := &out[i]
		nt, ok := types[n.Uses]
		if !ok {
			continue
		}
		at := fmt.Sprintf("%s, node %q (node_type %q)", where, n.Name, nt.Name)
		if len(n.Provides) == 0 {
			return nil, fmt.Errorf("%s: needs at least one name in provides", at)
		}
		if n.Kind != "" && n.Kind != nt.Kind {
			return nil, fmt.Errorf("%s: kind %q conflicts with the type's kind %q", at, n.Kind, nt.Kind)
		}
		if n.Resource != "" {
			return nil, fmt.Errorf("%s: a scripted node takes no resource", at)
		}
		if nt.Authz != nil {
			if n.Authz != nil {
				return nil, fmt.Errorf("%s: the node type already enforces authz; a node cannot declare its own", at)
			}
			a := *nt.Authz
			n.Authz = &a
		}
		params, err := bindParams(nt, n.Config)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		n.Uses = "script"
		n.Kind = nt.Kind
		n.Config = map[string]any{"script": id(defKindNodeType, nt.Name), "params": params}
	}
	return out, nil
}

func bindParams(nt NodeTypeSpec, cfg map[string]any) (map[string]any, error) {
	declared := map[string]DefParamSpec{}
	for _, p := range nt.Params {
		declared[p.Name] = p
	}
	for k := range cfg {
		if _, ok := declared[k]; !ok {
			names := make([]string, 0, len(declared))
			for n := range declared {
				names = append(names, n)
			}
			slices.Sort(names)
			return nil, fmt.Errorf("config key %q is not a param of the type (params: %s)", k, strings.Join(names, ", "))
		}
	}
	out := make(map[string]any, len(declared))
	for _, p := range nt.Params {
		v, given := cfg[p.Name]
		switch {
		case given:
			if !paramFits(p.Type, v) {
				return nil, fmt.Errorf("param %q must be a %s", p.Name, p.Type)
			}
			out[p.Name] = v
		case p.Default != nil:
			out[p.Name] = p.Default
		case p.Required:
			return nil, fmt.Errorf("param %q is required", p.Name)
		default:
			out[p.Name] = nil
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func checkHandler(h HandlerSpec, policy *DefinitionPolicySpec) error {
	if h.Method == "" || h.Path == "" {
		return fmt.Errorf("handler %q: needs method and path", h.Name)
	}
	impl := 0
	for _, s := range []string{h.Source, h.File, h.Helper} {
		if s != "" {
			impl++
		}
	}
	if impl != 1 {
		return fmt.Errorf("handler %q: set exactly one of source, file or helper", h.Name)
	}
	if h.Authz != nil && h.Authz.Empty() {
		return fmt.Errorf("handler %q: authz declares no rule", h.Name)
	}
	if h.Public {
		if policy.AllowPublic != nil && !*policy.AllowPublic {
			return fmt.Errorf("handler %q: public handlers are disabled by definition_policy", h.Name)
		}
		return nil
	}
	if h.Auth == "" || h.Authz == nil {
		return fmt.Errorf("handler %q would be reachable without authorization: name an authenticator (auth) and an authz rule, or declare public true", h.Name)
	}
	return nil
}

func expandHandler(h HandlerSpec, script string, helper *Helper) (IntentSpec, RouteSpec) {
	intentName := "handler." + h.Name
	run := NodeSpec{Name: "run", Kind: "pure", Requires: []string{"input"}, Provides: []string{"result"}}
	if helper != nil {
		args := map[string]any{}
		for _, p := range helper.Params {
			args[p] = "input." + p
		}
		run.Uses = "helper.call"
		run.Config = map[string]any{"helper": helper.Name, "args": args}
	} else {
		run.Uses = "script"
		run.Config = map[string]any{"script": script}
	}
	intent := IntentSpec{
		Name:         intentName,
		Description:  h.Description,
		Response:     "result",
		Timeout:      h.Timeout,
		InputSchema:  h.InputSchema,
		OutputSchema: h.OutputSchema,
		Nodes:        []NodeSpec{run},
	}
	if h.Authz != nil {
		a := *h.Authz
		intent.Authz = &a
	}
	route := RouteSpec{
		Name:           intentName,
		Method:         h.Method,
		Path:           h.Path,
		Intent:         intentName,
		Session:        h.Session,
		Auth:           h.Auth,
		AllowAnonymous: h.Public,
		Status:         h.Status,
		Description:    h.Description,
		Tags:           slices.Clone(h.Tags),
		Timeout:        h.Timeout,
		MaxBodyBytes:   h.MaxBodyBytes,
		Authz:          h.Authz,
		RateLimit:      h.RateLimit,
		Idempotency:    h.Idempotency,
		CacheControl:   "no-store",
	}
	if route.MaxBodyBytes == 0 {
		route.MaxBodyBytes = defaultHandlerBodyLimit
	}
	if h.Audit != "" {
		on := true
		route.Audit = &AuditSpec{Enabled: &on, Action: intentName, Resource: h.Audit}
	}
	return intent, route
}

// ---------------------------------------------------------------------------
// helper.call
// ---------------------------------------------------------------------------

func registerDefinitionActions(r *Registry) {
	mustAction(r, "helper.call", helperCallAction, ActionInfo{
		Family:   "compute",
		Summary:  "Call a helper (a pure function written in BCL or SPL) and publish its result",
		Provides: "The helper's result",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "helper", Type: "string", Required: true, Summary: "Name of a top-level helper block"},
			{Name: "args", Type: "object", Summary: "Argument name to BCL expression (a string literal needs its own quotes). A parameter without an entry is read from the fact of the same name"},
		},
	})
}

var helperCallAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if len(spec.Provides) != 1 {
		return nil, fmt.Errorf("node %q: helper.call publishes exactly one fact", spec.Name)
	}
	name := configString(spec.Config, "helper", "")
	if name == "" {
		return nil, fmt.Errorf("node %q: helper.call needs config.helper", spec.Name)
	}
	var h *Helper
	if build.Platform != nil && build.Platform.defs != nil {
		h = build.Platform.defs.helpers[name]
	}
	if h == nil {
		return nil, fmt.Errorf("node %q: unknown helper %q", spec.Name, name)
	}
	args := map[string]*Expression{}
	if raw, ok := spec.Config["args"].(map[string]any); ok {
		for k, v := range raw {
			s, isStr := v.(string)
			if !isStr {
				return nil, fmt.Errorf("node %q: args.%s must be an expression string", spec.Name, k)
			}
			e, err := CompileExpr(s)
			if err != nil {
				return nil, fmt.Errorf("node %q: args.%s: %w", spec.Name, k, err)
			}
			args[k] = e
		}
	}
	for k := range args {
		if !slices.Contains(h.Params, k) {
			return nil, fmt.Errorf("node %q: helper %q has no parameter %q (params: %s)", spec.Name, name, k, strings.Join(h.Params, ", "))
		}
	}
	provides := spec.Provides[0]
	platform := build.Platform
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		call := make(map[string]any, len(h.Params))
		for _, param := range h.Params {
			if e, ok := args[param]; ok {
				v, err := e.Eval(env)
				if err != nil {
					return ActionResult{}, fmt.Errorf("helper %s: %w", name, err)
				}
				call[param] = v
			} else if v, ok := env[param]; ok {
				call[param] = v
			}
		}
		value, err := platform.callHelper(name, call)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Outputs: map[string]any{provides: value}}, nil
	}), nil
})

// plainData reduces values to JSON-shaped data for the interpreter.
func plainData(in map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("arguments are not plain data: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	for k, v := range out {
		out[k] = wholeNumbers(v)
	}
	return out, nil
}

func paramNames(nt NodeTypeSpec) []string {
	out := make([]string, 0, len(nt.Params))
	for _, p := range nt.Params {
		out = append(out, p.Name)
	}
	return out
}

// helperEnvKey carries the platform's helper caller inside an expression's
// variables, so the global `helper(...)` function reaches the right generation.
const helperEnvKey = "__helpers"

type helperCaller func(name string, args []any) (any, error)

// MarshalJSON keeps the caller out of anything that serializes an environment
// (script data, audit records, debug output): it is plumbing, not a fact.
func (helperCaller) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

// exprHelper implements `helper("name", arg, ...)` in expressions. Arguments
// are positional, in the order of the helper's params.
func exprHelper(args []any, opts *bcl.EvalOptions) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("helper takes a helper name and its arguments")
	}
	name, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("helper: the first argument must be the helper's name")
	}
	var call helperCaller
	if opts != nil {
		call, _ = opts.Variables[helperEnvKey].(helperCaller)
	}
	if call == nil {
		return nil, fmt.Errorf("helper %q: no helpers are available here", name)
	}
	return call(name, args[1:])
}

// helperCaller binds this generation's helpers for use in expressions.
func (p *Platform) helperCaller() helperCaller {
	if p == nil || p.defs == nil || len(p.defs.helpers) == 0 {
		return nil
	}
	return func(name string, args []any) (any, error) {
		h := p.defs.helpers[name]
		if h == nil {
			return nil, fmt.Errorf("unknown helper %q", name)
		}
		if len(args) != len(h.Params) {
			return nil, fmt.Errorf("helper %s takes %d argument(s) (%s), got %d", name, len(h.Params), strings.Join(h.Params, ", "), len(args))
		}
		named := make(map[string]any, len(args))
		for i, param := range h.Params {
			named[param] = args[i]
		}
		return p.callHelper(name, named)
	}
}
