package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/ref/platform"
	"github.com/oarkflow/ref/platform/spi"
)

type ProviderDef struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Protocol        string   `json:"protocol"`
	CountryCoverage []string `json:"country_coverage"`
	CarrierMNO      []string `json:"carrier_mno"`
	Tier            int      `json:"tier"`
	Priority        int      `json:"priority"`
	CostPerSMS      float64  `json:"cost_per_sms"`
	TPSLimit        int      `json:"tps_limit"`
	Features        []string `json:"features"`
	HealthStatus    string   `json:"health_status"`
	SuccessRate     float64  `json:"success_rate"`
	AvgLatencyMs    int64    `json:"avg_latency_ms"`
}

type UserProfile struct {
	UserID            string  `json:"user_id"`
	Name              string  `json:"name"`
	Email             string  `json:"email"`
	PhoneRaw          string  `json:"phone_raw"`
	CountryCode       string  `json:"country_code"`
	CustomerTier      string  `json:"customer_tier"`
	TrafficType       string  `json:"traffic_type"`
	PreferredEncoding string  `json:"preferred_encoding"`
	MaxCostPerSMS     float64 `json:"max_cost_per_sms"`
	MonthlyVolumeEst  int     `json:"monthly_volume_est"`
	SenderID          string  `json:"sender_id"`
	Status            string  `json:"status"`
}

type PhoneNumberDef struct {
	ID           string `json:"id"`
	Raw          string `json:"raw"`
	CountryCode  string `json:"country_code"`
	CountryName  string `json:"country_name"`
	Region       string `json:"region"`
	ExpectedE164 string `json:"expected_e164"`
	MNO          string `json:"mno"`
	Type         string `json:"type"`
	Valid        bool   `json:"valid"`
}

type SimulatedProvider struct {
	id          string
	name        string
	latency     time.Duration
	successRate float64
}

func (s *SimulatedProvider) ProviderID() string { return s.id }

func (s *SimulatedProvider) Submit(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
	time.Sleep(s.latency)
	return spi.SMSResult{
		OK:            true,
		ProviderMsgID: fmt.Sprintf("sim_%s_%d", s.id, time.Now().UnixNano()),
		ProviderID:    s.id,
		Segments:      1,
		LatencyMs:     s.latency.Milliseconds(),
		Attempts:      1,
	}, nil
}

func (s *SimulatedProvider) Health(ctx context.Context) spi.SMSHealthReport {
	return spi.SMSHealthReport{
		Available:   true,
		SuccessRate: s.successRate,
		OpenWindow:  10,
	}
}

type AppEnv struct {
	DataDir      string
	BCLDir       string
	Providers    []ProviderDef
	Users        []UserProfile
	PhoneNumbers []PhoneNumberDef
	Router       *platform.SMSRouter
	RuleProgram  *bcl.DecisionProgram
}

