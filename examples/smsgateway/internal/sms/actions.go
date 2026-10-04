package sms

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/invocation"
	"github.com/oarkflow/ref/platform"

	"github.com/oarkflow/ref/examples/smsgateway/internal/gateway"
)

// The action vocabulary. Each action is one stage of a pipeline; the BCL
// decides which stages an intent has and in what order by what each node
// requires and provides.
//
// Submit pipeline (intent sms.submit):
//
//	sms.validate_user ─▶ sms.check_data ─▶ sms.route ─▶ sms.accept ─▶ sms.enqueue
//
// Delivery pipeline (intent sms.deliver, run by a provider's consumers):
//
//	sms.load_job ─▶ sms.provider_send ─▶ sms.settle
//
// Adding a stage is a Go function registered with platform.RegisterActionDriver
// and a node in the BCL; nothing here has to change.
func registerActions() {
	reg := func(name string, f func(*Hub, platform.NodeSpec) (platform.ActionFunc, error), info platform.ActionInfo) {
		info.Family = "sms"
		if info.ResourceKind == "" {
			info.ResourceKind = "sms.hub"
		}
		platform.RegisterActionDriver(name, platform.ActionFactoryFunc(func(b platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
			h, err := hubFor(b, spec)
			if err != nil {
				return nil, err
			}
			fn, err := f(h, spec)
			if err != nil {
				return nil, fmt.Errorf("node %q (%s): %w", spec.Name, name, err)
			}
			return fn, nil
		}), info)
	}

	reg("sms.validate_user", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			if c.Principal.ID == "" {
				return platform.ActionResult{}, intent.Failure{Code: "UNAUTHENTICATED", Category: intent.CategoryAuth, Message: "an API key is required"}
			}
			u, err := h.ValidateUser(c.Context, c.Principal.ID)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, u), nil
		}, nil
	}, platform.ActionInfo{Summary: "Check the caller is an active account within its rate and daily limits", Kind: "read", Provides: "The user"})

	reg("sms.check_data", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			u, err := need[User](c, "user")
			if err != nil {
				return platform.ActionResult{}, err
			}
			var in SendRequest
			if err := decodeStrict(c.Inputs["input"], &in); err != nil {
				return platform.ActionResult{}, intent.Failure{Code: "INVALID_BODY", Category: intent.CategoryInvalidInput, Message: err.Error()}
			}
			d, err := h.CheckData(c.Context, u, in)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, d), nil
		}, nil
	}, platform.ActionInfo{Summary: "Validate and normalise the recipient, sender, text, type and schedule; check opt-outs", Kind: "read", Provides: "The draft message"})

	reg("sms.route", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			u, err := need[User](c, "user")
			if err != nil {
				return platform.ActionResult{}, err
			}
			d, err := need[*Draft](c, factName(spec, "draft_fact", "draft"))
			if err != nil {
				return platform.ActionResult{}, err
			}
			ex, err := h.PlanRoute(c.Context, u, d)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, ex), nil
		}, nil
	}, platform.ActionInfo{Summary: "Choose the ordered chain of providers by user, tenant, country, capability, quality and cost", Kind: "read", Provides: "The route",
		Config: []platform.ConfigField{{Name: "draft_fact", Type: "fact", Default: "draft"}}})

	reg("sms.accept", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			u, err := need[User](c, "user")
			if err != nil {
				return platform.ActionResult{}, err
			}
			d, err := need[*Draft](c, factName(spec, "draft_fact", "draft"))
			if err != nil {
				return platform.ActionResult{}, err
			}
			ex, err := need[Explanation](c, "route")
			if err != nil {
				return platform.ActionResult{}, err
			}
			res, err := h.Accept(c.Context, u, d, ex)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, res), nil
		}, nil
	}, platform.ActionInfo{Summary: "Price the message, hold the funds and store it in one transaction; idempotent by reference", Kind: "effect", Provides: "The accepted message",
		Config: []platform.ConfigField{{Name: "draft_fact", Type: "fact", Default: "draft"}}})

	reg("sms.enqueue", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			res, err := need[AcceptResult](c, "accepted")
			if err != nil {
				return platform.ActionResult{}, err
			}
			if err := h.Enqueue(c.Context, res); err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, submitResult(res)), nil
		}, nil
	}, platform.ActionInfo{Summary: "Publish the dispatch job to the first provider's queue", Kind: "effect", Provides: "The API result"})

	reg("sms.load_job", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			raw, err := json.Marshal(c.Inputs["input"])
			if err != nil {
				return platform.ActionResult{}, err
			}
			js, err := h.LoadJob(c.Context, raw)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, js), nil
		}, nil
	}, platform.ActionInfo{Summary: "Parse a dispatch job and claim its message; stale and duplicate jobs are skipped", Kind: "effect", Provides: "The claimed job"})

	reg("sms.provider_send", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			js, err := need[*JobState](c, "job")
			if err != nil {
				return platform.ActionResult{}, err
			}
			out, err := h.ProviderSend(c.Context, js)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, out), nil
		}, nil
	}, platform.ActionInfo{Summary: "Send through the provider's gateway with a circuit breaker, rate limit and timeout", Kind: "effect", Provides: "The send outcome"})

	reg("sms.settle", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			js, err := need[*JobState](c, "job")
			if err != nil {
				return platform.ActionResult{}, err
			}
			out, err := need[*SendOutcome](c, "sent")
			if err != nil {
				return platform.ActionResult{}, err
			}
			res, err := h.Settle(c.Context, js, out)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, res), nil
		}, nil
	}, platform.ActionInfo{Summary: "Capture the charge, retry per the provider's policy, fail over, or fail and release funds", Kind: "effect", Provides: "The outcome"})

	reg("sms.dlr_apply", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			raw, err := json.Marshal(c.Inputs["input"])
			if err != nil {
				return platform.ActionResult{}, err
			}
			out, err := h.ApplyDLR(c.Context, raw)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, out), nil
		}, nil
	}, platform.ActionInfo{Summary: "Apply a delivery receipt to its message", Kind: "effect", Provides: "The outcome"})

	reg("sms.dlr_ingest", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			name, _ := c.Inputs["provider"].(string)
			p, ok := h.provider(name)
			if !ok {
				return platform.ActionResult{}, intent.Failure{Code: "UNKNOWN_PROVIDER", Category: intent.CategoryNotFound, Message: "unknown provider"}
			}
			parser, ok := p.GW.(gateway.DLRParser)
			if !ok {
				return platform.ActionResult{}, intent.Failure{Code: "NO_WEBHOOK", Category: intent.CategoryNotFound, Message: "provider " + name + " does not deliver receipts by webhook"}
			}
			headers := httpHeaders(c)
			if secret := p.Cfg.WebhookSecret; secret != "" {
				got := ""
				for k, v := range headers {
					if strings.EqualFold(k, "X-Webhook-Secret") && len(v) > 0 {
						got = v[0]
					}
				}
				if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
					return platform.ActionResult{}, intent.Failure{Code: "UNAUTHENTICATED", Category: intent.CategoryAuth, Message: "invalid webhook secret"}
				}
			}
			receipts, err := parser.ParseDLR(headers, c.Invocation.Input.RawBytes())
			if err != nil {
				return platform.ActionResult{}, intent.Failure{Code: "INVALID_RECEIPT", Category: intent.CategoryInvalidInput, Message: err.Error()}
			}
			for _, r := range receipts {
				r.Provider = name
				if err := h.EnqueueDLR(r); err != nil {
					return platform.ActionResult{}, intent.Failure{Code: "QUEUE_UNAVAILABLE", Category: intent.CategoryUnavailable, Message: "cannot queue the receipt"}
				}
			}
			return one(spec, map[string]any{"accepted": len(receipts)}), nil
		}, nil
	}, platform.ActionInfo{Summary: "Parse a provider's receipt webhook and queue each receipt", Kind: "effect", Provides: "The number of receipts queued"})

	reg("sms.query", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		op, _ := spec.Config["op"].(string)
		q, ok := queryOps[op]
		if !ok {
			return nil, fmt.Errorf("unknown op %q; use one of %s", op, strings.Join(sortedKeys(queryOps), ", "))
		}
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			if c.Principal.ID == "" {
				return platform.ActionResult{}, intent.Failure{Code: "UNAUTHENTICATED", Category: intent.CategoryAuth, Message: "an API key is required"}
			}
			v, err := q(h, c)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, v), nil
		}, nil
	}, platform.ActionInfo{
		Summary: "Read the caller's own data: message, messages, balance, ledger, route.explain",
		Kind:    "read", Provides: "The result",
		Config: []platform.ConfigField{{Name: "op", Type: "string", Required: true, Summary: strings.Join(sortedKeys(queryOps), ", ")}},
	})

	reg("sms.admin", func(h *Hub, spec platform.NodeSpec) (platform.ActionFunc, error) {
		op, _ := spec.Config["op"].(string)
		a, ok := adminOps[op]
		if !ok {
			return nil, fmt.Errorf("unknown op %q; use one of %s", op, strings.Join(sortedKeys(adminOps), ", "))
		}
		return func(c *platform.ActionContext) (platform.ActionResult, error) {
			v, err := a(h, c)
			if err != nil {
				return platform.ActionResult{}, failure(err)
			}
			return one(spec, v), nil
		}, nil
	}, platform.ActionInfo{
		Summary: "Operator actions: users, keys, providers, assignments, rates, top-ups, opt-outs, stats",
		Kind:    "effect", Provides: "The result",
		Config: []platform.ConfigField{{Name: "op", Type: "string", Required: true, Summary: strings.Join(sortedKeys(adminOps), ", ")}},
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func hubFor(b platform.BuildContext, spec platform.NodeSpec) (*Hub, error) {
	if spec.Resource == "" {
		return nil, fmt.Errorf("node %q needs `resource` naming an sms.hub", spec.Name)
	}
	r, ok := b.Resource(spec.Resource)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not declared", spec.Name, spec.Resource)
	}
	h, ok := r.(*Hub)
	if !ok {
		return nil, fmt.Errorf("node %q: resource %q is not an sms.hub", spec.Name, spec.Resource)
	}
	return h, nil
}

// one publishes v as the node's single output fact.
func one(spec platform.NodeSpec, v any) platform.ActionResult {
	if len(spec.Provides) == 0 {
		return platform.ActionResult{}
	}
	return platform.ActionResult{Outputs: map[string]any{spec.Provides[0]: v}}
}

// factName reads the name of the fact a stage consumes from the node's config,
// so a stage can be inserted into a pipeline without renaming its neighbours.
func factName(spec platform.NodeSpec, key, def string) string {
	if s, _ := spec.Config[key].(string); s != "" {
		return s
	}
	return def
}

// need reads a typed fact a previous stage published.
func need[T any](c *platform.ActionContext, name string) (T, error) {
	v, ok := c.Inputs[name].(T)
	if !ok {
		var zero T
		return zero, intent.Failure{Code: "INTERNAL_ERROR", Category: intent.CategoryInternal,
			Message: fmt.Sprintf("stage input %q is missing or has the wrong type (%T)", name, c.Inputs[name])}
	}
	return v, nil
}

// failure maps a pipeline fault to a REF failure, and so to an HTTP status.
// Anything that is not a Fault is an infrastructure error and is returned as
// is: a queue consumer retries it.
func failure(err error) error {
	var f *Fault
	if !errors.As(err, &f) {
		return err
	}
	cat := intent.CategoryInternal
	switch f.Kind {
	case FaultInvalid:
		cat = intent.CategoryInvalidInput
	case FaultAuth:
		cat = intent.CategoryAuth
	case FaultForbidden:
		cat = intent.CategoryPermission
	case FaultFunds, FaultConflict:
		cat = intent.CategoryConflict
	case FaultLimit:
		cat = intent.CategoryRateLimit
	case FaultNotFound:
		cat = intent.CategoryNotFound
	case FaultUnavailable:
		cat = intent.CategoryUnavailable
	}
	return intent.Failure{Code: f.Code, Category: cat, Message: f.Msg, Meta: f.Meta, Retryable: f.Kind == FaultUnavailable}
}

func decodeStrict(v any, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid request body: %v", strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func httpHeaders(c *platform.ActionContext) map[string][]string {
	switch m := c.Invocation.Metadata.(type) {
	case invocation.HTTPMeta:
		return m.Headers
	case *invocation.HTTPMeta:
		return m.Headers
	}
	return nil
}

func submitResult(res AcceptResult) map[string]any {
	m := res.Message
	route := make([]string, 0, len(m.Plan))
	for _, p := range m.Plan {
		route = append(route, p.Provider)
	}
	out := map[string]any{
		"id": m.ID, "state": m.State, "to": m.To, "country": m.Country, "from": m.From,
		"type": m.Type, "segments": m.Segments, "encoding": m.Encoding,
		"price": ToUnits(m.PriceMicros), "currency": m.Currency, "route": route, "duplicate": res.Duplicate,
	}
	if m.Provider != "" {
		out["provider"] = m.Provider
	}
	if !m.NextRunAt.IsZero() && m.NextRunAt.After(m.CreatedAt.Add(time.Second)) {
		out["scheduled_at"] = m.NextRunAt
	}
	if res.Duplicate {
		out["state"] = m.State
	}
	return out
}

// ---------------------------------------------------------------------------
// Self-service queries
// ---------------------------------------------------------------------------

type queryOp func(*Hub, *platform.ActionContext) (any, error)

var queryOps = map[string]queryOp{
	"message": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		m, err := h.Store.Get(c.Context, id)
		if errors.Is(err, ErrNotFound) || (err == nil && m.UserID != c.Principal.ID) {
			// Another account's message is indistinguishable from none.
			return nil, fault(FaultNotFound, "NOT_FOUND", "no such message")
		}
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return messageView(h, c.Context, m, true), nil
	},
	"messages": func(h *Hub, c *platform.ActionContext) (any, error) {
		state, _ := c.Inputs["state"].(string)
		limit := intOf(c.Inputs["limit"], 50)
		ms, err := h.Store.ListMessages(c.Context, c.Principal.ID, MessageState(state), limit)
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		out := make([]any, 0, len(ms))
		for _, m := range ms {
			out = append(out, messageView(h, c.Context, m, false))
		}
		return map[string]any{"messages": out, "count": len(out)}, nil
	},
	"balance": func(h *Hub, c *platform.ActionContext) (any, error) {
		return balanceView(h, c.Context, c.Principal.ID)
	},
	"ledger": func(h *Hub, c *platform.ActionContext) (any, error) {
		entries, err := h.Store.Ledger(c.Context, c.Principal.ID, intOf(c.Inputs["limit"], 100))
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"entries": entries}, nil
	},
	"route.explain": func(h *Hub, c *platform.ActionContext) (any, error) {
		return explain(h, c, c.Principal.ID)
	},
}

