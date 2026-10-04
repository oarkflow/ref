package e2e

import "testing"

func TestOperatorUserApi(t *testing.T) {
	s := start(t)
	if status, out := s.do("GET", "/v1/admin/users", "", nil); status != 401 {
		t.Fatalf("no key = %d %v", status, out)
	}
	status, out := s.admin("GET", "/v1/admin/users", nil)
	if status != 200 {
		t.Fatalf("list = %d %v", status, out)
	}
	key := s.key("demo")
	if len(key) < 20 {
		t.Fatalf("key = %q", key)
	}
	if status, out := s.admin("POST", "/v1/admin/users/demo/topup", map[string]any{"amount": 5, "reference": "pay-1"}); status != 200 || out["applied"] != true || out["balance"] != 30.0 {
		t.Fatalf("topup = %d %v", status, out)
	}
	if status, out := s.admin("POST", "/v1/admin/users/demo/topup", map[string]any{"amount": 5, "reference": "pay-1"}); status != 200 || out["applied"] != false || out["balance"] != 30.0 {
		t.Fatalf("replayed topup = %d %v", status, out)
	}
	if status, _ := s.admin("POST", "/v1/admin/users/nobody/topup", map[string]any{"amount": 5, "reference": "pay-2"}); status != 404 {
		t.Fatalf("unknown account = %d", status)
	}
	if status, out := s.admin("PUT", "/v1/admin/users/newco", map[string]any{"name": "NewCo", "countries": []string{"NP", "IN"}}); status != 200 || out["tenant"] != "newco" {
		t.Fatalf("create = %d %v", status, out)
	}
	if status, out := s.admin("GET", "/v1/admin/users/newco/balance", nil); status != 200 || out["balance"] != 0.0 {
		t.Fatalf("balance = %d %v", status, out)
	}
}
