package platform

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oarkflow/ref/ai"
	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/platform/spi"
)

// Integration actions that back the grpc, webhook and rag node types.
//
// Every outbound call still goes through a service.http resource, so the
// resource's allowlist, private-network refusal, timeout, response cap and
// retry policy apply exactly as they do to service.http. These actions add the
// protocol on top, never a way around those limits.

func registerIntegrationActions(r *Registry) {
	mustAction(r, "service.grpc_json", ActionFactoryFunc(buildGRPCJSON), ActionInfo{
		Family:       "service",
		Summary:      "Unary gRPC call over JSON (Connect protocol; also works with grpc-gateway style JSON endpoints)",
		ResourceKind: "service",
		Provides:     "The decoded response message",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "service", Type: "string", Required: true, Summary: "Fully qualified service, e.g. billing.v1.ClaimService"},
			{Name: "method", Type: "string", Required: true, Summary: "RPC name, e.g. SubmitClaim"},
			{Name: "url", Type: "template", Summary: "Override the path (default /<service>/<method>)"},
			{Name: "request_fact", Type: "fact", Summary: "Fact serialised as the request message (default: empty message)"},
			{Name: "headers", Type: "map", Summary: "Metadata sent as headers, templated"},
		},
	})
	mustAction(r, "service.webhook_send", ActionFactoryFunc(buildWebhookSend), ActionInfo{
		Family:       "service",
		Summary:      "Deliver a Standard Webhooks signed event (webhook-id, webhook-timestamp, webhook-signature)",
		ResourceKind: "service",
		Provides:     "An object with status, webhook_id and timestamp",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "url", Type: "template", Required: true},
			{Name: "event_type", Type: "template", Summary: "Wrapped as {type, timestamp, data} when set; otherwise the payload is sent as-is"},
			{Name: "payload_fact", Type: "fact", Required: true, Summary: "Fact holding the event data"},
			{Name: "secret", Type: "string", Required: true, Summary: "Signing secret; whsec_<base64> or raw. Keep it in the environment: secret env.required(\"X\")"},
			{Name: "id_fact", Type: "fact", Summary: "Stable message id (the receiver's idempotency key); random when absent"},
			{Name: "headers", Type: "map"},
		},
	})
	mustAction(r, "service.rag_retrieve", ActionFactoryFunc(buildRAGRetrieve), ActionInfo{
		Family:       "intelligence",
		Summary:      "Retrieve grounding documents from a search index and assemble a citation-ready context",
		ResourceKind: "search",
		Provides:     "An object with context, documents and count",
		Kind:         "read",
		Config: []ConfigField{
			{Name: "collection", Type: "string", Required: true},
			{Name: "text", Type: "template", Summary: "Query text (or text_fact)"},
			{Name: "text_fact", Type: "fact"},
			{Name: "filters", Type: "map"},
			{Name: "limit", Type: "int", Default: "5"},
			{Name: "content_field", Type: "string", Default: "text", Summary: "Payload field holding the document text"},
			{Name: "title_field", Type: "string", Default: "title"},
			{Name: "max_characters", Type: "int", Default: "8000", Summary: "Total text budget across documents"},
			{Name: "instruction", Type: "string", Summary: "Instruction prepended to the context (default: answer only from the documents and cite them)"},
		},
	})
}

// ---------------------------------------------------------------------------
// gRPC over JSON
// ---------------------------------------------------------------------------

var grpcNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

