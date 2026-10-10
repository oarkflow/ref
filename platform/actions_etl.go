package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/etl"
	"github.com/oarkflow/ref/intent"
)

// ETL actions drive an etl.engine resource. Parameters are read as the
// pipeline actions read them: config.<name>_fact, a literal config.<name>, the
// path parameter, the query parameter, then the request body (the `input`
// fact, so nodes that take a body declare `requires [input]`).
//
// The caller's roles decide what is allowed (admin, ingest, operate, replay,
// read); the engine enforces them, so an action cannot be configured around them.

func registerETLActions(r *Registry) {
	id := ConfigField{Name: "id_fact", Type: "fact", Summary: "Fact path of the batch id (default: :id)"}
	src := ConfigField{Name: "source_fact", Type: "fact", Summary: "Fact path of the source id (default: :source or input.source)"}
	for _, a := range []struct {
		op, kind, summary, provides string
		cfg                         []ConfigField
	}{
		{"me", "read", "What the caller may do: their roles, permissions and the sources each applies to", "{id, roles, permissions}", nil},
		{"permissions", "read", "The permission catalogue", "The permissions", nil},
		{"require", "read", "Fail with PERMISSION_DENIED unless the caller holds config.permission; put it ahead of an application's own operations (users, keys)", "{allowed}", []ConfigField{{Name: "permission", Type: "string", Required: true}}},
		{"note", "effect", "Record an application event in the audit trail (input.action, input.detail); needs config.permission", "{recorded}", []ConfigField{{Name: "permission", Type: "string", Required: true}, {Name: "action", Type: "string", Summary: "Audit action name (default input.action)"}, {Name: "detail_fact", Type: "fact", Summary: "Fact path of the detail text (default input.detail)"}}},
		{"roles", "read", "List roles (needs roles.manage or users.manage)", "The roles", nil},
		{"role_put", "effect", "Create or update a role: {id, name, description, permissions, sources}", "The role", nil},
		{"role_delete", "effect", "Delete a role that is not built in (:id)", "{deleted}", []ConfigField{id}},
		{"monitor", "read", "Monitoring view: throughput series, per-source health, stage timings, queue and alerts (?window=24h)", "The view", nil},
		{"alert_ack", "effect", "Silence an open alert for a while (input.id, input.note, input.minutes); it stays listed, marked acknowledged", "{acknowledged}", nil},
		{"alert_history", "read", "Alerts that opened and cleared, newest first, with who acknowledged them (?limit=)", "The records", nil},
		{"alerts", "read", "Open alerts, most severe first", "The alerts", nil},
		{"logs", "read", "Recent structured logs (?level=, ?batch=, ?trace=, ?q=, ?limit=)", "The entries", nil},
		{"health", "read", "Health probes. Without a token only the overall status; with monitor.read, every check", "{status, checks}", nil},
		{"counters", "read", "The durable counters (they survive restarts and cover every process)", "The counters", nil},
		{"metrics", "read", "Prometheus text metrics", "The text", nil},
		{"summary", "read", "Totals, batches by status, active batches by stage, rows in, quarantined, delivered and lost", "The summary", nil},
		{"sources", "read", "List registered sources", "The sources", nil},
		{"source_put", "effect", "Register or update a source (id, name, owner, format, destination, rules, max_reject_rate, retry); each update bumps its version", "The source", nil},
		{"source_pause", "effect", "Pause or resume a source (input.paused, default true); a paused source refuses new batches", "The source", []ConfigField{src}},
		{"ingest", "effect", "Validate rows from a source and start a batch; idempotent on the key (input.key or the Idempotency-Key header). Body: {source, key, rows | data | object}; object names a file already in the blob store (under its inbox/ prefix), read as a stream so it can be far larger than memory", "{batch, duplicate}", []ConfigField{src}},
		{"advance", "effect", "Run a batch's next stage", "The batch", []ConfigField{id}},
		{"run", "effect", "Run a batch until it is delivered, waiting, held or failed", "The batch", []ConfigField{id}},
		{"replay", "effect", "Replay a held batch from its last checkpoint with the same idempotency key", "The batch", []ConfigField{id}},
		{"batches", "read", "List batches (?source=, ?status=a,b, ?limit=, ?offset=)", "The batches", nil},
		{"batch", "read", "A batch with its lineage, quarantined rows and audit entries", "The trace", []ConfigField{id}},
		{"quarantine", "read", "Recently quarantined rows with the rule that refused them (?limit=)", "The rows", nil},
		{"audit", "read", "The audit trail, newest first (?limit=, ?offset=)", "The entries", nil},
		{"verify_audit", "read", "Check the audit trail's hash chain; bad_seq is 0 when it is intact", "{ok, bad_seq, total}", nil},
	} {
		mustAction(r, "etl."+a.op, etlAction(a.op), ActionInfo{Family: "data", Kind: a.kind, Summary: a.summary, Provides: a.provides,
			ResourceKind: "etl", Config: a.cfg})
	}
}

