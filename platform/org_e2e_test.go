package platform

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	_ "modernc.org/sqlite"
)

// appHarness drives an example application through fh.App.Test.
type appHarness struct {
	t        *testing.T
	platform *Platform
	base     string
	client   *http.Client
}

func newAppHarness(t *testing.T, bclPath string, env map[string]string) *appHarness {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	src, err := os.ReadFile(bclPath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Compile(context.Background(), src, filepath.Dir(bclPath), DefaultLoadOptions())
	if err != nil {
		t.Fatalf("compile %s: %v", bclPath, err)
	}
	t.Cleanup(func() { _ = p.Close() })
	app := fh.NewFast()
	if err := p.Mount(app); err != nil {
		t.Fatalf("mount: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = app.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = app.ShutdownWithTimeout(2 * time.Second)
		// fh closes the listener only if Serve registered it before Shutdown
		// ran; on a loaded machine the test can finish first, leaving Accept
		// blocked forever. Closing it here unblocks Serve in every ordering.
		_ = listener.Close()
		<-served
	})
	return &appHarness{t: t, platform: p, base: "http://" + listener.Addr().String(),
		client: &http.Client{Timeout: 10 * time.Second}}
}

// token mints a JWT through the app's own auth.jwt resource.
func (h *appHarness) token(resource, subject string, roles []string, claims map[string]any) string {
	h.t.Helper()
	res, ok := h.platform.Resource(resource)
	if !ok {
		h.t.Fatalf("no resource %q", resource)
	}
	jwt, ok := res.(*jwtAuth)
	if !ok {
		h.t.Fatalf("resource %q is %T, not auth.jwt", resource, res)
	}
	extra := map[string]any{"roles": roles}
	for k, v := range claims {
		extra[k] = v
	}
	tok, _, err := jwt.Issue(Principal{ID: subject, Roles: roles}, 0, extra)
	if err != nil {
		h.t.Fatalf("issue token: %v", err)
	}
	return tok
}

func (h *appHarness) call(method, path, token string, body any) (int, any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded
}

func dig(v any, path ...any) any {
	for _, p := range path {
		switch key := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[key]
		case int:
			list, _ := v.([]any)
			if key >= len(list) {
				return nil
			}
			v = list[key]
		}
	}
	return v
}

func codesOf(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, item := range list {
		out = append(out, Stringify(dig(item, "code"))+"="+Stringify(dig(item, "label")))
	}
	return out
}