func buildGRPCJSON(build BuildContext, spec NodeSpec) (Action, error) {
	service := configString(spec.Config, "service", "")
	method := configString(spec.Config, "method", "")
	if !grpcNamePattern.MatchString(service) || !grpcNamePattern.MatchString(method) || strings.Contains(method, ".") {
		return nil, fmt.Errorf("node %q: service.grpc_json needs a qualified service (pkg.Service) and a method name", spec.Name)
	}
	config := make(map[string]any, len(spec.Config)+1)
	for k, v := range spec.Config {
		config[k] = v
	}
	if _, ok := config["url"]; !ok {
		config["url"] = "/" + service + "/" + method
	}
	callSpec := spec
	callSpec.Config = config
	call, err := compileHTTPCall(build, callSpec, "", http.MethodPost)
	if err != nil {
		return nil, err
	}
	requestFact := configString(spec.Config, "request_fact", "")
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		request, err := call.request(ctx)
		if err != nil {
			return ActionResult{}, err
		}
		message := any(map[string]any{})
		if requestFact != "" {
			value, found := resolvePath(ctx.Inputs, requestFact)
			if !found {
				return ActionResult{}, invalidInput("no request message at %q", requestFact)
			}
			message = value
		}
		if request.Body, err = json.Marshal(message); err != nil {
			return ActionResult{}, invalidInput("the request message cannot be serialised: %v", err)
		}
		request.Method = http.MethodPost
		if request.Headers == nil {
			request.Headers = map[string]string{}
		}
		request.Headers["Content-Type"] = "application/json"
		request.Headers["Connect-Protocol-Version"] = "1"
		if deadline, ok := ctx.Context.Deadline(); ok {
			if remaining := time.Until(deadline).Milliseconds(); remaining > 0 {
				request.Headers["Connect-Timeout-Ms"] = strconv.FormatInt(remaining, 10)
			}
		}
		response, err := call.service.Do(ctx.Context, request)
		if err != nil || response.Status >= 300 {
			return ActionResult{}, grpcFailure(response, err)
		}
		if response.JSON == nil && len(response.Body) > 0 {
			return ActionResult{}, unavailable("the gRPC response was not JSON")
		}
		return singleOutput(spec, response.JSON), nil
	}), nil
}

// grpcFailure maps a Connect/gRPC error ({"code": "...", "message": "..."})
// onto a platform failure category, falling back to the HTTP status.
func grpcFailure(response HTTPResponse, err error) error {
	body, _ := response.JSON.(map[string]any)
	code := strings.ToLower(Stringify(body["code"]))
	message := Stringify(body["message"])
	if message == "" {
		message = "the gRPC call failed"
	}
	category, known := map[string]intent.Category{
		"invalid_argument":    intent.CategoryInvalidInput,
		"out_of_range":        intent.CategoryInvalidInput,
		"failed_precondition": intent.CategoryInvalidInput,
		"not_found":           intent.CategoryNotFound,
		"already_exists":      intent.CategoryConflict,
		"aborted":             intent.CategoryConflict,
		"permission_denied":   intent.CategoryPermission,
		"resource_exhausted":  intent.CategoryRateLimit,
		"deadline_exceeded":   intent.CategoryTimeout,
		"unauthenticated":     intent.CategoryUnavailable, // our credentials, not the caller's
		"unavailable":         intent.CategoryUnavailable,
	}[code]
	if !known {
		if response.Status > 0 {
			return upstreamFailure(err, response.Status)
		}
		return unavailable("the gRPC call failed: %v", err)
	}
	return intent.Failure{Code: "GRPC_" + strings.ToUpper(code), Category: category, Message: message}
}

// ---------------------------------------------------------------------------
// Standard Webhooks
// ---------------------------------------------------------------------------

func buildWebhookSend(build BuildContext, spec NodeSpec) (Action, error) {
	call, err := compileHTTPCall(build, spec, "", http.MethodPost)
	if err != nil {
		return nil, err
	}
	payloadFact, err := requiredString(spec.Config, "payload_fact")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	secret, err := decodeWebhookSecret(configString(spec.Config, "secret", ""))
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	eventType, err := configTemplate(spec.Config, "event_type", "")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	idFact := configString(spec.Config, "id_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		request, err := call.request(ctx)
		if err != nil {
			return ActionResult{}, err
		}
		data, found := resolvePath(ctx.Inputs, payloadFact)
		if !found {
			return ActionResult{}, invalidInput("no webhook payload at %q", payloadFact)
		}
		now := ctx.Now
		if now.IsZero() {
			now = time.Now()
		}
		payload := data
		if eventType != nil {
			kind, err := eventType.Render(actionEnv(ctx))
			if err != nil {
				return ActionResult{}, err
			}
			payload = map[string]any{"type": kind, "timestamp": now.UTC().Format(time.RFC3339), "data": data}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return ActionResult{}, invalidInput("the webhook payload cannot be serialised: %v", err)
		}
		id := ""
		if idFact != "" {
			if value, ok := resolvePath(ctx.Inputs, idFact); ok {
				id = Stringify(value)
			}
		}
		if id == "" {
			id = "msg_" + randomHex(16)
		}
		timestamp := strconv.FormatInt(now.Unix(), 10)
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(id + "." + timestamp + "."))
		mac.Write(body)
		if request.Headers == nil {
			request.Headers = map[string]string{}
		}
		request.Headers["Content-Type"] = "application/json"
		request.Headers["webhook-id"] = id
		request.Headers["webhook-timestamp"] = timestamp
		request.Headers["webhook-signature"] = "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		request.Method, request.Body = http.MethodPost, body

		response, err := call.service.Do(ctx.Context, request)
		if err != nil || !call.accepts(response.Status) {
			if err == nil {
				err = fmt.Errorf("receiver returned %d", response.Status)
			}
			return ActionResult{}, upstreamFailure(err, response.Status)
		}
		return acknowledgement(spec, map[string]any{"status": response.Status, "webhook_id": id, "timestamp": timestamp}), nil
	}), nil
}

