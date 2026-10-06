package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAppConsoleEndpoints(t *testing.T) {
	e := newEnv(t)

	// 1. Create App via POST /api/v1/apps
	createBody := map[string]any{
		"id":          "app_ecommerce",
		"name":        "E-Commerce Store",
		"description": "Storefront and checkout",
		"version":     "1.0.0",
		"source_bcl": `
app "ecommerce" { version "1.0.0" }
intent "cart.checkout" {
  node "calc" { uses "constant" config { value 99 } provides [total] }
}
`,
		"status": "draft",
	}

	status, body := e.call(http.MethodPost, "/api/v1/apps", tokEditor, createBody)
	if status != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", status, string(body))
	}

	var createdResp struct {
		App struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Status  string   `json:"status"`
			Intents []string `json:"intents"`
		} `json:"app"`
	}
	if err := json.Unmarshal(body, &createdResp); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if createdResp.App.ID != "app_ecommerce" || len(createdResp.App.Intents) != 1 {
		t.Fatalf("unexpected app created: %+v", createdResp.App)
	}

	// 2. List Apps via GET /api/v1/apps
	status, body = e.call(http.MethodGet, "/api/v1/apps", tokViewer, nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}
	var listResp struct {
		Apps []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(listResp.Apps) != 1 || listResp.Apps[0].ID != "app_ecommerce" {
		t.Fatalf("unexpected apps list: %+v", listResp.Apps)
	}

	// 3. Get App via GET /api/v1/apps/app_ecommerce
	status, body = e.call(http.MethodGet, "/api/v1/apps/app_ecommerce", tokViewer, nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}

	// 4. Update App via PUT /api/v1/apps/app_ecommerce
	updateBody := map[string]any{
		"name": "E-Commerce Storefront Pro",
	}
	status, body = e.call(http.MethodPut, "/api/v1/apps/app_ecommerce", tokEditor, updateBody)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}

	// 5. Activate App via POST /api/v1/apps/app_ecommerce/activate
	status, body = e.call(http.MethodPost, "/api/v1/apps/app_ecommerce/activate", tokReviewer, map[string]any{"revision_id": 2})
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}

	// 6. Check Health via GET /api/v1/apps/app_ecommerce/health
	status, body = e.call(http.MethodGet, "/api/v1/apps/app_ecommerce/health", tokViewer, nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}
	var healthResp struct {
		Health struct {
			AppID   string `json:"app_id"`
			Status  string `json:"status"`
			Healthy bool   `json:"healthy"`
		} `json:"health"`
	}
	if err := json.Unmarshal(body, &healthResp); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	if healthResp.Health.Status != "active" || !healthResp.Health.Healthy {
		t.Fatalf("unexpected health response: %+v", healthResp.Health)
	}

	// 7. Run Intent via POST /api/v1/apps/app_ecommerce/run
	runBody := map[string]any{
		"intent": "cart.checkout",
		"inputs": map[string]any{"user_id": "usr_123"},
	}
	status, body = e.call(http.MethodPost, "/api/v1/apps/app_ecommerce/run", tokEditor, runBody)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK on run, got %d: %s", status, string(body))
	}

	// 8. Check Metrics via GET /api/v1/apps/app_ecommerce/metrics
	status, body = e.call(http.MethodGet, "/api/v1/apps/app_ecommerce/metrics", tokViewer, nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}

	// 9. Check Logs via GET /api/v1/apps/app_ecommerce/logs
	status, body = e.call(http.MethodGet, "/api/v1/apps/app_ecommerce/logs", tokViewer, nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}
	var logsResp struct {
		Logs []struct {
			Message string `json:"message"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(body, &logsResp); err != nil {
		t.Fatalf("unmarshal logs: %v", err)
	}
	if len(logsResp.Logs) < 2 {
		t.Fatalf("expected at least 2 log entries, got: %d", len(logsResp.Logs))
	}

	// 10. Deactivate via POST /api/v1/apps/app_ecommerce/deactivate
	status, body = e.call(http.MethodPost, "/api/v1/apps/app_ecommerce/deactivate", tokReviewer, nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}

	// 11. Delete via DELETE /api/v1/apps/app_ecommerce
	status, body = e.call(http.MethodDelete, "/api/v1/apps/app_ecommerce", tokAdmin, nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", status, string(body))
	}
}