func TestGovHierarchyExample(t *testing.T) {
	dir := t.TempDir()
	h := newAppHarness(t, "../examples/gov-hierarchy/app.bcl", map[string]string{
		"GOV_DSN":        "file:" + filepath.Join(dir, "gov.db"),
		"GOV_JWT_SECRET": strings.Repeat("k", 40),
	})
	admin := h.token("jwt", "admin-1", []string{"admin"}, nil)
	bagmati := h.token("jwt", "officer-bagmati", []string{"officer"}, map[string]any{"org_units": []string{"bagmati"}})
	ktmMetro := h.token("jwt", "clerk-ktm", []string{"clerk"}, map[string]any{"org_units": "ktm-metro", "department": "revenue"})
	morang := h.token("jwt", "officer-morang", []string{"officer"}, map[string]any{"org_units": []string{"morang"}})
	nobody := h.token("jwt", "stranger", []string{"clerk"}, nil)

	t.Run("scope", func(t *testing.T) {
		status, body := h.call("GET", "/api/org/scope", bagmati, nil)
		if status != 200 || dig(body, "global") != false || Stringify(dig(body, "assigned_ids", 0)) != "bagmati" {
			t.Fatalf("scope = %d %v", status, body)
		}
		status, body = h.call("GET", "/api/org/scope", admin, nil)
		if status != 200 || dig(body, "global") != true {
			t.Fatalf("admin scope = %d %v", status, body)
		}
		if status, _ := h.call("GET", "/api/org/scope", nobody, nil); status != 403 {
			t.Fatalf("unassigned user should get 403, got %d", status)
		}
		if status, _ := h.call("GET", "/api/org/scope", "", nil); status != 401 {
			t.Fatalf("anonymous should get 401, got %d", status)
		}
	})

	t.Run("tree and children are scoped", func(t *testing.T) {
		status, body := h.call("GET", "/api/org/tree", bagmati, nil)
		if status != 200 || Stringify(dig(body, 0, "id")) != "bagmati" || len(dig(body, 0, "children").([]any)) != 2 {
			t.Fatalf("tree = %d %v", status, body)
		}
		status, body = h.call("GET", "/api/org/units/ktm/children", bagmati, nil)
		if status != 200 || len(body.([]any)) != 2 {
			t.Fatalf("children = %d %v", status, body)
		}
		if status, _ := h.call("GET", "/api/org/units/morang/children", bagmati, nil); status != 403 {
			t.Fatalf("sibling province must be out of scope, got %d", status)
		}
	})

	t.Run("lookup inheritance", func(t *testing.T) {
		status, body := h.call("GET", "/api/lookups/services", ktmMetro, nil)
		got := strings.Join(codesOf(body), "|")
		// Kathmandu district disabled death registration; the clerk's revenue
		// department sees its own tax label; the metro adds parking.
		want := "birth=Birth registration|tax=Property tax (revenue section)|parking=Parking permit"
		if status != 200 || got != want {
			t.Fatalf("ktm-metro services = %d %q, want %q", status, got, want)
		}
		status, body = h.call("GET", "/api/lookups/services", morang, nil)
		if got := strings.Join(codesOf(body), "|"); status != 200 || got != "birth=Birth registration|death=Death registration|tax=Property tax" {
			t.Fatalf("morang services = %d %q", status, got)
		}
		status, body = h.call("GET", "/api/lookups/services?org_unit=lalitpur-metro", bagmati, nil)
		if got := strings.Join(codesOf(body), "|"); status != 200 || !strings.Contains(got, "tax=Integrated property tax") {
			t.Fatalf("lalitpur services via query = %d %q", status, got)
		}
		if status, _ := h.call("GET", "/api/lookups/services?org_unit=biratnagar", bagmati, nil); status != 403 {
			t.Fatalf("lookup outside scope should be 403, got %d", status)
		}
		// A province officer adds a local service for one of its municipalities.
		status, _ = h.call("POST", "/api/lookups", bagmati, map[string]any{
			"set": "service", "code": "water", "label": "Water connection", "node_id": "lalitpur-metro", "sort": 5})
		if status != 200 {
			t.Fatalf("lookup upsert = %d", status)
		}
		_, body = h.call("GET", "/api/lookups/services?org_unit=lalitpur-metro", bagmati, nil)
		if !strings.Contains(strings.Join(codesOf(body), "|"), "water=Water connection") {
			t.Fatalf("new lookup not visible: %v", codesOf(body))
		}
		if status, _ := h.call("POST", "/api/lookups", bagmati, map[string]any{"set": "service", "code": "x"}); status != 403 {
			t.Fatalf("non-admin global lookup must be 403, got %d", status)
		}
	})

	t.Run("unit administration", func(t *testing.T) {
		status, body := h.call("POST", "/api/org/units", bagmati, map[string]any{
			"id": "bhaktapur", "parent_id": "bagmati", "level": "district", "name": "Bhaktapur"})
		if status != 200 || dig(body, "path") != "/np/bagmati/bhaktapur/" {
			t.Fatalf("create district = %d %v", status, body)
		}
		if status, _ := h.call("POST", "/api/org/units", bagmati, map[string]any{
			"id": "x", "parent_id": "bhaktapur", "level": "state"}); status != 422 {
			t.Fatalf("level violation should be 422, got %d", status)
		}
		if status, _ := h.call("POST", "/api/org/units", bagmati, map[string]any{
			"id": "y", "parent_id": "morang", "level": "municipality"}); status != 403 {
			t.Fatalf("creating under another province should be 403, got %d", status)
		}
		if status, _ := h.call("POST", "/api/org/units", bagmati, map[string]any{"id": "bagmati", "name": "Renamed"}); status != 403 {
			t.Fatalf("an officer must not edit their own scope root, got %d", status)
		}
	})

	t.Run("scoped applications", func(t *testing.T) {
		status, body := h.call("POST", "/api/applications", ktmMetro, map[string]any{
			"org_unit_id": "ktm-metro", "service": "parking", "applicant": "Ram"})
		if status != 201 || dig(body, "org_path") != "/np/bagmati/ktm/ktm-metro/" {
			t.Fatalf("create application = %d %v", status, body)
		}
		if status, _ := h.call("POST", "/api/applications", ktmMetro, map[string]any{
			"org_unit_id": "ktm-metro", "service": "death", "applicant": "Sita"}); status != 422 {
			t.Fatalf("a service disabled for the district must be rejected, got %d", status)
		}
		if status, _ := h.call("POST", "/api/applications", ktmMetro, map[string]any{
			"org_unit_id": "tokha", "service": "birth", "applicant": "Hari"}); status != 403 {
			t.Fatalf("clerk filing for a sibling municipality must be 403, got %d", status)
		}
		if status, _ := h.call("POST", "/api/applications", morang, map[string]any{
			"org_unit_id": "biratnagar", "service": "birth", "applicant": "Gita"}); status != 201 {
			t.Fatalf("morang create = %d", status)
		}
		if status, _ := h.call("POST", "/api/applications", bagmati, map[string]any{
			"org_unit_id": "lalitpur-metro", "service": "water", "applicant": "Maya"}); status != 201 {
			t.Fatalf("bagmati create in lalitpur = %d", status)
		}

		count := func(token string) int {
			status, body := h.call("GET", "/api/applications", token, nil)
			if status != 200 {
				t.Fatalf("list = %d %v", status, body)
			}
			list, _ := body.([]any)
			return len(list)
		}
		if got := count(ktmMetro); got != 1 {
			t.Fatalf("ktm-metro clerk sees %d applications, want 1", got)
		}
		if got := count(bagmati); got != 2 {
			t.Fatalf("bagmati officer sees %d applications, want 2 (ktm-metro + lalitpur-metro)", got)
		}
		if got := count(morang); got != 1 {
			t.Fatalf("morang officer sees %d, want 1", got)
		}
		if got := count(admin); got != 3 {
			t.Fatalf("admin sees %d, want 3", got)
		}
	})

	t.Run("persistence", func(t *testing.T) {
		// A second generation over the same database sees the units and
		// lookups written through the API.
		h2 := newAppHarness(t, "../examples/gov-hierarchy/app.bcl", nil)
		status, body := h2.call("GET", "/api/lookups/services?org_unit=lalitpur-metro", h2.token("jwt", "a", []string{"admin"}, nil), nil)
		if status != 200 || !strings.Contains(strings.Join(codesOf(body), "|"), "water=") {
			t.Fatalf("lookup not persisted: %d %v", status, body)
		}
		_, body = h2.call("GET", "/api/org/units/bagmati/children", h2.token("jwt", "a", []string{"admin"}, nil), nil)
		found := false
		for _, c := range body.([]any) {
			if dig(c, "id") == "bhaktapur" {
				found = true
			}
		}
		if !found {
			t.Fatalf("unit not persisted: %v", body)
		}
	})
}
