package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/oarkflow/ref/examples/smsgateway/internal/cfgdec"
)

func init() { Register("http", openHTTP) }

type httpConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Format  string            `json:"format"` // json (default) or form
	Headers map[string]string `json:"headers"`
	Auth    struct {
		Type     string `json:"type"` // bearer, basic or header
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
		Header   string `json:"header"`
	} `json:"auth"`
	// Body is the request body. Every string value may use {{to}}, {{from}},
	// {{text}}, {{id}}, {{segments}}, {{country}} and {{dlr}}; values are
	// substituted after the structure is built, so message text can never alter
	// the shape of the request.
	Body           map[string]any  `json:"body"`
	SuccessStatus  []int           `json:"success_status"`
	IDPath         string          `json:"id_path"`    // dotted path of the upstream id in the response
	ErrorPath      string          `json:"error_path"` // dotted path of an upstream error code
	Timeout        cfgdec.Duration `json:"timeout"`
	MaxResponse    int64           `json:"max_response_bytes"`
	AllowPrivate   cfgdec.Bool     `json:"allow_private_networks"`
	PermanentCodes []string        `json:"permanent_codes"` // upstream error codes that fail the message for good
	ProviderCodes  []string        `json:"provider_codes"`  // upstream error codes that fail this provider only
	DLR            struct {
		IDField   string   `json:"id_field"`
		RefField  string   `json:"ref_field"`
		StatField string   `json:"status_field"`
		Delivered []string `json:"delivered"`
		Failed    []string `json:"failed"`
		ListPath  string   `json:"list_path"`
	} `json:"dlr"`
}

// HTTPGateway delivers through a vendor's JSON or form HTTP API. It is the
// plugin to copy for a vendor with a conventional REST interface: the request
// shape, the success test, the id extraction and the error mapping are all
// configuration.
type HTTPGateway struct {
	name   string
	cfg    httpConfig
	client *http.Client
}

func openHTTP(_ context.Context, name string, config map[string]any) (Gateway, error) {
	var cfg httpConfig
	if err := cfgdec.Decode(config, &cfg); err != nil {
		return nil, fmt.Errorf("http gateway %q: %w", name, err)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("http gateway %q: url must be an absolute http(s) URL", name)
	}
	if cfg.Method == "" {
		cfg.Method = http.MethodPost
	}
	if cfg.Format == "" {
		cfg.Format = "json"
	}
	if cfg.Format != "json" && cfg.Format != "form" {
		return nil, fmt.Errorf("http gateway %q: format must be json or form", name)
	}
	if len(cfg.SuccessStatus) == 0 {
		cfg.SuccessStatus = []int{200, 201, 202}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = cfgdec.Duration(10 * time.Second)
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = 1 << 20
	}
	if len(cfg.Body) == 0 {
		cfg.Body = map[string]any{"to": "{{to}}", "from": "{{from}}", "text": "{{text}}", "reference": "{{id}}"}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if !bool(cfg.AllowPrivate) {
		// A provider can be created at runtime by an account holder, so the
		// gateway must not be a way into the private network.
		dialer.Control = refusePrivate
	}
	tr := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
		Proxy:               nil,
	}
	return &HTTPGateway{name: name, cfg: cfg, client: &http.Client{
		Transport: tr,
		Timeout:   cfg.Timeout.D(),
		// A redirect would let a vendor URL bounce a request elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("gateway: unresolved dial address %q", address)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("gateway: refusing to connect to private address %s (set allow_private_networks to permit it)", ip)
	}
	return nil
}

func (g *HTTPGateway) substitute(msg Message) map[string]any {
	repl := map[string]string{
		"to": msg.To, "from": msg.From, "text": msg.Text, "id": msg.ID,
		"segments": strconv.Itoa(msg.Segments), "country": msg.Country,
		"dlr": strconv.FormatBool(msg.WantDLR),
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case string:
			if strings.HasPrefix(x, "{{") && strings.HasSuffix(x, "}}") && strings.Count(x, "{{") == 1 {
				if r, ok := repl[strings.TrimSpace(x[2:len(x)-2])]; ok {
					return r
				}
			}
			for k, r := range repl {
				x = strings.ReplaceAll(x, "{{"+k+"}}", r)
			}
			return x
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, val := range x {
				out[k] = walk(val)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, val := range x {
				out[i] = walk(val)
			}
			return out
		default:
			return v
		}
	}
	return walk(cfgdec.Normalize(g.cfg.Body)).(map[string]any)
}

// Send implements Gateway.
func (g *HTTPGateway) Send(ctx context.Context, msg Message) (Receipt, error) {
	start := time.Now()
	body := g.substitute(msg)
	var (
		reader      io.Reader
		contentType string
	)
	if g.cfg.Format == "form" {
		form := url.Values{}
		for k, v := range body {
			form.Set(k, fmt.Sprint(v))
		}
		reader, contentType = strings.NewReader(form.Encode()), "application/x-www-form-urlencoded"
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			return Receipt{}, Errorf(ClassPermanent, "encode", "cannot encode request: %v", err)
		}
		reader, contentType = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, g.cfg.Method, g.cfg.URL, reader)
	if err != nil {
		return Receipt{}, Errorf(ClassProvider, "request", "cannot build request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	if msg.ID != "" {
		req.Header.Set("Idempotency-Key", msg.ID)
	}
	for k, v := range g.cfg.Headers {
		req.Header.Set(k, v)
	}
	switch g.cfg.Auth.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+g.cfg.Auth.Token)
	case "basic":
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(g.cfg.Auth.Username+":"+g.cfg.Auth.Password)))
	case "header":
		req.Header.Set(g.cfg.Auth.Header, g.cfg.Auth.Token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		// A refused private address is a configuration fault, not a blip.
		if strings.Contains(err.Error(), "refusing to connect") {
			return Receipt{}, &Error{Class: ClassProvider, Code: "blocked_address", Message: err.Error(), Cause: err}
		}
		return Receipt{}, &Error{Class: ClassRetryable, Code: "transport", Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, g.cfg.MaxResponse))
	latency := time.Since(start)

	if !slices.Contains(g.cfg.SuccessStatus, resp.StatusCode) {
		class := ClassifyHTTPStatus(resp.StatusCode)
		code := "http_" + strconv.Itoa(resp.StatusCode)
		if upstream := g.extract(raw, g.cfg.ErrorPath); upstream != "" {
			code = upstream
			if slices.Contains(g.cfg.PermanentCodes, upstream) {
				class = ClassPermanent
			} else if slices.Contains(g.cfg.ProviderCodes, upstream) {
				class = ClassProvider
			}
		}
		e := &Error{Class: class, Code: code, Message: fmt.Sprintf("upstream answered %d: %s", resp.StatusCode, snippet(raw))}
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
				e.RetryAfter = time.Duration(secs) * time.Second
			}
		}
		return Receipt{}, e
	}
	id := g.extract(raw, g.cfg.IDPath)
	if g.cfg.IDPath != "" && id == "" {
		// Accepted but not identifiable: the message is on its way, but a DLR can
		// never be matched. Treat the platform id as the reference.
		id = msg.ID
	}
	return Receipt{ProviderMessageID: id, Latency: latency, Raw: snippet(raw)}, nil
}