func intOf(v any, def int) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return def
}

func balanceView(h *Hub, ctx context.Context, user string) (any, error) {
	b, err := h.Store.Balance(ctx, user)
	if errors.Is(err, ErrNotFound) {
		return nil, fault(FaultNotFound, "NOT_FOUND", "no account")
	}
	if err != nil {
		return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
	}
	return map[string]any{
		"user": user, "currency": b.Currency, "balance": ToUnits(b.Balance),
		"held": ToUnits(b.Held), "available": ToUnits(b.Available),
	}, nil
}

func messageView(h *Hub, ctx context.Context, m *Message, detail bool) map[string]any {
	out := map[string]any{
		"id": m.ID, "state": m.State, "to": m.To, "country": m.Country, "from": m.From, "type": m.Type,
		"segments": m.Segments, "price": ToUnits(m.PriceMicros), "currency": m.Currency,
		"created_at": m.CreatedAt,
	}
	if m.Provider != "" {
		out["provider"] = m.Provider
	}
	if m.Error != "" || m.ErrorCode != "" {
		out["error"] = map[string]any{"code": m.ErrorCode, "message": m.Error}
	}
	if !m.SubmittedAt.IsZero() {
		out["submitted_at"] = m.SubmittedAt
	}
	if !m.DeliveredAt.IsZero() {
		out["delivered_at"] = m.DeliveredAt
	}
	if detail {
		out["attempts"] = m.TotalAttempts
		if st, err := h.Store.HoldState(ctx, m.ID); err == nil {
			out["payment"] = st
		}
		if atts, err := h.Store.Attempts(ctx, m.ID); err == nil {
			out["attempt_log"] = atts
		}
	}
	return out
}