func decodeWebhookSecret(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, fmt.Errorf("service.webhook_send needs config.secret")
	}
	if encoded, ok := strings.CutPrefix(secret, "whsec_"); ok {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("webhook secret: %w", err)
		}
		return key, nil
	}
	if len(secret) < 24 {
		return nil, fmt.Errorf("webhook secret must be at least 24 characters")
	}
	return []byte(secret), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Retrieval-augmented generation
// ---------------------------------------------------------------------------

func buildRAGRetrieve(build BuildContext, spec NodeSpec) (Action, error) {
	index, err := requireResource[spi.SearchIndex](build, spec, "a search resource")
	if err != nil {
		return nil, err
	}
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	collection, err := requiredString(spec.Config, "collection")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	text, err := configTemplate(spec.Config, "text", "")
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	textFact := configString(spec.Config, "text_fact", "")
	if text == nil && textFact == "" {
		return nil, fmt.Errorf("node %q: service.rag_retrieve needs config.text or config.text_fact", spec.Name)
	}
	limit, err := configInt(spec.Config, "limit", 5)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	maxChars, err := configInt(spec.Config, "max_characters", 8000)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	contentField := configString(spec.Config, "content_field", "text")
	titleField := configString(spec.Config, "title_field", "title")
	instruction := configString(spec.Config, "instruction", "Answer the question using the documents below.")
	filters := configMap(spec.Config, "filters")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		query := spi.SearchQuery{Limit: limit, Filters: filters, Tenant: ctx.TenantID}
		if textFact != "" {
			if value, ok := resolvePath(ctx.Inputs, textFact); ok {
				query.Text = Stringify(value)
			}
		}
		if query.Text == "" && text != nil {
			if query.Text, err = text.Render(actionEnv(ctx)); err != nil {
				return ActionResult{}, err
			}
		}
		if strings.TrimSpace(query.Text) == "" {
			return ActionResult{}, invalidInput("a retrieval query is required")
		}
		result, err := index.Search(ctx.Context, collection, query)
		if err != nil {
			return ActionResult{}, unavailable("retrieval failed: %v", err)
		}
		documents := make([]ai.Document, 0, len(result.Hits))
		hits := make([]any, 0, len(result.Hits))
		for _, hit := range result.Hits {
			body := Stringify(hit.Payload[contentField])
			if body == "" {
				continue
			}
			documents = append(documents, ai.Document{ID: hit.ID, Title: Stringify(hit.Payload[titleField]), Text: body})
			hits = append(hits, map[string]any{"id": hit.ID, "score": hit.Score, "title": hit.Payload[titleField]})
		}
		out := map[string]any{"documents": hits, "count": len(documents), "context": ""}
		if len(documents) == 0 {
			return singleOutput(spec, out), nil
		}
		prompt, err := ai.BuildGroundedPrompt(instruction, documents, ai.GroundingPolicy{MaxDocuments: limit, MaxCharacters: maxChars, RequireCitations: true})
		if err != nil {
			return ActionResult{}, invalidInput("%s", err.Error())
		}
		out["context"] = prompt.SystemText()
		out["instruction"] = prompt.Instruction
		return singleOutput(spec, out), nil
	}), nil
}