func loadAppEnv(baseDir string) (*AppEnv, error) {
	dataDir := filepath.Join(baseDir, "data")
	bclDir := filepath.Join(baseDir, "bcl")

	// 1. Load Rules Program
	rulesPath := filepath.Join(bclDir, "sms_routing_rules.bcl")
	prog, err := bcl.CompileDecisionFile(rulesPath, &bcl.Options{AllowTime: true})
	if err != nil {
		return nil, fmt.Errorf("compile rules from %s: %w", rulesPath, err)
	}


	// 2. Load Datasets
	provBytes, err := os.ReadFile(filepath.Join(dataDir, "providers.json"))
	if err != nil {
		return nil, fmt.Errorf("read providers.json: %w", err)
	}
	var providers []ProviderDef
	if err := json.Unmarshal(provBytes, &providers); err != nil {
		return nil, fmt.Errorf("parse providers.json: %w", err)
	}

	userBytes, err := os.ReadFile(filepath.Join(dataDir, "users.json"))
	if err != nil {
		return nil, fmt.Errorf("read users.json: %w", err)
	}
	var users []UserProfile
	if err := json.Unmarshal(userBytes, &users); err != nil {
		return nil, fmt.Errorf("parse users.json: %w", err)
	}

	pnBytes, err := os.ReadFile(filepath.Join(dataDir, "phone_numbers.json"))
	if err != nil {
		return nil, fmt.Errorf("read phone_numbers.json: %w", err)
	}
	var phoneNumbers []PhoneNumberDef
	if err := json.Unmarshal(pnBytes, &phoneNumbers); err != nil {
		return nil, fmt.Errorf("parse phone_numbers.json: %w", err)
	}

	// 3. Build Router Providers Map
	pSpecs := make([]platform.SMSRouterProviderConfig, len(providers))
	resMap := make(map[string]any)

	for i, p := range providers {
		latency := time.Duration(p.AvgLatencyMs) * time.Millisecond
		if latency == 0 {
			latency = 20 * time.Millisecond
		}
		sr := p.SuccessRate
		if sr == 0 {
			sr = 0.99
		}
		simProv := &SimulatedProvider{
			id:          p.ID,
			name:        p.Name,
			latency:     latency,
			successRate: sr,
		}
		resMap[p.ID] = simProv
		pSpecs[i] = platform.SMSRouterProviderConfig{
			Name:     p.ID,
			Resource: p.ID,
			Priority: p.Priority,
			Tier:     p.Tier,
			Cost:     p.CostPerSMS,
			Enabled:  true,
		}
	}

	routerCfg := platform.SMSRouterConfig{
		Providers:        pSpecs,
		RoutingRules:     "sms_routing",
		RulesDecision:    "provider_chain",
		Failover:         true,
		MaxRetries:       3,
		RetryBudget:      50 * time.Millisecond,
		MinSuccessRate:   0.80,
	}

	provMap := make(map[string]spi.SMSProvider)
	for k, v := range resMap {
		if sp, ok := v.(spi.SMSProvider); ok {
			provMap[k] = sp
		}
	}

	routerRes := platform.NewSMSRouter("gateway", routerCfg, provMap, prog)

	return &AppEnv{
		DataDir:      dataDir,
		BCLDir:       bclDir,
		Providers:    providers,
		Users:        users,
		PhoneNumbers: phoneNumbers,
		Router:       routerRes,
		RuleProgram:  prog,
	}, nil

}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	baseDir := filepath.Dir(os.Args[0])
	// If run with go run, look in current directory or examples/sms_routing
	if _, err := os.Stat("data/providers.json"); err == nil {
		baseDir = "."
	} else if _, err := os.Stat("examples/sms_routing/data/providers.json"); err == nil {
		baseDir = "examples/sms_routing"
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	env, err := loadAppEnv(baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing environment: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "server":
		runServer(env, args)
	case "send":
		runSend(env, args)
	case "simulate":
		runSimulate(env, args)
	case "batch":
		runBatch(env, args)
	case "failover":
		runFailover(env, args)
	case "providers":
		runListProviders(env)
	case "users":
		runListUsers(env)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q. Run 'help' for usage.\n", cmd)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`REF SMS Multi-Provider Parameter Routing Platform

Usage:
  go run ./examples/sms_routing/main.go <command> [arguments]

Commands:
  server     Start HTTP REST API server with interactive endpoints
  send       Dispatch a single SMS with routing parameters via CLI
  simulate   Dry-run parameter routing and display chosen provider and score
  batch      Execute routing across all users or phone numbers in dataset
  failover   Simulate a primary provider outage and verify dynamic failover
  providers  Display table of all 18 configured SMS gateways with metrics
  users      Display table of customer profiles and routing requirements

Run '<command> -h' for command-specific flags and options.`)
}

// ----------------------------------------------------------------------------
// 1. Server Command (HTTP REST API)
// ----------------------------------------------------------------------------

func runServer(env *AppEnv, args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "Listen address for HTTP REST API")
	_ = fs.Parse(args)

	mux := http.NewServeMux()

	// POST /api/sms/send
	mux.HandleFunc("/api/sms/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			To             string  `json:"to"`
			From           string  `json:"from"`
			Text           string  `json:"text"`
			Country        string  `json:"country"`
			MNO            string  `json:"mno"`
			CustomerTier   string  `json:"customer_tier"`
			TrafficType    string  `json:"traffic_type"`
			Encoding       string  `json:"encoding"`
			ProviderFailed string  `json:"provider_failed"`
			MaxCost        float64 `json:"max_cost"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("invalid JSON payload: %v", err), http.StatusBadRequest)
			return
		}

		hints := map[string]any{
			"country":         req.Country,
			"mno":             req.MNO,
			"customer_tier":   req.CustomerTier,
			"traffic_type":    req.TrafficType,
			"encoding":        req.Encoding,
			"provider_failed": req.ProviderFailed,
			"max_cost":        req.MaxCost,
		}

		res, err := env.Router.RouteAndSubmit(r.Context(), spi.SMSMessage{
			To:   req.To,
			From: req.From,
			Text: req.Text,
		}, hints)
		if err != nil {
			http.Error(w, fmt.Sprintf("submission error: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})

	// POST /api/sms/simulate
	mux.HandleFunc("/api/sms/simulate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var hints map[string]any
		if err := json.NewDecoder(r.Body).Decode(&hints); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		toStr, _ := hints["to"].(string)
		if toStr == "" {
			toStr = "+15551234567"
		}
		candidates := env.Router.SelectAndRankCandidates(r.Context(), spi.SMSMessage{To: toStr}, hints)

		type candidateOutput struct {
			Rank       int     `json:"rank"`
			ProviderID string  `json:"provider_id"`
			Tier       int     `json:"tier"`
			Priority   int     `json:"priority"`
			Cost       float64 `json:"cost_per_sms"`
		}


		var out []candidateOutput
		for i, c := range candidates {
			out = append(out, candidateOutput{
				Rank:       i + 1,
				ProviderID: c.Name,
				Tier:       c.Tier,
				Priority:   c.Priority,
				Cost:       c.Cost,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"selected_provider": out[0].ProviderID,
			"candidate_chain":   out,
			"hints_evaluated":   hints,
		})
	})

	// GET /api/sms/providers
	mux.HandleFunc("/api/sms/providers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env.Providers)
	})

	// GET /api/sms/users
	mux.HandleFunc("/api/sms/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env.Users)
	})

	// GET /api/sms/phone-numbers
	mux.HandleFunc("/api/sms/phone-numbers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env.PhoneNumbers)
	})

	// GET /api/sms/stats
	mux.HandleFunc("/api/sms/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(env.Router.Metrics())
	})

	// POST /api/sms/dlr
	mux.HandleFunc("/api/sms/dlr", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ProviderID string `json:"provider_id"`
			Status     string `json:"status"`
			LatencyMs  int64  `json:"latency_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		env.Router.RecordDLR(r.Context(), req.ProviderID, req.Status, req.LatencyMs)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "recorded": req})
	})

	fmt.Printf("\n🚀 REF SMS Routing Platform HTTP Server listening on %s\n", *addr)
	fmt.Println("Interactive Endpoints Available:")
	fmt.Println("  POST /api/sms/send         - Route and dispatch an SMS")
	fmt.Println("  POST /api/sms/simulate     - Dry-run inspect provider scoring and decision")
	fmt.Println("  GET  /api/sms/providers    - Catalog of all 18 gateways and coverage")
	fmt.Println("  GET  /api/sms/users        - Large user dataset (47 profiles)")
	fmt.Println("  GET  /api/sms/phone-numbers- Multi-format phone number dataset (42 records)")
	fmt.Println("  GET  /api/sms/stats        - Real-time router throughput and failover stats")
	fmt.Println("  POST /api/sms/dlr          - Ingest delivery receipt feedback")
	fmt.Println("\nPress Ctrl+C to terminate server.")

	server := &http.Server{Addr: *addr, Handler: mux}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
	}
}

// ----------------------------------------------------------------------------
// 2. Send Command (CLI Dispatch)
// ----------------------------------------------------------------------------

func runSend(env *AppEnv, args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "+9779841234567", "Destination phone number")
	from := fs.String("from", "REF_GATEWAY", "Sender ID")
	text := fs.String("text", "Your verification OTP code is 884219.", "Message body text")
	country := fs.String("country", "NP", "ISO 2-letter country code (NP, IN, US, GB, etc.)")
	mno := fs.String("mno", "Nepal Telecom (NTC)", "Carrier / Mobile Network Operator")
	tier := fs.String("tier", "vip", "Customer tier (vip, enterprise, business, starter)")
	trafficType := fs.String("type", "otp", "Traffic type (otp, critical_alert, transactional, marketing)")
	encoding := fs.String("encoding", "gsm7", "Encoding (gsm7, ucs2)")
	providerFailed := fs.String("failed", "", "Simulate failure of specified primary provider for failover")
	maxCost := fs.Float64("max-cost", 0.0, "Cost limit ceiling in USD")
	_ = fs.Parse(args)

	hints := map[string]any{
		"country":         *country,
		"mno":             *mno,
		"customer_tier":   *tier,
		"traffic_type":    *trafficType,
		"encoding":        *encoding,
		"provider_failed": *providerFailed,
		"max_cost":        *maxCost,
	}

	fmt.Printf("\n📨 Submitting Message via Parameter Router...\n")
	fmt.Printf("   Destination : %s\n", *to)
	fmt.Printf("   Parameters  : country=%s, mno=%q, tier=%s, type=%s\n", *country, *mno, *tier, *trafficType)
	if *providerFailed != "" {
		fmt.Printf("   Failover Drill: Primary provider %q marked as failed\n", *providerFailed)
	}

	start := time.Now()
	res, err := env.Router.RouteAndSubmit(context.Background(), spi.SMSMessage{
		To:   *to,
		From: *from,
		Text: *text,
	}, hints)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Submission Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n✅ SMS Dispatched Successfully!\n")
	fmt.Printf("   Selected Gateway   : %s\n", res.ProviderID)
	fmt.Printf("   Provider Msg ID    : %s\n", res.ProviderMsgID)
	fmt.Printf("   Attempts / Retries : %d\n", res.Attempts)
	fmt.Printf("   Reported Latency   : %d ms (total: %v)\n", res.LatencyMs, elapsed.Round(time.Millisecond))
	fmt.Printf("   Segments           : %d\n", res.Segments)
	fmt.Printf("   Status             : %s\n\n", map[bool]string{true: "DELIVERED / ACCEPTED", false: "FAILED"}[res.OK])
}

// ----------------------------------------------------------------------------
// 3. Simulate Command (Dry Run Inspection)
// ----------------------------------------------------------------------------

func runSimulate(env *AppEnv, args []string) {
	fs := flag.NewFlagSet("simulate", flag.ExitOnError)
	country := fs.String("country", "NP", "Country code")
	mno := fs.String("mno", "", "Carrier MNO")
	tier := fs.String("tier", "vip", "Customer tier")
	trafficType := fs.String("type", "otp", "Traffic type")
	failed := fs.String("failed", "", "Provider failed for failover inspection")
	_ = fs.Parse(args)

	hints := map[string]any{
		"country":         *country,
		"mno":             *mno,
		"customer_tier":   *tier,
		"traffic_type":    *trafficType,
		"provider_failed": *failed,
	}

	candidates := env.Router.SelectAndRankCandidates(context.Background(), spi.SMSMessage{To: "+15551234567"}, hints)

	fmt.Printf("\n🔍 Simulation Results for Parameters: %v\n\n", hints)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RANK\tPROVIDER ID\tTIER\tPRIORITY\tCOST/SMS\tREASON")
	fmt.Fprintln(w, "----\t-----------\t----\t--------\t--------\t------")

	for i, c := range candidates {
		reason := "Catalog rank"
		if i == 0 {
			reason = "🎯 Selected Primary Route"
		} else if i == 1 {
			reason = "1st Failover Backup"
		} else if i == 2 {
			reason = "2nd Failover Backup"
		}
		fmt.Fprintf(w, "#%d\t%s\tTier %d\t%d\t$%.4f\t%s\n", i+1, c.Name, c.Tier, c.Priority, c.Cost, reason)
		if i >= 4 {
			break
		}
	}
	_ = w.Flush()
	fmt.Println()
}

// ----------------------------------------------------------------------------
// 4. Batch Command (Dataset Batch Run)
// ----------------------------------------------------------------------------

func runBatch(env *AppEnv, args []string) {
	fs := flag.NewFlagSet("batch", flag.ExitOnError)
	dataset := fs.String("dataset", "users", "Dataset to run batch over: 'users' or 'numbers'")
	_ = fs.Parse(args)

	ctx := context.Background()

	if *dataset == "users" {
		fmt.Printf("\n📦 Running batch dispatch across %d users in dataset...\n\n", len(env.Users))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "USER ID\tNAME\tCOUNTRY\tTYPE\tTIER\tSELECTED GATEWAY\tSTATUS")
		fmt.Fprintln(w, "-------\t----\t-------\t----\t----\t----------------\t------")

		successCount := 0
		for _, u := range env.Users {
			hints := map[string]any{
				"country":       u.CountryCode,
				"customer_tier": u.CustomerTier,
				"traffic_type":  u.TrafficType,
				"encoding":      u.PreferredEncoding,
				"max_cost":      u.MaxCostPerSMS,
			}
			res, err := env.Router.RouteAndSubmit(ctx, spi.SMSMessage{
				From: u.SenderID,
				To:   u.PhoneRaw,
				Text: fmt.Sprintf("Batch verification test for %s", u.Name),
			}, hints)

			status := "OK"
			if err != nil || !res.OK {
				status = "FAIL"
			} else {
				successCount++
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				u.UserID, truncate(u.Name, 20), u.CountryCode, u.TrafficType, u.CustomerTier, res.ProviderID, status)
		}
		_ = w.Flush()
		fmt.Printf("\nBatch Finished: %d / %d messages routed successfully.\n\n", successCount, len(env.Users))

	} else {
		fmt.Printf("\n📦 Running batch dispatch across %d phone numbers in dataset...\n\n", len(env.PhoneNumbers))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tRAW PHONE\tCOUNTRY\tMNO\tSELECTED GATEWAY\tSTATUS")
		fmt.Fprintln(w, "--\t---------\t-------\t---\t----------------\t------")

		successCount := 0
		for _, pn := range env.PhoneNumbers {
			if !pn.Valid {
				continue
			}
			hints := map[string]any{
				"country":      pn.CountryCode,
				"mno":          pn.MNO,
				"traffic_type": "otp",
			}
			res, err := env.Router.RouteAndSubmit(ctx, spi.SMSMessage{
				To:   pn.Raw,
				Text: "Test Number",
			}, hints)
			status := "OK"
			if err != nil || !res.OK {
				status = "FAIL"
			} else {
				successCount++
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				pn.ID, pn.Raw, pn.CountryCode, truncate(pn.MNO, 22), res.ProviderID, status)
		}
		_ = w.Flush()
		fmt.Printf("\nBatch Finished: %d numbers routed successfully.\n\n", successCount)
	}
}

// ----------------------------------------------------------------------------
// 5. Failover Command (Drill Outage Simulation)
// ----------------------------------------------------------------------------

func runFailover(env *AppEnv, args []string) {
	fs := flag.NewFlagSet("failover", flag.ExitOnError)
	country := fs.String("country", "NP", "Country to test failover in (NP, IN, US, GB, etc.)")
	failed := fs.String("failed", "carrier_ntc_smpp", "Provider to mark as failed")
	_ = fs.Parse(args)

	fmt.Printf("\n⚡ Executing Outage & Automatic Failover Drill...\n")
	fmt.Printf("   Country Target  : %s\n", *country)
	fmt.Printf("   Outage Provider : %s\n\n", *failed)

	// Step 1: Normal route
	normalRes, _ := env.Router.RouteAndSubmit(context.Background(), spi.SMSMessage{
		To:   "+9779841234567",
		Text: "Pre-outage message",
	}, map[string]any{
		"country":      *country,
		"traffic_type": "otp",
	})
	fmt.Printf("1. NORMAL OPERATION : Routed to %q\n", normalRes.ProviderID)

	// Step 2: Outage route
	failoverRes, _ := env.Router.RouteAndSubmit(context.Background(), spi.SMSMessage{
		To:   "+9779841234567",
		Text: "Post-outage message",
	}, map[string]any{
		"country":         *country,
		"traffic_type":    "otp",
		"provider_failed": *failed,
	})
	fmt.Printf("2. UNDER OUTAGE     : Automatically failed over to %q (zero caller error!)\n\n", failoverRes.ProviderID)
}

// ----------------------------------------------------------------------------
// 6. Providers & Users Listing
// ----------------------------------------------------------------------------

func runListProviders(env *AppEnv) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROVIDER ID\tPROTOCOL\tTIER\tPRIORITY\tCOST/SMS\tCOVERAGE\tSTATUS")
	fmt.Fprintln(w, "-----------\t--------\t----\t--------\t--------\t--------\t------")
	for _, p := range env.Providers {
		fmt.Fprintf(w, "%s\t%s\tTier %d\t%d\t$%.4f\t%s\t%s\n",
			p.ID, p.Protocol, p.Tier, p.Priority, p.CostPerSMS, strings.Join(p.CountryCoverage, ","), p.HealthStatus)
	}
	_ = w.Flush()
	fmt.Println()
}

func runListUsers(env *AppEnv) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USER ID\tNAME\tCOUNTRY\tTIER\tTRAFFIC TYPE\tMAX COST\tSTATUS")
	fmt.Fprintln(w, "-------\t----\t-------\t----\t------------\t--------\t------")
	for _, u := range env.Users {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t$%.4f\t%s\n",
			u.UserID, truncate(u.Name, 22), u.CountryCode, u.CustomerTier, u.TrafficType, u.MaxCostPerSMS, u.Status)
	}
	_ = w.Flush()
	fmt.Println()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