func explain(h *Hub, c *platform.ActionContext, userID string) (any, error) {
	var in SendRequest
	if err := decodeStrict(c.Inputs["input"], &in); err != nil {
		return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
	}
	u, ok := h.Dir.User(userID)
	if !ok {
		return nil, fault(FaultNotFound, "UNKNOWN_USER", "no such user")
	}
	d, err := h.CheckData(c.Context, u, in)
	if err != nil {
		return nil, err
	}
	ex, err := h.PlanRoute(c.Context, u, d)
	view := map[string]any{
		"to": d.To, "country": d.Country, "type": d.Type, "segments": d.Segments, "encoding": d.Encoding,
		"objective": ex.Objective, "route": ex.Plan, "candidates": ex.Candidates,
	}
	if price, perr := h.Price(u, d); perr == nil {
		view["price"] = ToUnits(price)
	}
	if err != nil {
		var f *Fault
		if errors.As(err, &f) {
			view["error"] = map[string]any{"code": f.Code, "message": f.Msg}
			return view, nil
		}
		return nil, err
	}
	return view, nil
}

// ---------------------------------------------------------------------------
// Operator actions
// ---------------------------------------------------------------------------

type adminOp func(*Hub, *platform.ActionContext) (any, error)

var adminOps = map[string]adminOp{
	"user.put": func(h *Hub, c *platform.ActionContext) (any, error) {
		var body struct {
			User
			APIKey string `json:"api_key"`
		}
		if err := decodeStrict(c.Inputs["input"], &body); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		if id, _ := c.Inputs["id"].(string); id != "" {
			body.ID = id
		}
		u := body.User
		if existing, err := h.Store.GetUser(c.Context, u.ID); err == nil && u.APIKeyHash == "" {
			u.APIKeyHash = existing.APIKeyHash // an update must not lock the user out
		}
		if body.APIKey != "" {
			u.APIKeyHash = HashKey(body.APIKey)
		}
		saved, err := h.PutUser(c.Context, u)
		if err != nil {
			return nil, fault(FaultInvalid, "INVALID_USER", "%v", err)
		}
		saved.APIKeyHash = ""
		return saved, nil
	},
	"user.get": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		u, ok := h.Dir.User(id)
		if !ok {
			return nil, fault(FaultNotFound, "NOT_FOUND", "no such user")
		}
		u.APIKeyHash = ""
		bal, _ := balanceView(h, c.Context, id)
		return map[string]any{"user": u, "balance": bal}, nil
	},
	"user.list": func(h *Hub, c *platform.ActionContext) (any, error) {
		users := h.Dir.Users()
		for i := range users {
			users[i].APIKeyHash = ""
		}
		return map[string]any{"users": users}, nil
	},
	"user.delete": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		if _, ok := h.Dir.User(id); !ok {
			return nil, fault(FaultNotFound, "NOT_FOUND", "no such user")
		}
		if err := h.DeleteUser(c.Context, id); err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"deleted": id}, nil
	},
	"user.key": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		key, err := h.IssueKey(c.Context, id)
		if errors.Is(err, ErrNotFound) {
			return nil, fault(FaultNotFound, "NOT_FOUND", "no such user")
		}
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"user": id, "api_key": key, "note": "store this key now; it cannot be shown again"}, nil
	},
	"topup": func(h *Hub, c *platform.ActionContext) (any, error) {
		var body struct {
			Amount    float64 `json:"amount"`
			Reference string  `json:"reference"`
		}
		if err := decodeStrict(c.Inputs["input"], &body); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		id, _ := c.Inputs["id"].(string)
		if body.Amount <= 0 || body.Reference == "" {
			return nil, fault(FaultInvalid, "INVALID_BODY", "amount must be positive and reference is required (it makes the top-up safe to replay)")
		}
		bal, applied, err := h.TopUp(c.Context, id, body.Amount, body.Reference)
		if errors.Is(err, ErrUnknownUser) {
			return nil, fault(FaultNotFound, "NOT_FOUND", "no such user")
		}
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"user": id, "applied": applied, "balance": ToUnits(bal.Balance), "available": ToUnits(bal.Available), "currency": bal.Currency}, nil
	},
	"balance": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		return balanceView(h, c.Context, id)
	},
	"provider.put": func(h *Hub, c *platform.ActionContext) (any, error) {
		var body struct {
			Kind    string         `json:"kind"`
			Owner   string         `json:"owner"`
			Enabled *bool          `json:"enabled"`
			Config  map[string]any `json:"config"`
		}
		if err := decodeStrict(c.Inputs["input"], &body); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		name, _ := c.Inputs["id"].(string)
		rec := ProviderRecord{Name: name, Kind: body.Kind, Owner: body.Owner, Enabled: body.Enabled == nil || *body.Enabled, Config: body.Config}
		if rec.Kind == "" {
			return nil, fault(FaultInvalid, "INVALID_BODY", "kind is required (one of %s)", strings.Join(gateway.Kinds(), ", "))
		}
		if err := h.PutProvider(c.Context, rec); err != nil {
			return nil, fault(FaultInvalid, "INVALID_PROVIDER", "%v", err)
		}
		return map[string]any{"provider": name, "kind": rec.Kind, "enabled": rec.Enabled}, nil
	},
	"provider.delete": func(h *Hub, c *platform.ActionContext) (any, error) {
		name, _ := c.Inputs["id"].(string)
		if err := h.DeleteProvider(c.Context, name); err != nil {
			return nil, fault(FaultInvalid, "INVALID_PROVIDER", "%v", err)
		}
		return map[string]any{"deleted": name}, nil
	},
	"provider.list": func(h *Hub, c *platform.ActionContext) (any, error) {
		return map[string]any{"providers": h.Health(c.Context)}, nil
	},
	"provider.state": func(h *Hub, c *platform.ActionContext) (any, error) {
		var body struct {
			State string `json:"state"`
		}
		if err := decodeStrict(c.Inputs["input"], &body); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		name, _ := c.Inputs["id"].(string)
		if _, ok := h.provider(name); !ok {
			return nil, fault(FaultNotFound, "NOT_FOUND", "no such provider")
		}
		if err := h.SetProviderState(name, body.State); err != nil {
			return nil, fault(FaultInvalid, "INVALID_STATE", "%v", err)
		}
		return map[string]any{"provider": name, "state": body.State}, nil
	},
	"assignment.put": func(h *Hub, c *platform.ActionContext) (any, error) {
		var a Assignment
		if err := decodeStrict(c.Inputs["input"], &a); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		if id, _ := c.Inputs["id"].(string); id != "" {
			a.ID = id
		}
		saved, err := h.PutAssignment(c.Context, a)
		if err != nil {
			return nil, fault(FaultInvalid, "INVALID_ASSIGNMENT", "%v", err)
		}
		return saved, nil
	},
	"assignment.delete": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		if err := h.DeleteAssignment(c.Context, id); err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"deleted": id}, nil
	},
	"assignment.list": func(h *Hub, c *platform.ActionContext) (any, error) {
		return map[string]any{"assignments": h.Dir.Assignments()}, nil
	},
	"rate.put": func(h *Hub, c *platform.ActionContext) (any, error) {
		var body struct {
			ID          string  `json:"id"`
			UserID      string  `json:"user_id"`
			Country     string  `json:"country"`
			MessageType string  `json:"message_type"`
			Price       float64 `json:"price"`
		}
		if err := decodeStrict(c.Inputs["input"], &body); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		if id, _ := c.Inputs["id"].(string); id != "" {
			body.ID = id
		}
		saved, err := h.PutRate(c.Context, Rate{ID: body.ID, UserID: body.UserID, Country: body.Country,
			MessageType: body.MessageType, SellPerSegmentMicros: FromUnits(body.Price), Currency: h.Cfg.Currency})
		if err != nil {
			return nil, fault(FaultInvalid, "INVALID_RATE", "%v", err)
		}
		return saved, nil
	},
	"rate.delete": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		if err := h.DeleteRate(c.Context, id); err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"deleted": id}, nil
	},
	"rate.list": func(h *Hub, c *platform.ActionContext) (any, error) {
		return map[string]any{"rates": h.Dir.Rates()}, nil
	},
	"optout.add": func(h *Hub, c *platform.ActionContext) (any, error) {
		var body struct {
			Number string `json:"number"`
			User   string `json:"user"`
			Reason string `json:"reason"`
		}
		if err := decodeStrict(c.Inputs["input"], &body); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		digits, _, err := NormalizeNumber(body.Number, h.Cfg.DefaultCountry)
		if err != nil {
			return nil, fault(FaultInvalid, "INVALID_NUMBER", "%v", err)
		}
		scope := body.User
		if scope == "" {
			scope = GlobalScope
		}
		if err := h.Store.AddOptOut(c.Context, scope, digits, body.Reason); err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"number": digits, "scope": scope}, nil
	},
	"optout.remove": func(h *Hub, c *platform.ActionContext) (any, error) {
		var body struct {
			Number string `json:"number"`
			User   string `json:"user"`
		}
		if err := decodeStrict(c.Inputs["input"], &body); err != nil {
			return nil, fault(FaultInvalid, "INVALID_BODY", "%v", err)
		}
		digits, _, err := NormalizeNumber(body.Number, h.Cfg.DefaultCountry)
		if err != nil {
			return nil, fault(FaultInvalid, "INVALID_NUMBER", "%v", err)
		}
		scope := body.User
		if scope == "" {
			scope = GlobalScope
		}
		if err := h.Store.RemoveOptOut(c.Context, scope, digits); err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"number": digits, "scope": scope, "removed": true}, nil
	},
	"message.get": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		m, err := h.Store.Get(c.Context, id)
		if errors.Is(err, ErrNotFound) {
			return nil, fault(FaultNotFound, "NOT_FOUND", "no such message")
		}
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		v := messageView(h, c.Context, m, true)
		v["user"] = m.UserID
		v["plan"] = m.Plan
		return v, nil
	},
	"route.explain": func(h *Hub, c *platform.ActionContext) (any, error) {
		id, _ := c.Inputs["id"].(string)
		return explain(h, c, id)
	},
	"stats": func(h *Hub, c *platform.ActionContext) (any, error) {
		counts, err := h.Store.Counts(c.Context)
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		cn := &h.Counters
		return map[string]any{
			"counters": map[string]int64{
				"accepted": cn.Accepted.Load(), "duplicates": cn.Duplicates.Load(), "submitted": cn.Submitted.Load(),
				"delivered": cn.Delivered.Load(), "failed": cn.Failed.Load(), "retries": cn.Retries.Load(),
				"failovers": cn.Failovers.Load(), "stale_jobs": cn.StaleJobs.Load(), "recovered": cn.Recovered.Load(),
			},
			"messages": counts, "providers": h.Health(c.Context), "circuits": h.Router.Circuits(),
		}, nil
	},
	"recover": func(h *Hub, c *platform.ActionContext) (any, error) {
		n, err := h.Recover(c.Context)
		if err != nil {
			return nil, fault(FaultUnavailable, "STORE_UNAVAILABLE", "%v", err)
		}
		return map[string]any{"republished": n}, nil
	},
}
