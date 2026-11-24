// Command casekit runs a solution-agnostic case runner: every pipeline in the
// application document is served through one set of routes and one browser UI.
//
//	go run ./examples/casekit -demo            # dev secrets, stub faceworker, UI at /
//	go run ./examples/casekit -token '{"sub":"o1","roles":["officer"]}'
//
// -demo supplies throwaway secrets and starts a STUB faceworker. The stub is a
// test double, not a biometric model: it rejects frames that are all identical
// (a still photo held to the camera) and accepts anything else. Production
// must set CASEKIT_LIVENESS_URL and CASEKIT_LIVENESS_TOKEN to a real faceworker.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/oarkflow/fh"
	"github.com/oarkflow/ref/config"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/serve"
	_ "modernc.org/sqlite"
)

type tokenIssuer interface {
	Issue(platform.Principal, time.Duration, map[string]any) (string, time.Time, error)
}

func main() {
	cfgPath := flag.String("config", "examples/casekit/app.bcl", "path to the application document")
	addr := flag.String("addr", "127.0.0.1:8097", "address to listen on")
	token := flag.String("token", "", "print a development JWT for these JSON claims and exit")
	demo := flag.Bool("demo", false, "use throwaway dev secrets and a stub faceworker (never in production)")
	flag.Parse()

	// Real environment variables win; then ENV_FILE or .env in the working
	// directory; then .env next to the application document.
	for _, path := range []string{config.DotenvPath(), filepath.Join(filepath.Dir(*cfgPath), ".env")} {
		if err := config.LoadDotenv(path); err != nil {
			log.Fatalf("casekit: %s: %v", path, err)
		}
	}
	if *demo {
		for _, k := range []string{"CASEKIT_JWT_SECRET", "CASEKIT_SIGNING_SECRET"} {
			if os.Getenv(k) == "" {
				os.Setenv(k, randomSecret())
			}
		}
		if os.Getenv("CASEKIT_LIVENESS_URL") == "" {
			url, stop := startStubFaceworker()
			defer stop()
			os.Setenv("CASEKIT_LIVENESS_URL", url)
			os.Setenv("CASEKIT_LIVENESS_TOKEN", "demo-token")
			log.Printf("casekit: DEMO stub faceworker at %s (not a real liveness model)", url)
		}
	}
	// The first administrator is created on an empty user directory. Without a
	// password in the environment, make one and say so once.
	if os.Getenv("CASEKIT_ADMIN_PASSWORD") == "" {
		pw := randomSecret()[:20]
		os.Setenv("CASEKIT_ADMIN_PASSWORD", pw)
		email := os.Getenv("CASEKIT_ADMIN_EMAIL")
		if email == "" {
			email = "admin@casekit.local"
		}
		log.Printf("casekit: first administrator %s / %s (only used while no one has an account yet; set CASEKIT_ADMIN_PASSWORD to choose it)", email, pw)
	}
	_ = os.MkdirAll(".data", 0o700)
	// Pages are SPL templates under templates/, rendered by the platform's routes.
	renderer := serve.NewRenderer(serve.RendererConfig{
		TemplatesDir: filepath.Join(filepath.Dir(*cfgPath), "templates"),
		Reload:       *demo,
		Env:          map[bool]string{true: "development", false: "production"}[*demo],
		Globals:      map[string]any{"title": "Casekit", "appName": "Casekit", "page": "", "error": "", "success": "", "ui": map[string]any{"accent": "", "density": "", "brand": ""}, "workspace": map[string]any{"level": "", "label": "", "required": false}, "intro": map[string]any{"title": "", "image": "", "image_alt": "", "content": "", "button": "Start", "documents": []any{}}, "stages": []any{}, "name": "", "description": ""},
	})
	opts := platform.DefaultLoadOptions()
	opts.Templates = renderer
	src, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatalf("casekit: %v", err)
	}
	if *demo {
		src = demoClocks(src)
	}
	app, err := platform.Compile(context.Background(), src, filepath.Dir(*cfgPath), opts)
	if err != nil {
		log.Fatalf("casekit: compile: %v", err)
	}
	defer app.Close()

	if *token != "" {
		fmt.Println(issueToken(app, *token))
		return
	}
	server := fh.NewFast(fh.WithTemplateEngine(renderer))
	if err := app.Mount(server); err != nil {
		log.Fatalf("casekit: mount: %v", err)
	}
	if *demo {
		// Demo only: signed tokens for ready-made accounts so the UI can sign in with a click.
		// The secret is random per run, so tokens cannot be made offline.
		type account struct {
			ID, Label, Group, Hint string
			Claims                 string
		}
		list := []account{
			{"officer", "Officer", "Workflow staff", "reviews onboarding and certificate cases", `{"sub":"demo-officer","roles":["officer"]}`},
			{"manager", "Manager", "Workflow staff", "approves expenses and certificates", `{"sub":"demo-manager","roles":["manager"]}`},
			{"director", "Director", "Workflow staff", "approves large expenses", `{"sub":"demo-director","roles":["director"]}`},
			{"admin", "Admin", "Workflow staff", "takes a stand on missed deadlines; administers every workspace", `{"sub":"demo-admin","roles":["admin"],"org_units":["himal"]}`},
			{"city_nurse", "Nurse", "City General Hospital", "records encounters", `{"sub":"demo-city-nurse","roles":["nurse"],"org_units":["city"]}`},
			{"city_coder", "Coder", "City General Hospital", "assigns CPT codes", `{"sub":"demo-city-coder","roles":["coder"],"org_units":["city"]}`},
			{"city_supervisor", "Supervisor", "City General Hospital", "signs off", `{"sub":"demo-city-supervisor","roles":["supervisor"],"org_units":["city"]}`},
			{"city_admin", "Workspace admin", "City General Hospital", "manages this hospital's providers, nurses, codes and settings", `{"sub":"demo-city-admin","roles":["admin","supervisor"],"org_units":["city"]}`},
			{"river_nurse", "Nurse", "Riverside Clinic", "records encounters", `{"sub":"demo-river-nurse","roles":["nurse"],"org_units":["river"]}`},
			{"river_coder", "Coder", "Riverside Clinic", "assigns CPT codes", `{"sub":"demo-river-coder","roles":["coder"],"org_units":["river"]}`},
			{"river_supervisor", "Supervisor", "Riverside Clinic", "signs off", `{"sub":"demo-river-supervisor","roles":["supervisor"],"org_units":["river"]}`},
			{"river_admin", "Workspace admin", "Riverside Clinic", "manages this clinic's reference data and settings", `{"sub":"demo-river-admin","roles":["admin"],"org_units":["river"]}`},
		}
		// Each account is a real member of the user directory (a password nobody knows), and its token is
		// issued for that member: role recipients, assignment and the people lists then find it like anyone else.
		var directory *platform.IdentityDirectory
		if r, ok := app.Resource("people"); ok {
			directory, _ = r.(*platform.IdentityDirectory)
		}
		tokens, accounts := map[string]string{}, []map[string]string{}
		for _, a := range list {
			if directory != nil {
				var claims map[string]any
				if err := json.Unmarshal([]byte(a.Claims), &claims); err != nil {
					log.Fatalf("casekit: demo account %s: %v", a.ID, err)
				}
				var roles []string
				for _, r := range claims["roles"].([]any) {
					roles = append(roles, fmt.Sprint(r))
				}
				unit := ""
				if units, ok := claims["org_units"].([]any); ok && len(units) > 0 {
					unit = fmt.Sprint(units[0])
				}
				email := a.ID + "@demo.local"
				// The directory may still be seeding its first administrator: retry a busy database briefly.
				var id string
				var err error
				for try := 0; try < 20; try++ {
					if id, err = directory.EnsureMember(context.Background(), "casekit", email, a.Label+" ("+a.Group+")", roles, unit); err == nil || !strings.Contains(err.Error(), "concurrent change") {
						break
					}
					time.Sleep(150 * time.Millisecond)
				}
				if err != nil {
					log.Fatalf("casekit: demo account %s: %v", a.ID, err)
				}
				claims["sub"], claims["email"], claims["name"] = id, email, a.Label+" · "+a.Group
				raw, _ := json.Marshal(claims)
				a.Claims = string(raw)
			}
			tok := issueToken(app, a.Claims)
			tokens[a.ID] = tok
			accounts = append(accounts, map[string]string{"id": a.ID, "label": a.Label, "group": a.Group, "hint": a.Hint, "token": tok})
		}
		server.Get("/demo/tokens", func(c fh.Ctx) error { return c.JSON(tokens) })
		server.Get("/demo/accounts", func(c fh.Ctx) error { return c.JSON(accounts) })
		log.Printf("casekit: DEMO sign-in buttons enabled (%d accounts)", len(list))
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		_ = server.ShutdownWithTimeout(10 * time.Second)
	}()
	log.Printf("casekit: listening on http://%s", *addr)
	if err := server.Listen(*addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("casekit: %v", err)
	}
}