func snippet(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return strings.TrimSpace(string(b))
}

// extract reads a dotted path from a JSON document, as a string.
func (g *HTTPGateway) extract(raw []byte, path string) string {
	if path == "" {
		return ""
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return dig(doc, path)
}

func dig(doc any, path string) string {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return ""
			}
			cur = x[i]
		default:
			return ""
		}
	}
	switch x := cur.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}

// ParseDLR implements DLRParser using the configured field names. A body that
// is a JSON array, or an object holding one at dlr.list_path, carries several
// receipts.
func (g *HTTPGateway) ParseDLR(_ map[string][]string, body []byte) ([]DLR, error) {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("receipt is not JSON: %w", err)
	}
	var items []any
	if g.cfg.DLR.ListPath != "" {
		cur := doc
		for _, part := range strings.Split(g.cfg.DLR.ListPath, ".") {
			if m, ok := cur.(map[string]any); ok {
				cur = m[part]
			}
		}
		items, _ = cur.([]any)
	} else if arr, ok := doc.([]any); ok {
		items = arr
	} else {
		items = []any{doc}
	}
	idField, statField := g.cfg.DLR.IDField, g.cfg.DLR.StatField
	if idField == "" {
		idField = "id"
	}
	if statField == "" {
		statField = "status"
	}
	delivered := g.cfg.DLR.Delivered
	if len(delivered) == 0 {
		delivered = []string{"delivered", "DELIVRD", "success"}
	}
	failed := g.cfg.DLR.Failed
	if len(failed) == 0 {
		failed = []string{"failed", "undelivered", "UNDELIV", "REJECTD", "expired", "EXPIRED"}
	}
	var out []DLR
	for _, it := range items {
		pid := dig(it, idField)
		ref := ""
		if g.cfg.DLR.RefField != "" {
			ref = dig(it, g.cfg.DLR.RefField)
		}
		if pid == "" && ref == "" {
			continue
		}
		st := StatusUnknown
		raw := dig(it, statField)
		switch {
		case containsFold(delivered, raw):
			st = StatusDelivered
		case containsFold(failed, raw):
			st = StatusFailed
		}
		out = append(out, DLR{Provider: g.name, ProviderMessageID: pid, MessageID: ref, Status: st, Code: raw, At: time.Now()})
	}
	return out, nil
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, v) })
}

// Close implements Gateway.
func (g *HTTPGateway) Close() error {
	g.client.CloseIdleConnections()
	return nil
}
