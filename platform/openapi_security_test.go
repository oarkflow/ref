package platform

import "testing"

func TestOpenAPISecurityFollowsConfiguredResources(t *testing.T) {
	p := &Platform{Document: Document{
		Resources: []ResourceSpec{
			{Name: "browser", Kind: "session.memory", Config: map[string]any{"cookie": "app_session"}},
			{Name: "session_login", Kind: "auth.session", Config: map[string]any{"session": "browser"}},
			{Name: "token", Kind: "auth.jwt"},
			{Name: "service_key", Kind: "auth.api_key", Config: map[string]any{"header": "X-Service-Key"}},
			{Name: "login", Kind: "auth.chain", Config: map[string]any{"authenticators": []any{"session_login", "token", "service_key"}}},
		},
		Routes: []RouteSpec{
			{Name: "protected", Path: "/protected", Method: "GET", Auth: "login"},
			{Name: "public", Path: "/public", Method: "GET", Auth: "login", AllowAnonymous: true},
		},
	}}
	spec := p.OpenAPI()
	schemes := spec["components"].(map[string]any)["securitySchemes"].(map[string]any)
	if got := schemes["cookieAuth_browser"].(map[string]any)["name"]; got != "app_session" {
		t.Fatalf("cookie name = %v", got)
	}
	if got := schemes["apiKeyAuth_service_key"].(map[string]any)["name"]; got != "X-Service-Key" {
		t.Fatalf("API key header = %v", got)
	}
	if _, ok := schemes["bearerAuth"]; !ok {
		t.Fatal("JWT bearer scheme missing")
	}
	paths := spec["paths"].(map[string]any)
	protected := paths["/protected"].(map[string]any)["get"].(map[string]any)
	if got := len(protected["security"].([]any)); got != 3 {
		t.Fatalf("chain alternatives = %d, want 3", got)
	}
	public := paths["/public"].(map[string]any)["get"].(map[string]any)
	if _, ok := public["security"]; ok {
		t.Fatal("anonymous route advertises required authentication")
	}
}

func TestOpenAPITagsDoNotAssumeMedicalDomain(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"coding", "Coding"},
		{"provider", "Provider"},
		{"charge_code", "Charge Code"},
	} {
		if got := formatTag(tc.input); got != tc.want {
			t.Fatalf("tag %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}
