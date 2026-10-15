package main_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/serve"

	_ "modernc.org/sqlite"
)

func TestSPLTemplateRendering(t *testing.T) {
	templatesDir := "resources/templates"
	renderer := serve.NewRenderer(serve.RendererConfig{
		TemplatesDir: templatesDir,
		Reload:       true,
		Env:          "development",
		Globals: map[string]any{
			"appName":     "CLEAR Medical Coding",
			"appVersion":  "2.0.0",
			"title":       "Medical Coding Platform",
			"environment": "development",
			"currentYear": "2026",
			"q":           "",
			"status":      "",
			"wid":         "wi_memorial_er",
			"error":       "",
			"success":     "",
			"user": map[string]any{
				"id":    "usr_coder_01",
				"name":  "Gita Coder",
				"email": "coder@clear.io",
				"role":  "coder",
			},
		},
	})

	// 1. Render Login Page
	var buf bytes.Buffer
	err := renderer.Render(&buf, "pages/auth/login", map[string]any{
		"title": "Sign In",
		"email": "coder@clear.io",
	})
	if err != nil {
		t.Fatalf("Render login template failed: %v", err)
	}
	loginHTML := buf.String()
	if !strings.Contains(loginHTML, "CLEAR Medical Coding") {
		t.Errorf("expected login page to contain 'CLEAR Medical Coding'")
	}
	if !strings.Contains(loginHTML, "coder@clear.io") {
		t.Errorf("expected login page to contain 'coder@clear.io'")
	}
	if !strings.Contains(loginHTML, "Quick Role Demo Access") {
		t.Errorf("expected login page to contain 'Quick Role Demo Access'")
	}

	// 2. Render Dashboard Page
	buf.Reset()
	err = renderer.Render(&buf, "pages/dashboard/index", map[string]any{
		"title": "Operational Dashboard",
		"stats": map[string]any{
			"open_count":        "12",
			"in_progress_count": "4",
			"qa_count":          "3",
			"suspend_count":     "2",
		},
		"summary_rows": []map[string]any{
			{
				"facility_name":     "Memorial General Hospital",
				"work_item_name":    "Emergency Department",
				"work_item_type":    "Both",
				"work_item_id":      "wi_memorial_er",
				"open_count":        12,
				"in_progress_count": 4,
				"qa_count":          3,
				"de_count":          2,
				"suspend_count":     1,
				"complete_count":    45,
			},
		},
	})
	if err != nil {
		t.Fatalf("Render dashboard template failed: %v", err)
	}
	dashHTML := buf.String()
	if !strings.Contains(dashHTML, "Operational Dashboard") {
		t.Errorf("expected dashboard page to contain 'Operational Dashboard'")
	}
	if !strings.Contains(dashHTML, "SLA: 24h Target") && !strings.Contains(dashHTML, "SLA 24h") {
		t.Errorf("expected dashboard page to contain SLA 24h indicators")
	}

	// 3. Render Coding Queue Page
	buf.Reset()
	err = renderer.Render(&buf, "pages/coding/queue", map[string]any{
		"title": "Clinical Coding Queue",
		"wid":   "wi_memorial_er",
		"encounters": []map[string]any{
			{
				"encounter_id":               "enc_1001",
				"work_item_id":               "wi_memorial_er",
				"patient_name":               "Jane Doe",
				"patient_sex":                "F",
				"patient_dob":                "1985-05-12",
				"patient_mrn":                "MRN-1001",
				"patient_fin":                "FIN-2001",
				"encounter_dos":              "2026-03-10",
				"encounter_location":         "ED Main",
				"encounter_chief_complaint":  "Chest pain",
				"encounter_reason_for_visit": "Discomfort radiating to shoulder",
				"encounter_status":           "OPEN",
			},
		},
		"inprogress_encounters": []map[string]any{},
	})
	if err != nil {
		t.Fatalf("Render coding queue template failed: %v", err)
	}
	queueHTML := buf.String()
	if !strings.Contains(queueHTML, "Jane Doe") {
		t.Errorf("expected queue page to contain patient Jane Doe")
	}
	if !strings.Contains(queueHTML, "Claim & Start") {
		t.Errorf("expected queue page to contain 'Claim & Start' button")
	}

	// 4. Render Coding Workspace Page (with Dynamic Rows and E&M Group)
	buf.Reset()
	err = renderer.Render(&buf, "pages/coding/workspace", map[string]any{
		"title": "Coding Workspace",
		"wid":   "wi_memorial_er",
		"eid":   "enc_1001",
		"patient": map[string]any{
			"encounter_id":               "enc_1001",
			"patient_name":               "Jane Doe",
			"patient_sex":                "F",
			"patient_dob":                "1985-05-12",
			"patient_mrn":                "MRN-1001",
			"patient_fin":                "FIN-2001",
			"encounter_dos":              "2026-03-10",
			"encounter_location":         "ED Main",
			"encounter_type":             "Emergency",
			"encounter_chief_complaint":  "Chest pain",
			"encounter_reason_for_visit": "Discomfort radiating to shoulder",
			"encounter_status":           "IN_PROGRESS",
			"deadline":                   "18h left",
		},
		"cpt_rows": []map[string]any{
			{"cpt_code": "99285", "modifier1": "25", "modifier2": "", "units": 1, "nfrvu": "4.85"},
		},
		"dx_rows": []map[string]any{
			{"icd_code": "R07.9", "priority": 1, "poa": "Y"},
		},
		"em": map[string]any{
			"mdm_level":       "High",
			"em_code":         "99285",
			"modifier":        "25",
			"downcode_reason": "",
		},
	})
	if err != nil {
		t.Fatalf("Render coding workspace template failed: %v", err)
	}
	wsHTML := buf.String()
	if !strings.Contains(wsHTML, "Jane Doe") {
		t.Errorf("expected workspace to contain patient 'Jane Doe'")
	}
	if !strings.Contains(wsHTML, "99285") {
		t.Errorf("expected workspace to contain CPT code '99285'")
	}
	if !strings.Contains(wsHTML, "R07.9") {
		t.Errorf("expected workspace to contain ICD code 'R07.9'")
	}
	if !strings.Contains(wsHTML, "Add CPT Row") {
		t.Errorf("expected workspace to contain 'Add CPT Row' dynamic row button")
	}
	if !strings.Contains(wsHTML, "Add Diagnosis Row") {
		t.Errorf("expected workspace to contain 'Add Diagnosis Row' dynamic row button")
	}
	if !strings.Contains(wsHTML, "Evaluation & Management (E&M) Level") {
		t.Errorf("expected workspace to contain E&M form group")
	}
	if !strings.Contains(wsHTML, "Suspend Chart") {
		t.Errorf("expected workspace to contain Suspend Chart modal trigger")
	}

	// 5. Render QA Review Page
	buf.Reset()
	err = renderer.Render(&buf, "pages/qa/index", map[string]any{
		"title": "QA Review",
		"qa_encounters": []map[string]any{
			{
				"encounter_id":     "enc_1001",
				"work_item_id":     "wi_memorial_er",
				"patient_name":     "Jane Doe",
				"patient_sex":      "F",
				"patient_mrn":      "MRN-1001",
				"patient_fin":      "FIN-2001",
				"encounter_dos":    "2026-03-10",
				"code_assigned":    "Gita Coder",
				"encounter_status": "IN_QA",
			},
		},
	})
	if err != nil {
		t.Fatalf("Render QA template failed: %v", err)
	}
	qaHTML := buf.String()
	if !strings.Contains(qaHTML, "Quality Assurance (QA) Review") {
		t.Errorf("expected QA review title")
	}
	if !strings.Contains(qaHTML, "Agree & Approve") {
		t.Errorf("expected QA Agree & Approve button")
	}

	// 6. Render Suspends Page
	buf.Reset()
	err = renderer.Render(&buf, "pages/suspends/index", map[string]any{
		"title": "Suspends & Queries",
		"suspended_encounters": []map[string]any{
			{
				"encounter_id":   "enc_1002",
				"work_item_id":   "wi_memorial_er",
				"patient_name":   "Robert Smith",
				"patient_mrn":    "MRN-1002",
				"encounter_dos":  "2026-03-11",
				"suspend_reason": "MISSING_DOC",
				"suspend_note":   "Physician operative report missing",
				"created_by":     "Gita Coder",
			},
		},
	})
	if err != nil {
		t.Fatalf("Render suspends template failed: %v", err)
	}
	susHTML := buf.String()
	if !strings.Contains(susHTML, "Suspends & Clinical Queries") {
		t.Errorf("expected suspends title")
	}
	if !strings.Contains(susHTML, "MISSING_DOC") {
		t.Errorf("expected MISSING_DOC reason")
	}
	if !strings.Contains(susHTML, "Resolve & Release") {
		t.Errorf("expected Resolve & Release action")
	}

	// 7. Render Data Entry Page
	buf.Reset()
	err = renderer.Render(&buf, "pages/dataentry/index", map[string]any{
		"title": "Data Entry",
		"de_encounters": []map[string]any{
			{
				"encounter_id":  "enc_1001",
				"work_item_id":  "wi_memorial_er",
				"patient_name":  "Jane Doe",
				"patient_fin":   "FIN-2001",
				"encounter_dos": "2026-03-10",
			},
		},
	})
	if err != nil {
		t.Fatalf("Render data entry template failed: %v", err)
	}
	deHTML := buf.String()
	if !strings.Contains(deHTML, "Data Entry & Billing Queue") {
		t.Errorf("expected Data Entry title")
	}
	if !strings.Contains(deHTML, "Post & Transmit Claim") {
		t.Errorf("expected Post & Transmit Claim button")
	}
}