// demoClocks shortens every SLA and the sweep so a missed deadline can be seen
// within a minute: deadlines given in hours become 45s, warnings 15s and the sweep
// runs every 5s. Deadlines in days (the applicant's own stages) are left alone.
func demoClocks(src []byte) []byte {
	src = regexp.MustCompile(`(?m)^(\s*duration )"\d+h"`).ReplaceAll(src, []byte(`${1}"45s"`))
	src = regexp.MustCompile(`(?m)^(\s*warn_before )"\d+h"`).ReplaceAll(src, []byte(`${1}"15s"`))
	return regexp.MustCompile(`(?m)^(\s*every )1m`).ReplaceAll(src, []byte(`${1}5s`))
}

func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func issueToken(app *platform.Platform, claimsJSON string) string {
	var claims map[string]any
	if err := json.Unmarshal([]byte(claimsJSON), &claims); err != nil {
		log.Fatalf("-token: %v", err)
	}
	resource, ok := app.Resource("jwt")
	if !ok {
		log.Fatal("-token: the app has no \"jwt\" resource")
	}
	issuer, ok := resource.(tokenIssuer)
	if !ok {
		log.Fatal("-token: the \"jwt\" resource cannot issue tokens")
	}
	// One tenant: everyone, whether signed in by password or by token, shares the same workspaces.
	if _, ok := claims["tenant_id"]; !ok {
		claims["tenant_id"] = "casekit"
	}
	principal := platform.Principal{ID: fmt.Sprint(claims["sub"]), TenantID: fmt.Sprint(claims["tenant_id"])}
	if roles, ok := claims["roles"].([]any); ok {
		for _, r := range roles {
			principal.Roles = append(principal.Roles, fmt.Sprint(r))
		}
	}
	signed, _, err := issuer.Issue(principal, 12*time.Hour, claims)
	if err != nil {
		log.Fatalf("-token: %v", err)
	}
	return signed
}

