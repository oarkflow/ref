package platform

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/ref/platform/spi"
)

type jsonPhoneNumber struct {
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

type jsonProvider struct {
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





type jsonUser struct {
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

// TestDatasetIntegrity verifies that both CSV and JSON representations of
// phone numbers, providers, and users are valid and consistent.
func TestDatasetIntegrity(t *testing.T) {
	dataDir := filepath.Join("..", "examples", "sms_routing", "data")

	// 1. Phone Numbers
	pnJSONData, err := os.ReadFile(filepath.Join(dataDir, "phone_numbers.json"))
	if err != nil {
		t.Fatalf("failed to read phone_numbers.json: %v", err)
	}
	var phoneNumbers []jsonPhoneNumber
	if err := json.Unmarshal(pnJSONData, &phoneNumbers); err != nil {
		t.Fatalf("failed to parse phone_numbers.json: %v", err)
	}
	if len(phoneNumbers) < 40 {
		t.Fatalf("expected at least 40 phone numbers, got %d", len(phoneNumbers))
	}

	pnCSVFile, err := os.Open(filepath.Join(dataDir, "phone_numbers.csv"))
	if err != nil {
		t.Fatalf("failed to read phone_numbers.csv: %v", err)
	}
	defer pnCSVFile.Close()
	pnCSVRecords, err := csv.NewReader(pnCSVFile).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse phone_numbers.csv: %v", err)
	}
	// Header + data rows
	if len(pnCSVRecords)-1 != len(phoneNumbers) {
		t.Fatalf("mismatch count: JSON has %d numbers, CSV has %d", len(phoneNumbers), len(pnCSVRecords)-1)
	}

	// 2. Providers
	provJSONData, err := os.ReadFile(filepath.Join(dataDir, "providers.json"))
	if err != nil {
		t.Fatalf("failed to read providers.json: %v", err)
	}
	var providers []jsonProvider
	if err := json.Unmarshal(provJSONData, &providers); err != nil {
		t.Fatalf("failed to parse providers.json: %v", err)
	}
	if len(providers) < 15 {
		t.Fatalf("expected at least 15 providers, got %d", len(providers))
	}

	provCSVFile, err := os.Open(filepath.Join(dataDir, "providers.csv"))
	if err != nil {
		t.Fatalf("failed to read providers.csv: %v", err)
	}
	defer provCSVFile.Close()
	provCSVRecords, err := csv.NewReader(provCSVFile).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse providers.csv: %v", err)
	}
	if len(provCSVRecords)-1 != len(providers) {
		t.Fatalf("mismatch count: JSON has %d providers, CSV has %d", len(providers), len(provCSVRecords)-1)
	}

	// 3. Users
	userJSONData, err := os.ReadFile(filepath.Join(dataDir, "users.json"))
	if err != nil {
		t.Fatalf("failed to read users.json: %v", err)
	}
	var users []jsonUser
	if err := json.Unmarshal(userJSONData, &users); err != nil {
		t.Fatalf("failed to parse users.json: %v", err)
	}
	if len(users) < 40 {
		t.Fatalf("expected at least 40 users, got %d", len(users))
	}

	userCSVFile, err := os.Open(filepath.Join(dataDir, "users.csv"))
	if err != nil {
		t.Fatalf("failed to read users.csv: %v", err)
	}
	defer userCSVFile.Close()
	userCSVRecords, err := csv.NewReader(userCSVFile).ReadAll()
	if err != nil {
		t.Fatalf("failed to parse users.csv: %v", err)
	}
	if len(userCSVRecords)-1 != len(users) {
		t.Fatalf("mismatch count: JSON has %d users, CSV has %d", len(users), len(userCSVRecords)-1)
	}

	t.Logf("Datasets verified successfully: %d phone numbers, %d providers, %d users",
		len(phoneNumbers), len(providers), len(users))
}

// TestSMSRoutingMatrix verifies the full parameter matrix against the BCL decision table rules.
func TestSMSRoutingMatrix(t *testing.T) {
	rulesPath := filepath.Join("..", "examples", "sms_routing", "bcl", "sms_routing_rules.bcl")
	program, err := bcl.CompileDecisionFile(rulesPath, &bcl.Options{AllowTime: true})
	if err != nil {
		t.Fatalf("failed to compile sms_routing_rules.bcl: %v", err)
	}


	// Load providers from dataset
	provJSONData, err := os.ReadFile(filepath.Join("..", "examples", "sms_routing", "data", "providers.json"))
	if err != nil {
		t.Fatalf("failed to read providers.json: %v", err)
	}
	var providerDefs []jsonProvider
	if err := json.Unmarshal(provJSONData, &providerDefs); err != nil {
		t.Fatalf("failed to parse providers.json: %v", err)
	}

	// Instantiate mock providers and router map
	mockMap := make(map[string]*mockSMSProvider)
	routerMap := make(map[string]*routerProvider)
	for _, p := range providerDefs {
		mp := &mockSMSProvider{id: p.ID}
		mockMap[p.ID] = mp
		routerMap[p.ID] = &routerProvider{
			name:     p.ID,
			provider: mp,
			health:   mp,
			priority: p.Priority,
			tier:     p.Tier,
			cost:     p.CostPerSMS,
			enabled:  true,
		}
	}

	router := &SMSRouter{
		name: "gateway",
		cfg: SMSRouterConfig{
			RulesDecision: "provider_chain",
			Failover:      true,
			MaxRetries:    3,
		},
		providers:   routerMap,
		ruleProgram: program,
	}

	ctx := context.Background()

	// 1. Matrix: Nepal combinations
	t.Run("NepalMatrix", func(t *testing.T) {
		// NTC OTP -> carrier_ntc_smpp
		res, err := router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+9779841234567", Text: "Your OTP is 1234"}, map[string]any{
			"country":       "NP",
			"mno":           "Nepal Telecom (NTC)",
			"traffic_type":  "otp",
			"customer_tier": "vip",
		})
		if err != nil || !res.OK || res.ProviderID != "carrier_ntc_smpp" {
			t.Fatalf("expected carrier_ntc_smpp, got: %s (err: %v)", res.ProviderID, err)
		}

		// Ncell OTP -> carrier_ncell_smpp
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+9779801234567", Text: "Your OTP is 5678"}, map[string]any{
			"country":       "NP",
			"mno":           "Ncell",
			"traffic_type":  "otp",
			"customer_tier": "vip",
		})
		if err != nil || !res.OK || res.ProviderID != "carrier_ncell_smpp" {
			t.Fatalf("expected carrier_ncell_smpp, got: %s (err: %v)", res.ProviderID, err)
		}

		// Nepal Marketing -> sparrow_sms_http
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+9779841234567", Text: "50% off today!"}, map[string]any{
			"country":       "NP",
			"traffic_type":  "marketing",
			"customer_tier": "starter",
		})
		if err != nil || !res.OK || res.ProviderID != "sparrow_sms_http" {
			t.Fatalf("expected sparrow_sms_http for bulk marketing, got: %s", res.ProviderID)
		}

		// Nepal NTC Failover -> carrier_ncell_smpp
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+9779841234567", Text: "Alert"}, map[string]any{
			"country":         "NP",
			"provider_failed": "carrier_ntc_smpp",
		})
		if err != nil || !res.OK || res.ProviderID != "carrier_ncell_smpp" {
			t.Fatalf("expected carrier_ncell_smpp on ntc failover, got: %s", res.ProviderID)
		}
	})

	// 2. Matrix: India combinations
	t.Run("IndiaMatrix", func(t *testing.T) {
		// Airtel OTP -> airtel_direct_smpp
		res, err := router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+919876543210", Text: "HDFC OTP 9988"}, map[string]any{
			"country":       "IN",
			"mno":           "Airtel",
			"traffic_type":  "otp",
			"customer_tier": "vip",
		})
		if err != nil || !res.OK || res.ProviderID != "airtel_direct_smpp" {
			t.Fatalf("expected airtel_direct_smpp, got: %s", res.ProviderID)
		}

		// Jio OTP -> jio_direct_smpp
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+919823012345", Text: "Jio OTP 4433"}, map[string]any{
			"country":       "IN",
			"mno":           "Jio",
			"traffic_type":  "otp",
			"customer_tier": "enterprise",
		})
		if err != nil || !res.OK || res.ProviderID != "jio_direct_smpp" {
			t.Fatalf("expected jio_direct_smpp, got: %s", res.ProviderID)
		}

		// India Marketing -> route_mobile_http
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+919876543210", Text: "Mega Sale Flipkart"}, map[string]any{
			"country":       "IN",
			"traffic_type":  "marketing",
			"customer_tier": "enterprise",
		})
		if err != nil || !res.OK || res.ProviderID != "route_mobile_http" {
			t.Fatalf("expected route_mobile_http, got: %s", res.ProviderID)
		}

		// India Airtel Failover -> jio_direct_smpp
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+919876543210", Text: "Alert"}, map[string]any{
			"country":         "IN",
			"provider_failed": "airtel_direct_smpp",
		})
		if err != nil || !res.OK || res.ProviderID != "jio_direct_smpp" {
			t.Fatalf("expected jio_direct_smpp on airtel failover, got: %s", res.ProviderID)
		}
	})

	// 3. Matrix: North America combinations
	t.Run("NorthAmericaMatrix", func(t *testing.T) {
		// US VIP OTP -> twilio_us_direct
		res, err := router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+14155552671", Text: "Stripe Auth 6655"}, map[string]any{
			"country":       "US",
			"traffic_type":  "otp",
			"customer_tier": "vip",
		})
		if err != nil || !res.OK || res.ProviderID != "twilio_us_direct" {
			t.Fatalf("expected twilio_us_direct, got: %s", res.ProviderID)
		}

		// US Marketing -> bandwidth_us
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+14155552671", Text: "Promo Discount"}, map[string]any{
			"country":       "US",
			"traffic_type":  "marketing",
			"customer_tier": "business",
		})
		if err != nil || !res.OK || res.ProviderID != "bandwidth_us" {
			t.Fatalf("expected bandwidth_us, got: %s", res.ProviderID)
		}

		// US Transactional -> aws_sns_us
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+14155552671", Text: "Uber Ride Arriving"}, map[string]any{
			"country":       "US",
			"traffic_type":  "transactional",
			"customer_tier": "enterprise",
		})
		if err != nil || !res.OK || res.ProviderID != "aws_sns_us" {
			t.Fatalf("expected aws_sns_us, got: %s", res.ProviderID)
		}

		// US Failover -> aws_sns_us
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+14155552671", Text: "Alert"}, map[string]any{
			"country":         "US",
			"provider_failed": "twilio_us_direct",
		})
		if err != nil || !res.OK || res.ProviderID != "aws_sns_us" {
			t.Fatalf("expected aws_sns_us on twilio failover, got: %s", res.ProviderID)
		}
	})

	// 4. Matrix: International Regions (UK, Europe, APAC, MENA, LATAM, Africa)
	t.Run("InternationalRegionsMatrix", func(t *testing.T) {
		// UK VIP -> bt_direct_smpp
		res, err := router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+447911123456", Text: "Revolut 2FA"}, map[string]any{
			"country":       "GB",
			"traffic_type":  "otp",
			"customer_tier": "vip",
		})
		if err != nil || !res.OK || res.ProviderID != "bt_direct_smpp" {
			t.Fatalf("expected bt_direct_smpp, got: %s", res.ProviderID)
		}


		// Europe General (DE) -> messagebird_eu
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+4915123456789", Text: "N26 Verification"}, map[string]any{
			"country": "DE",
		})
		if err != nil || !res.OK || res.ProviderID != "messagebird_eu" {
			t.Fatalf("expected messagebird_eu, got: %s", res.ProviderID)
		}

		// Europe Marketing (FR) -> sinch_eu_http
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+33612345678", Text: "Promo Solde"}, map[string]any{
			"country":      "FR",
			"traffic_type": "marketing",
		})
		if err != nil || !res.OK || res.ProviderID != "sinch_eu_http" {
			t.Fatalf("expected sinch_eu_http, got: %s", res.ProviderID)
		}

		// MENA (AE) -> etisalat_mena_smpp
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+971501234567", Text: "Emirates NBD OTP"}, map[string]any{
			"country": "AE",
		})
		if err != nil || !res.OK || res.ProviderID != "etisalat_mena_smpp" {
			t.Fatalf("expected etisalat_mena_smpp, got: %s", res.ProviderID)
		}

		// LATAM (BR) -> zenvia_latam_http
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+5511987654321", Text: "Nubank Codigo"}, map[string]any{
			"country": "BR",
		})
		if err != nil || !res.OK || res.ProviderID != "zenvia_latam_http" {
			t.Fatalf("expected zenvia_latam_http, got: %s", res.ProviderID)
		}

		// Africa (NG) -> africastalking_http
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+2348031234567", Text: "Flutterwave Pin"}, map[string]any{
			"country": "NG",
		})
		if err != nil || !res.OK || res.ProviderID != "africastalking_http" {
			t.Fatalf("expected africastalking_http, got: %s", res.ProviderID)
		}

		// APAC (SG) -> singtel_apac_smpp
		res, err = router.RouteAndSubmit(ctx, spi.SMSMessage{To: "+6591234567", Text: "Grab Pin"}, map[string]any{
			"country": "SG",
		})
		if err != nil || !res.OK || res.ProviderID != "singtel_apac_smpp" {
			t.Fatalf("expected singtel_apac_smpp, got: %s", res.ProviderID)
		}
	})

	// 5. Matrix: End-to-End Across All Users in users.json
	t.Run("AllUsersDatasetMatrix", func(t *testing.T) {
		userJSONData, err := os.ReadFile(filepath.Join("..", "examples", "sms_routing", "data", "users.json"))
		if err != nil {
			t.Fatalf("failed to read users.json: %v", err)
		}
		var users []jsonUser
		if err := json.Unmarshal(userJSONData, &users); err != nil {
			t.Fatalf("failed to parse users.json: %v", err)
		}

		for _, u := range users {
			res, err := router.RouteAndSubmit(ctx, spi.SMSMessage{
				From: u.SenderID,
				To:   u.PhoneRaw,
				Text: "Hello " + u.Name + ", this is a verified routing test.",
			}, map[string]any{
				"country":       u.CountryCode,
				"customer_tier": u.CustomerTier,
				"traffic_type":  u.TrafficType,
				"encoding":      u.PreferredEncoding,
				"max_cost":      u.MaxCostPerSMS,
			})
			if err != nil {
				t.Fatalf("user %s (%s) routing error: %v", u.UserID, u.Name, err)
			}
			if !res.OK {
				t.Fatalf("user %s (%s) failed to route: %+v", u.UserID, u.Name, res.Error)
			}
			if res.ProviderID == "" {
				t.Fatalf("user %s (%s) returned empty provider ID", u.UserID, u.Name)
			}
		}
		t.Logf("Successfully routed test messages for all %d users across all parameter combinations!", len(users))
	})

	// 6. Max Cost Limit Filter Matrix
	t.Run("MaxCostFilter", func(t *testing.T) {
		// When max_cost is set to 0.005 in India:
		// airtel is 0.005, jio is 0.0048, but route_mobile is 0.0062.
		// Route mobile should be filtered out if max_cost is 0.005.
		candidates := router.selectAndRankCandidates(ctx, spi.SMSMessage{
			To:   "+919876543210",
			Text: "Cheap SMS",
		}, map[string]any{
			"country":  "IN",
			"max_cost": 0.005,
		})
		for _, c := range candidates {
			if c.cost > 0.005 {
				t.Fatalf("candidate %s has cost %f exceeding max_cost 0.005", c.name, c.cost)
			}
		}
	})

	// 7. Full Action Pipeline Integration (sms.send node)
	t.Run("SMSSendActionIntegration", func(t *testing.T) {
		bctx := BuildContext{
			Resources: map[string]Resource{
				"gateway": router,
			},
		}
		spec := NodeSpec{
			Name:     "send_sms",
			Resource: "gateway",
			Provides: []string{"sent"},
			Config: map[string]any{
				"to_fact":            "message.to",
				"text_fact":          "message.text",
				"country_fact":       "message.country",
				"mno_fact":           "message.mno",
				"traffic_type_fact":  "message.traffic_type",
				"customer_tier_fact": "message.customer_tier",
				"capture":            true,
			},
		}
		action, err := smsSendAction.Build(bctx, spec)
		if err != nil {
			t.Fatalf("buildSMSSendAction failed: %v", err)
		}

		actRes, err := action.Run(&ActionContext{
			Context: ctx,
			Inputs: map[string]any{
				"message": map[string]any{
					"to":            "+9779841234567",
					"text":          "Action pipeline OTP test",
					"country":       "NP",
					"mno":           "Nepal Telecom (NTC)",
					"traffic_type":  "otp",
					"customer_tier": "vip",
				},
			},
		})
		if err != nil {
			t.Fatalf("action.Run returned error: %v", err)
		}
		sentMap, ok := actRes.Outputs["sent"].(map[string]any)
		if !ok {
			t.Fatalf("expected sent output map, got: %#v", actRes.Outputs["sent"])
		}
		if sentMap["provider"] != "carrier_ntc_smpp" {
			t.Fatalf("expected provider carrier_ntc_smpp, got: %v", sentMap["provider"])
		}
		if sentMap["ok"] != true {
			t.Fatalf("expected ok true, got: %v", sentMap["ok"])
		}
	})
}