func etlAction(op string) ActionFactory {
	return ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
		res, err := requireResource[*ETLEngine](build, spec, "an etl.engine resource")
		if err != nil {
			return nil, err
		}
		if err := rejectUnknownConfig("etl."+op, spec.Config, "id_fact", "id", "source_fact", "source", "limit", "permission", "action", "detail_fact"); err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		limit, err := configInt(spec.Config, "limit", 50)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", spec.Name, err)
		}
		if op == "require" && configString(spec.Config, "permission", "") == "" {
			return nil, fmt.Errorf("node %q: etl.require needs config.permission", spec.Name)
		}
		if op == "note" && configString(spec.Config, "permission", "") == "" && configString(spec.Config, "action", "") == "" {
			return nil, fmt.Errorf("node %q: etl.note without a permission needs a fixed config.action", spec.Name)
		}
		h := &etlHandler{res: res, spec: spec, op: op, limit: limit}
		return ActionFunc(h.run), nil
	})
}

type etlHandler struct {
	res   *ETLEngine
	spec  NodeSpec
	op    string
	limit int
}

func (h *etlHandler) param(ctx *ActionContext, key string) string {
	if path := configString(h.spec.Config, key+"_fact", ""); path != "" {
		if v, ok := resolvePath(orgRoot(ctx), path); ok && v != nil {
			return strings.TrimSpace(Stringify(v))
		}
		return ""
	}
	if lit := configString(h.spec.Config, key, ""); lit != "" {
		return lit
	}
	for _, from := range []string{"path", "query"} {
		if v, ok := requestValue(ctx, from, key); ok && v != "" {
			return v
		}
	}
	if v, ok := resolvePath(ctx.Inputs, "input."+key); ok && v != nil {
		return strings.TrimSpace(Stringify(v))
	}
	return ""
}

func (h *etlHandler) out(v any) (ActionResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return ActionResult{}, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ActionResult{}, err
	}
	return acknowledgement(h.spec, decoded), nil
}