// startStubFaceworker serves the faceworker's three endpoints with a trivial
// rule: frames that are all identical look like a replayed still and score
// low; anything else passes. Test double only.
func startStubFaceworker() (string, func()) {
	mux := http.NewServeMux()
	decode := func(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
		if r.Header.Get("Authorization") != "Bearer demo-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return nil, false
		}
		var body map[string]any
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&body) != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return nil, false
		}
		return body, true
	}
	frames := func(body map[string]any) []string {
		var out []string
		list, _ := body["frames"].([]any)
		for _, f := range list {
			if s, ok := f.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	mux.HandleFunc("/v1/pad", func(w http.ResponseWriter, r *http.Request) {
		body, ok := decode(w, r)
		if !ok {
			return
		}
		fs, score := frames(body), 0.95
		distinct := map[string]bool{}
		for _, f := range fs {
			distinct[f] = true
		}
		if len(fs) < 2 || len(distinct) < 2 {
			score = 0.1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"score": score})
	})
	mux.HandleFunc("/v1/challenge", func(w http.ResponseWriter, r *http.Request) {
		body, ok := decode(w, r)
		if !ok {
			return
		}
		res := map[string]bool{}
		actions, _ := body["actions"].([]any)
		for _, a := range actions {
			if s, ok := a.(string); ok {
				res[s] = true
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": res})
	})
	mux.HandleFunc("/v1/embed", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := decode(w, r); ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{1, 0, 0}})
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("stub faceworker: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }
}