func TestBCLEndToEndWebRoutes(t *testing.T) {
	ctx := context.Background()
	_ = config.LoadDotenv(config.DotenvPath())
	_ = config.LoadSecretFile("SESSION_SECRET", "./.data/secrets/session_secret")
	_ = config.LoadSecretFile("JWT_SECRET", "./.data/secrets/jwt_secret")
	t.Setenv("SESSION_SECRET", "super-secure-argon2id-medical-coding-session-secret-32-bytes")
	t.Setenv("JWT_SECRET", "super-secure-argon2id-medical-coding-jwt-secret-32-bytes")

	bclDir := config.ResolveDir("examples/medical-coding-platform/resources/config", "./resources/config", "resources/config")
	templatesDir := config.ResolveDir("examples/medical-coding-platform/resources/templates", "./resources/templates", "resources/templates")

	renderer := serve.NewRenderer(serve.RendererConfig{
		TemplatesDir: templatesDir,
		Reload:       true,
		Env:          "development",
		Globals: map[string]any{
			"appName":     "CLEAR Medical Coding",
			"appVersion":  "2.0.0",
			"title":       "Medical Coding Platform",
			"environment": "development",
			"q":           "",
			"status":      "",
			"wid":         "wi_memorial_er",
			"error":       "",
			"success":     "",
		},
	})

	opts := platform.DefaultLoadOptions()
	opts.Profile = "development"
	opts.Templates = renderer

	p, err := platform.LoadDirRecursive(ctx, bclDir, opts)
	if err != nil {
		t.Fatalf("platform.LoadDirRecursive failed: %v", err)
	}
	defer p.Close()

	app := fh.NewFast(fh.WithTemplateEngine(renderer))
	if err := p.Mount(app); err != nil {
		t.Fatalf("platform.Mount failed: %v", err)
	}

	// Verify all web routes and form actions are compiled and registered
	routes := app.Routes()
	expectedWebRoutes := []string{
		"GET /",
		"GET /dashboard",
		"GET /login",
		"GET /coding/queue",
		"GET /coding/:wid/:eid",
		"GET /qa",
		"GET /suspends",
		"GET /dataentry",
		"GET /web/facilities",
		"POST /web/coding/:wid/:eid/start-coding",
		"POST /web/coding/:wid/:eid/save-patient-details",
		"POST /web/coding/:wid/:eid/save-coding-documentation",
		"POST /web/coding/:wid/:eid/abandon",
		"POST /web/suspends/:wid/:eid/suspend",
		"POST /web/suspends/:wid/:eid/release",
		"POST /web/qa/:wid/:eid/complete",
		"POST /web/dataentry/:wid/:eid/complete",
	}
	routeMap := make(map[string]bool)
	for _, r := range routes {
		routeMap[r.Method+" "+r.Path] = true
	}
	for _, expected := range expectedWebRoutes {
		if !routeMap[expected] {
			t.Errorf("expected route %s to be compiled and registered", expected)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- app.Serve(ln) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		_ = ln.Close()
	})
	baseURL := "http://" + ln.Addr().String()

	doReq := func(method, path string) (*http.Response, string) {
		req, err := http.NewRequest(method, baseURL+path, nil)
		if err != nil {
			t.Fatalf("NewRequest failed: %v", err)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s failed: %v", method, path, err)
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(bodyBytes)
	}

	// 1. Test GET /
	resp, body := doReq("GET", "/")
	t.Logf("GET / status=%d", resp.StatusCode)
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for GET /, got %d (body=%s)", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Operational Dashboard") {
		t.Errorf("expected dashboard title in GET / response")
	}

	// 2. Test GET /dashboard
	resp, body = doReq("GET", "/dashboard")
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for GET /dashboard, got %d", resp.StatusCode)
	}

	// 3. Test GET /login
	resp, body = doReq("GET", "/login")
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for GET /login, got %d", resp.StatusCode)
	}
	if !strings.Contains(body, "Sign In") {
		t.Errorf("expected Sign In in /login response")
	}

	// 4. Test GET /coding/queue
	resp, body = doReq("GET", "/coding/queue")
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for GET /coding/queue, got %d (body=%s)", resp.StatusCode, body)
	}

	// 5. Test GET /coding/wi_memorial_er/enc_1001 (workspace)
	resp, body = doReq("GET", "/coding/wi_memorial_er/enc_1001")
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for workspace, got %d (body=%s)", resp.StatusCode, body)
	}

	// 6. Test GET /qa
	resp, body = doReq("GET", "/qa")
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for /qa, got %d", resp.StatusCode)
	}

	// 7. Test GET /suspends
	resp, body = doReq("GET", "/suspends")
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for /suspends, got %d", resp.StatusCode)
	}

	// 8. Test GET /dataentry
	resp, body = doReq("GET", "/dataentry")
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for /dataentry, got %d", resp.StatusCode)
	}

	// 9. Test GET /static/css/app.css
	resp, body = doReq("GET", "/static/css/app.css")
	t.Logf("GET /static/css/app.css status=%d content-type=%s len=%d", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for GET /static/css/app.css, got %d", resp.StatusCode)
	}

	// 10. Test GET /static/js/coding-workspace.js
	resp, body = doReq("GET", "/static/js/coding-workspace.js")
	t.Logf("GET /static/js/coding-workspace.js status=%d content-type=%s len=%d", resp.StatusCode, resp.Header.Get("Content-Type"), len(body))
	if resp.StatusCode != 200 {
		t.Errorf("expected status 200 for GET /static/js/coding-workspace.js, got %d", resp.StatusCode)
	}
}