func (h *etlHandler) body(ctx *ActionContext) map[string]any {
	m, _ := ctx.Inputs["input"].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func (h *etlHandler) intParam(ctx *ActionContext, key string, fallback int) int {
	if n, err := strconv.Atoi(h.param(ctx, key)); err == nil && n >= 0 {
		return n
	}
	return fallback
}

func (h *etlHandler) run(ctx *ActionContext) (ActionResult, error) {
	h.res.platform.Store(ctx.Platform)
	actor, state := h.res.identity(ctx.Context, ctx.Principal)
	switch state {
	case idDisabled:
		return ActionResult{}, intent.Failure{Code: "UNAUTHENTICATED", Category: intent.CategoryAuth, Message: "this session has ended: the account is disabled, or its password was changed"}
	case idMFA:
		if h.op == "me" {
			return h.out(map[string]any{"id": actor.ID, "roles": []string{}, "permissions": map[string][]string{}, "mfa_required": true})
		}
		return ActionResult{}, intent.Failure{Code: "MFA_REQUIRED", Category: intent.CategoryAuth, Message: "enter your authentication code to finish signing in"}
	}
	if op := h.op; op == "health" || op == "metrics" {
		return h.observe(ctx, actor)
	}
	// A queue worker or process step advances batches with no end user.
	if ctx.Principal.ID == "" && internalTransport(ctx) && (h.op == "advance" || h.op == "run") {
		actor = etl.Actor{ID: "system:" + h.res.name, Roles: []string{etl.RoleAdmin}}
	}
	if actor.ID == "" {
		return ActionResult{}, intent.Failure{Code: "UNAUTHENTICATED", Category: intent.CategoryAuth, Message: "etl." + h.op + " needs an authenticated caller"}
	}
	e, c := h.res.engine, ctx.Context
	switch h.op {
	case "me":
		acc, err := e.Access(c, actor)
		if err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"id": actor.ID, "roles": actor.Roles, "permissions": acc.Summary(), "mfa_required": false})
	case "permissions":
		return h.out(etl.Permissions)
	case "require":
		if err := e.Require(c, actor, configString(h.spec.Config, "permission", "")); err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"allowed": true})
	case "note":
		body := h.body(ctx)
		action, detail := configString(h.spec.Config, "action", Stringify(body["action"])), Stringify(body["detail"])
		if path := configString(h.spec.Config, "detail_fact", ""); path != "" {
			if v, ok := resolvePath(orgRoot(ctx), path); ok && v != nil {
				detail = Stringify(v)
			}
		}
		if err := e.Note(c, actor, configString(h.spec.Config, "permission", ""), action, detail); err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"recorded": true})
	case "roles":
		v, err := e.ListRoles(c, actor)
		return h.result(v, err)
	case "role_put":
		var role etl.Role
		raw, _ := json.Marshal(h.body(ctx))
		if err := json.Unmarshal(raw, &role); err != nil {
			return ActionResult{}, invalidInput("the role is not valid: %v", err)
		}
		v, err := e.PutRole(c, actor, role)
		return h.result(v, err)
	case "role_delete":
		id := h.param(ctx, "id")
		if err := e.DeleteRole(c, actor, id); err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"deleted": id})
	case "monitor":
		window := 24 * time.Hour
		if d, err := time.ParseDuration(h.param(ctx, "window")); err == nil && d > 0 {
			window = d
		}
		v, err := e.Monitor(c, actor, window)
		return h.result(v, err)
	case "counters":
		v, err := e.Counters(c, actor)
		return h.result(v, err)
	case "alert_ack":
		body := h.body(ctx)
		minutes := toFloat(body["minutes"])
		if err := e.AckAlert(c, actor, Stringify(body["id"]), Stringify(body["note"]), time.Duration(minutes)*time.Minute); err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"acknowledged": true})
	case "alert_history":
		v, err := e.AlertHistory(c, actor, h.intParam(ctx, "limit", 100))
		return h.result(v, err)
	case "alerts":
		v, err := e.Alerts(c, actor)
		return h.result(v, err)
	case "logs":
		v, err := e.LogsFor(c, actor, etl.LogFilter{Level: h.param(ctx, "level"), BatchID: h.param(ctx, "batch"), TraceID: h.param(ctx, "trace"),
			Contains: h.param(ctx, "q"), Limit: h.intParam(ctx, "limit", 200)})
		return h.result(v, err)
	case "summary":
		v, err := e.Summary(c, actor)
		return h.result(v, err)
	case "sources":
		v, err := e.Sources(c, actor)
		return h.result(v, err)
	case "source_put":
		var s etl.Source
		raw, _ := json.Marshal(h.body(ctx))
		if err := json.Unmarshal(raw, &s); err != nil {
			return ActionResult{}, invalidInput("the source is not valid: %v", err)
		}
		v, err := e.PutSource(c, actor, s)
		return h.result(v, err)
	case "source_pause":
		paused := true
		if v, ok := h.body(ctx)["paused"]; ok {
			paused = Truthy(v)
		}
		v, err := e.SetPaused(c, actor, h.param(ctx, "source"), paused)
		return h.result(v, err)
	case "ingest":
		return h.ingest(ctx, actor)
	case "advance":
		v, err := e.Advance(c, actor, h.param(ctx, "id"))
		return h.result(v, err)
	case "run":
		v, err := e.RunAll(c, actor, h.param(ctx, "id"))
		return h.result(v, err)
	case "replay":
		v, err := e.Replay(c, actor, h.param(ctx, "id"))
		return h.result(v, err)
	case "batches":
		q := etl.Query{SourceID: h.param(ctx, "source"), Limit: h.intParam(ctx, "limit", h.limit), Offset: h.intParam(ctx, "offset", 0)}
		if s := h.param(ctx, "status"); s != "" {
			q.Statuses = strings.Split(s, ",")
		}
		v, err := e.Batches(c, actor, q)
		return h.result(v, err)
	case "batch":
		t, err := e.Trace(c, actor, h.param(ctx, "id"))
		if err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"batch": t.Batch, "resume": t.Resume, "lineage": t.Lineage, "quarantine": t.Quarantine, "audit": t.Audit})
	case "quarantine":
		v, err := e.Quarantined(c, actor, h.intParam(ctx, "limit", h.limit))
		return h.result(v, err)
	case "audit":
		v, err := e.Audit(c, actor, h.intParam(ctx, "limit", h.limit), h.intParam(ctx, "offset", 0))
		return h.result(v, err)
	case "verify_audit":
		bad, total, err := e.VerifyAudit(c, actor)
		if err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"ok": bad == 0, "bad_seq": bad, "total": total})
	}
	return ActionResult{}, fmt.Errorf("etl: unknown operation %q", h.op)
}

// observe answers the probes that must work without a session: health (overall
// status for anyone, detail for monitors) and the metrics scrape (monitors only).
func (h *etlHandler) observe(ctx *ActionContext, actor etl.Actor) (ActionResult, error) {
	e, c := h.res.engine, ctx.Context
	acc, _ := e.Access(c, actor)
	if h.op == "metrics" {
		if !acc.Has(etl.PermMonitor) {
			return ActionResult{}, permissionDenied("metrics need the monitor.read permission")
		}
		text, err := e.MetricsText(c)
		if err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return singleOutput(h.spec, RawResponse{ContentType: "text/plain; version=0.0.4; charset=utf-8", Body: []byte(text)}), nil
	}
	status, checks := e.Health(c)
	out := map[string]any{"status": status}
	if acc.Has(etl.PermMonitor) {
		out["checks"] = checks
	}
	if status == "down" {
		return ActionResult{}, intent.Failure{Code: "UNAVAILABLE", Category: intent.CategoryUnavailable, Message: "the pipeline is down", Meta: out}
	}
	return h.out(out)
}

func (h *etlHandler) result(v any, err error) (ActionResult, error) {
	if err != nil {
		return ActionResult{}, etlFailure(err)
	}
	return h.out(v)
}

func (h *etlHandler) ingest(ctx *ActionContext, actor etl.Actor) (ActionResult, error) {
	body := h.body(ctx)
	sourceID := h.param(ctx, "source")
	key := h.param(ctx, "key")
	if key == "" {
		if v, ok := requestValue(ctx, "header", "Idempotency-Key"); ok {
			key = v
		}
	}
	if object, _ := body["object"].(string); object != "" {
		ictx := ctx.Context
		res, err := h.res.engine.IngestObject(ictx, actor, sourceID, key, object)
		if err != nil {
			return ActionResult{}, etlFailure(err)
		}
		return h.out(map[string]any{"batch": res.Batch, "duplicate": res.Duplicate})
	}
	var rows []etl.Row
	switch data := body["rows"].(type) {
	case []any:
		for _, item := range data {
			m, ok := item.(map[string]any)
			if !ok {
				return ActionResult{}, invalidInput("rows must be objects")
			}
			rows = append(rows, etl.Row(m))
		}
	default:
		text, _ := body["data"].(string)
		if text == "" {
			return ActionResult{}, invalidInput("send rows (a list of objects) or data (the file's text)")
		}
		format, _ := body["format"].(string)
		if format == "" {
			src, err := h.res.engine.Store.GetSource(ctx.Context, sourceID)
			if err != nil {
				return ActionResult{}, etlFailure(err)
			}
			format = src.Format
		}
		parsed, err := etl.ParseRows(format, []byte(text))
		if err != nil {
			return ActionResult{}, etlFailure(err)
		}
		rows = parsed
	}
	ictx := ctx.Context
	for _, name := range []string{"X-Trace-Id", "X-Request-Id"} {
		if v, ok := requestValue(ctx, "header", name); ok && v != "" {
			ictx = etl.WithTrace(ictx, v)
			break
		}
	}
	res, err := h.res.engine.Ingest(ictx, actor, sourceID, key, rows)
	if err != nil {
		return ActionResult{}, etlFailure(err)
	}
	return h.out(map[string]any{"batch": res.Batch, "duplicate": res.Duplicate})
}

// etlFailure maps engine errors to intent failures.
func etlFailure(err error) error {
	message := err.Error()
	switch {
	case errors.Is(err, etl.ErrForbidden):
		return permissionDenied("your roles do not allow that")
	case errors.Is(err, etl.ErrNotFound):
		return intent.Failure{Code: "NOT_FOUND", Category: intent.CategoryNotFound, Message: strings.TrimPrefix(message, "etl: ")}
	case errors.Is(err, etl.ErrInvalid):
		return intent.Failure{Code: "INVALID_INPUT", Category: intent.CategoryInvalidInput, Message: strings.TrimPrefix(message, "etl: invalid: ")}
	case errors.Is(err, etl.ErrKeyReused):
		return intent.Failure{Code: "KEY_REUSED", Category: intent.CategoryConflict, Message: "that idempotency key was used with different content"}
	case errors.Is(err, etl.ErrDuplicate):
		return intent.Failure{Code: "DUPLICATE_CONTENT", Category: intent.CategoryConflict, Message: strings.TrimPrefix(message, "etl: the same content was already sent: ")}
	case errors.Is(err, etl.ErrLeased):
		return intent.Failure{Code: "BUSY", Category: intent.CategoryConflict, Message: "another worker is running this batch; try again in a moment"}
	case errors.Is(err, etl.ErrConflict):
		return intent.Failure{Code: "CONFLICT", Category: intent.CategoryConflict, Message: "someone else changed this; reload and try again"}
	case errors.Is(err, etl.ErrState), errors.Is(err, etl.ErrNotDue):
		return intent.Failure{Code: "INVALID_STATE", Category: intent.CategoryConflict, Message: strings.TrimPrefix(message, "etl: ")}
	}
	return databaseFailure(err)
}
