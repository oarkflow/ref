package control_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oarkflow/ref/aegis/attest"
	"github.com/oarkflow/ref/aegis/control"
	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
	"github.com/oarkflow/ref/aegis/runtime"
	"github.com/oarkflow/ref/signing"
)

// inMemoryTransport allows full HTTP testing without requiring OS loopback network sockets.
type inMemoryTransport struct {
	handler http.Handler
}

func (t *inMemoryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func newTestClient(handler http.Handler) *http.Client {
	return &http.Client{
		Transport: &inMemoryTransport{handler: handler},
	}
}

const testBaseURL = "http://aegis-control.local"

func newTestKeySet(t *testing.T) *signing.KeySet {
	t.Helper()
	key, err := signing.GenerateEd25519("control-test-key")
	if err != nil {
		t.Fatalf("failed to generate signing key: %v", err)
	}
	ks, err := signing.NewKeySet(key)
	if err != nil {
		t.Fatalf("failed to create key set: %v", err)
	}
	return ks
}

func newTestMasterKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("failed to read random master key: %v", err)
	}
	return k
}

func newArithmeticModule() *ir.Module {
	return &ir.Module{
		Version: ir.Version,
		Types: []ir.FuncType{
			{
				Params:  []ir.ValType{ir.I32, ir.I32},
				Results: []ir.ValType{ir.I32},
			},
		},
		Functions: []ir.Function{
			{
				Name:      "add",
				TypeIndex: 0,
				Body: []ir.Instruction{
					{Op: ir.OpLocalGet, Operands: [3]uint64{0, 0, 0}},
					{Op: ir.OpLocalGet, Operands: [3]uint64{1, 0, 0}},
					{Op: ir.OpI32Add},
					{Op: ir.OpReturn},
				},
			},
		},
		Exports: []ir.Export{
			{Name: "add", FuncIndex: 0},
		},
		Memory: ir.MemorySpec{InitialPages: 1, MaxPages: 2},
	}
}

// 1. Key derivation uniqueness test
func TestKeyDerivation_Uniqueness(t *testing.T) {
	masterKey := newTestMasterKey(t)
	version := "1.2.0"

	key1, err := control.DeriveSessionKey(masterKey, "session-alpha", version)
	if err != nil {
		t.Fatalf("DeriveSessionKey session-alpha failed: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key1))
	}

	key2, err := control.DeriveSessionKey(masterKey, "session-beta", version)
	if err != nil {
		t.Fatalf("DeriveSessionKey session-beta failed: %v", err)
	}
	if len(key2) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key2))
	}

	// Two different sessions MUST receive different keys
	if bytes.Equal(key1, key2) {
		t.Fatalf("expected unique session keys for different sessions, but got identical keys")
	}

	// Session keys MUST NOT equal master key
	if bytes.Equal(key1, masterKey) || bytes.Equal(key2, masterKey) {
		t.Fatalf("derived session key must never expose master key")
	}

	// Same session ID and version MUST be deterministic
	key1Again, err := control.DeriveSessionKey(masterKey, "session-alpha", version)
	if err != nil {
		t.Fatalf("DeriveSessionKey repeat failed: %v", err)
	}
	if !bytes.Equal(key1, key1Again) {
		t.Fatalf("expected deterministic session key for identical session and version")
	}

	// Different version produces different session key
	key3, err := control.DeriveSessionKey(masterKey, "session-alpha", "1.3.0")
	if err != nil {
		t.Fatalf("DeriveSessionKey version change failed: %v", err)
	}
	if bytes.Equal(key1, key3) {
		t.Fatalf("expected different session key for different package version")
	}
}

// 2. End-to-end HTTP grant request
func TestGrantRequest_EndToEndHTTP(t *testing.T) {
	ctx := context.Background()
	store := control.NewMemoryStore()
	signingKeys := newTestKeySet(t)
	server := control.NewServer(store, control.WithServerSigningKeys(signingKeys))
	client := newTestClient(server)

	packageID := "pkg-e2e-001"
	masterKey := newTestMasterKey(t)

	// Register package and master key
	if err := store.StoreMasterKey(ctx, packageID, masterKey); err != nil {
		t.Fatalf("StoreMasterKey failed: %v", err)
	}
	if err := store.StorePackage(ctx, control.PackageRecord{
		PackageID:     packageID,
		ApplicationID: "app-test",
		Version:       "1.0.0",
	}); err != nil {
		t.Fatalf("StorePackage failed: %v", err)
	}

	// Perform HTTP POST /v1/grant/request
	reqBody := control.GrantRequestPayload{
		GrantRequest: runtime.GrantRequest{
			PackageID:     packageID,
			ApplicationID: "app-test",
			Version:       "1.0.0",
			Platform:      "linux-desktop",
			DeviceID:      "node-device-01",
		},
	}
	reqData, _ := json.Marshal(reqBody)

	resp, err := client.Post(testBaseURL+"/v1/grant/request", "application/json", bytes.NewReader(reqData))
	if err != nil {
		t.Fatalf("POST /v1/grant/request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
	}

	var grantResp control.GrantResponse
	if err := json.NewDecoder(resp.Body).Decode(&grantResp); err != nil {
		t.Fatalf("failed to decode grant response: %v", err)
	}

	if !grantResp.Success {
		t.Fatalf("expected success=true in response")
	}
	if grantResp.Grant == nil {
		t.Fatalf("expected grant object in response")
	}

	g := grantResp.Grant
	if len(g.Key) != 32 {
		t.Errorf("expected 32-byte session key, got %d", len(g.Key))
	}
	if g.SessionID == "" {
		t.Errorf("expected non-empty session ID")
	}
	if g.ExpiresAt <= time.Now().Unix() {
		t.Errorf("expected future expiration timestamp, got %d", g.ExpiresAt)
	}
	if len(g.Conditions) == 0 {
		t.Errorf("expected conditions attached to grant")
	}

	// Verify grant is recorded in store
	revoked, err := store.IsGrantRevoked(ctx, g.SessionID)
	if err != nil {
		t.Errorf("expected grant recorded in store: %v", err)
	}
	if revoked {
		t.Errorf("expected newly issued grant to not be revoked")
	}

	// Verify audit event was logged
	events := server.AuditLogger().EventsForPackage(packageID)
	if len(events) == 0 {
		t.Errorf("expected audit event logged for package %q", packageID)
	}
	foundIssued := false
	for _, ev := range events {
		if ev.EventType == control.EventGrantIssued && ev.Success {
			foundIssued = true
			break
		}
	}
	if !foundIssued {
		t.Errorf("expected EventGrantIssued audit event")
	}
}

// 3. Integration with aegis/runtime: loads encrypted package -> requests grant -> decrypts -> executes
func TestRuntimeIntegration_EndToEnd(t *testing.T) {
	ctx := context.Background()
	signingKeys := newTestKeySet(t)
	masterKey := newTestMasterKey(t)
	store := control.NewMemoryStore()
	server := control.NewServer(store, control.WithServerSigningKeys(signingKeys))
	client := newTestClient(server)

	packageID := "pkg-runtime-integration-001"
	appID := "aegis.math.app"
	version := "1.0.0"
	sessionID := "predetermined-test-session-777"

	// 1. Derive the session key that the server will issue for this session
	sessionKey, err := control.DeriveSessionKey(masterKey, sessionID, version)
	if err != nil {
		t.Fatalf("DeriveSessionKey failed: %v", err)
	}

	// 2. Build and seal an encrypted package using the session key
	builder, err := pkg.NewBuilder(appID, version, signingKeys, sessionKey)
	if err != nil {
		t.Fatalf("pkg.NewBuilder failed: %v", err)
	}
	builder.SetPackageID(packageID)

	irMod := newArithmeticModule()
	sealedPkg, err := builder.Build(irMod)
	if err != nil {
		t.Fatalf("builder.Build failed: %v", err)
	}

	pkgBytes, err := pkg.Marshal(sealedPkg)
	if err != nil {
		t.Fatalf("pkg.Marshal failed: %v", err)
	}

	// 3. Register package and master key on control server
	if err := store.StoreMasterKey(ctx, packageID, masterKey); err != nil {
		t.Fatalf("StoreMasterKey failed: %v", err)
	}
	if err := store.StorePackage(ctx, control.PackageRecord{
		PackageID:     packageID,
		ApplicationID: appID,
		Version:       version,
	}); err != nil {
		t.Fatalf("StorePackage failed: %v", err)
	}

	// 4. Configure aegis/runtime client with HTTPGrantProvider
	grantProvider := control.NewHTTPGrantProvider(testBaseURL,
		control.WithHTTPClient(client),
		control.WithSessionID(sessionID),
	)
	rtConfig := runtime.DefaultConfig()
	rtConfig.TrustedKeys = signingKeys
	rtConfig.GrantProvider = grantProvider

	rt, err := runtime.New(rtConfig)
	if err != nil {
		t.Fatalf("runtime.New failed: %v", err)
	}
	defer rt.Close()

	// 5. Load encrypted package into runtime
	lp, err := rt.LoadPackage(ctx, pkgBytes)
	if err != nil {
		t.Fatalf("rt.LoadPackage failed: %v", err)
	}

	// 6. Authorize: requests grant from control server over HTTP and decrypts payload
	err = rt.Authorize(ctx, lp, runtime.GrantRequest{
		PackageID:     packageID,
		ApplicationID: appID,
		Version:       version,
		Platform:      "linux-desktop",
	})
	if err != nil {
		t.Fatalf("rt.Authorize failed: %v", err)
	}

	if !lp.IsAuthorized() {
		t.Fatalf("expected package to be marked authorized")
	}

	// 7. Execute function "add"(20, 22) -> 42
	execRes, err := rt.Execute(ctx, lp, runtime.ExecutionRequest{
		Function: "add",
		Args:     []uint64{20, 22},
	})
	if err != nil {
		t.Fatalf("rt.Execute failed: %v", err)
	}

	if len(execRes.Returns) != 1 || execRes.Returns[0] != 42 {
		t.Fatalf("expected execution result 42, got %v", execRes.Returns)
	}

	if execRes.SessionID != sessionID {
		t.Errorf("expected session ID %q in execution result, got %q", sessionID, execRes.SessionID)
	}
}

// 4. Package revocation check
func TestPackage_RevocationCheck(t *testing.T) {
	ctx := context.Background()
	store := control.NewMemoryStore()
	server := control.NewServer(store)
	client := newTestClient(server)

	packageID := "pkg-to-revoke"
	masterKey := newTestMasterKey(t)

	// Register package
	_ = store.StoreMasterKey(ctx, packageID, masterKey)
	_ = store.StorePackage(ctx, control.PackageRecord{
		PackageID:     packageID,
		ApplicationID: "app-test",
		Version:       "1.0.0",
	})

	// Revoke package via POST /v1/package/revoke
	revokeBody := control.PackageRevokePayload{
		PackageID: packageID,
		Reason:    "critical security exploit CVE-2026-9999",
	}
	revokeData, _ := json.Marshal(revokeBody)
	resp, err := client.Post(testBaseURL+"/v1/package/revoke", "application/json", bytes.NewReader(revokeData))
	if err != nil {
		t.Fatalf("POST /v1/package/revoke failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from revoke, got %d", resp.StatusCode)
	}

	// Check status via GET /v1/package/{id}/status
	statusResp, err := client.Get(testBaseURL + "/v1/package/" + packageID + "/status")
	if err != nil {
		t.Fatalf("GET /v1/package/{id}/status failed: %v", err)
	}
	defer statusResp.Body.Close()

	var status control.PackageStatusResponse
	_ = json.NewDecoder(statusResp.Body).Decode(&status)
	if !status.Revoked {
		t.Fatalf("expected package to report revoked=true")
	}
	if !strings.Contains(status.RevokeReason, "CVE-2026-9999") {
		t.Errorf("expected revocation reason to contain CVE, got %q", status.RevokeReason)
	}

	// Request grant for revoked package -> must fail
	grantBody := control.GrantRequestPayload{
		GrantRequest: runtime.GrantRequest{
			PackageID:     packageID,
			ApplicationID: "app-test",
			Version:       "1.0.0",
		},
	}
	gData, _ := json.Marshal(grantBody)
	grantReqResp, err := client.Post(testBaseURL+"/v1/grant/request", "application/json", bytes.NewReader(gData))
	if err != nil {
		t.Fatalf("POST /v1/grant/request failed: %v", err)
	}
	defer grantReqResp.Body.Close()

	if grantReqResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for revoked package, got %d", grantReqResp.StatusCode)
	}

	body, _ := io.ReadAll(grantReqResp.Body)
	if !strings.Contains(string(body), "revoked") {
		t.Errorf("expected error message to mention 'revoked', got %s", string(body))
	}
}

// 5. Grant expiration & refresh tests
func TestGrant_ExpirationAndRefresh(t *testing.T) {
	ctx := context.Background()
	store := control.NewMemoryStore()
	km := control.NewKeyManager(store)
	// Short TTL of 500ms
	gs := control.NewGrantService(store, km, control.WithTTL(500*time.Millisecond))
	server := control.NewServer(store, control.WithServerGrantService(gs))
	client := newTestClient(server)

	packageID := "pkg-grant-lifecycle"
	masterKey := newTestMasterKey(t)
	_ = store.StoreMasterKey(ctx, packageID, masterKey)
	_ = store.StorePackage(ctx, control.PackageRecord{
		PackageID: packageID,
		Version:   "1.0.0",
	})

	// 1. Request grant
	grant, err := gs.RequestGrant(ctx, runtime.GrantRequest{
		PackageID: packageID,
		Version:   "1.0.0",
	})
	if err != nil {
		t.Fatalf("RequestGrant failed: %v", err)
	}

	firstExpiresAt := grant.ExpiresAt

	// 2. Refresh grant via HTTP POST /v1/grant/refresh
	refreshBody := control.GrantRefreshPayload{
		SessionID: grant.SessionID,
		PackageID: packageID,
	}
	rData, _ := json.Marshal(refreshBody)
	resp, err := client.Post(testBaseURL+"/v1/grant/refresh", "application/json", bytes.NewReader(rData))
	if err != nil {
		t.Fatalf("POST /v1/grant/refresh failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 OK from refresh, got %d: %s", resp.StatusCode, string(b))
	}

	var refreshResp control.GrantResponse
	_ = json.NewDecoder(resp.Body).Decode(&refreshResp)
	if refreshResp.Grant == nil {
		t.Fatalf("expected grant in refresh response")
	}

	if refreshResp.Grant.ExpiresAt < firstExpiresAt {
		t.Errorf("expected refreshed expiration to be at least equal or greater than original")
	}

	// 3. Revoke grant via POST /v1/grant/revoke
	revokeBody := control.GrantRevokePayload{
		GrantID: grant.SessionID,
		Reason:  "session anomaly detected",
	}
	revData, _ := json.Marshal(revokeBody)
	revResp, err := client.Post(testBaseURL+"/v1/grant/revoke", "application/json", bytes.NewReader(revData))
	if err != nil {
		t.Fatalf("POST /v1/grant/revoke failed: %v", err)
	}
	revResp.Body.Close()
	if revResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from grant revoke, got %d", revResp.StatusCode)
	}

	// 4. Try refreshing revoked grant -> must fail
	failResp, err := client.Post(testBaseURL+"/v1/grant/refresh", "application/json", bytes.NewReader(rData))
	if err != nil {
		t.Fatalf("POST /v1/grant/refresh failed: %v", err)
	}
	defer failResp.Body.Close()
	if failResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden when refreshing revoked grant, got %d", failResp.StatusCode)
	}
}

// 6. Tampered attestation rejection
func TestAttestation_TamperedRejection(t *testing.T) {
	ctx := context.Background()
	store := control.NewMemoryStore()
	signingKeys := newTestKeySet(t)
	server := control.NewServer(store, control.WithServerSigningKeys(signingKeys))
	client := newTestClient(server)

	packageID := "pkg-attest-test"
	masterKey := newTestMasterKey(t)
	_ = store.StoreMasterKey(ctx, packageID, masterKey)
	_ = store.StorePackage(ctx, control.PackageRecord{
		PackageID: packageID,
		Version:   "1.0.0",
	})

	// 1. Submit tampered evidence with invalid token format
	tamperedBody := control.GrantRequestPayload{
		GrantRequest: runtime.GrantRequest{
			PackageID:   packageID,
			Version:     "1.0.0",
			Platform:    "tpm",
			Attestation: []byte("corrupted-tampered-quote-structure"),
		},
	}
	tData, _ := json.Marshal(tamperedBody)

	resp, err := client.Post(testBaseURL+"/v1/grant/request", "application/json", bytes.NewReader(tData))
	if err != nil {
		t.Fatalf("POST /v1/grant/request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 Forbidden for tampered attestation, got %d: %s", resp.StatusCode, string(body))
	}

	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "attestation") {
		t.Errorf("expected error to mention 'attestation', got %s", string(respBody))
	}

	// 2. Submit valid evidence (using desktop none verifier or valid JSON)
	validBody := control.GrantRequestPayload{
		GrantRequest: runtime.GrantRequest{
			PackageID:   packageID,
			Version:     "1.0.0",
			Platform:    attest.PlatformDesktopLinux,
			Attestation: []byte(`{"app_identity": "valid.app"}`),
		},
	}
	vData, _ := json.Marshal(validBody)

	validResp, err := client.Post(testBaseURL+"/v1/grant/request", "application/json", bytes.NewReader(vData))
	if err != nil {
		t.Fatalf("POST /v1/grant/request with valid attestation failed: %v", err)
	}
	defer validResp.Body.Close()

	if validResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(validResp.Body)
		t.Fatalf("expected 200 OK for valid attestation, got %d: %s", validResp.StatusCode, string(body))
	}
}

// 7. Package registration and status endpoints
func TestPackage_RegisterAndJWKS(t *testing.T) {
	signingKeys := newTestKeySet(t)
	store := control.NewMemoryStore()
	server := control.NewServer(store, control.WithServerSigningKeys(signingKeys))
	client := newTestClient(server)

	masterKeyHex := hex.EncodeToString(newTestMasterKey(t))
	regPayload := control.PackageRegisterPayload{
		PackageID:     "pkg-dynamic-reg",
		ApplicationID: "app-dynamic",
		Version:       "2.1.0",
		MasterKey:     masterKeyHex,
	}
	regData, _ := json.Marshal(regPayload)

	resp, err := client.Post(testBaseURL+"/v1/package/register", "application/json", bytes.NewReader(regData))
	if err != nil {
		t.Fatalf("POST /v1/package/register failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from register, got %d", resp.StatusCode)
	}

	// Verify status endpoint
	stResp, err := client.Get(testBaseURL + "/v1/package/pkg-dynamic-reg/status")
	if err != nil {
		t.Fatalf("GET status failed: %v", err)
	}
	defer stResp.Body.Close()
	var st control.PackageStatusResponse
	_ = json.NewDecoder(stResp.Body).Decode(&st)
	if st.PackageID != "pkg-dynamic-reg" || st.Revoked {
		t.Errorf("unexpected status: %+v", st)
	}

	// Verify JWKS endpoint
	jwksResp, err := client.Get(testBaseURL + "/v1/keys/jwks")
	if err != nil {
		t.Fatalf("GET /v1/keys/jwks failed: %v", err)
	}
	defer jwksResp.Body.Close()
	if jwksResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from JWKS, got %d", jwksResp.StatusCode)
	}

	var jwks signing.JWKS
	if err := json.NewDecoder(jwksResp.Body).Decode(&jwks); err != nil {
		t.Fatalf("failed to decode JWKS: %v", err)
	}
	if len(jwks.Keys) == 0 {
		t.Errorf("expected keys in JWKS response")
	}
}

// 8. Integrity report telemetry collection
func TestIntegrity_Report(t *testing.T) {
	store := control.NewMemoryStore()
	server := control.NewServer(store)
	client := newTestClient(server)

	reportPayload := control.IntegrityReportPayload{
		DeviceID:  "device-telemetry-01",
		PackageID: "pkg-100",
		SessionID: "sess-100",
		Signals: map[string]any{
			"debugger_present": false,
			"ptrace_denied":    true,
			"timing_delta_ns":  450,
		},
		Timestamp: time.Now().Unix(),
	}
	rData, _ := json.Marshal(reportPayload)

	resp, err := client.Post(testBaseURL+"/v1/integrity/report", "application/json", bytes.NewReader(rData))
	if err != nil {
		t.Fatalf("POST /v1/integrity/report failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from integrity report, got %d", resp.StatusCode)
	}

	events := server.AuditLogger().EventsByType(control.EventIntegrityReport)
	if len(events) != 1 {
		t.Fatalf("expected 1 EventIntegrityReport logged, got %d", len(events))
	}
	if events[0].DeviceID != "device-telemetry-01" {
		t.Errorf("expected device ID %q, got %q", "device-telemetry-01", events[0].DeviceID)
	}
}

// 9. Liveness healthz endpoint
func TestHealthz_Endpoint(t *testing.T) {
	server := control.NewServer(nil)
	client := newTestClient(server)

	resp, err := client.Get(testBaseURL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /healthz, got %d", resp.StatusCode)
	}

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", body["status"])
	}
}

// 10. Device revocation test
func TestDevice_Revocation(t *testing.T) {
	ctx := context.Background()
	store := control.NewMemoryStore()
	server := control.NewServer(store)
	client := newTestClient(server)

	packageID := "pkg-device-test"
	deviceID := "quarantined-device-99"
	masterKey := newTestMasterKey(t)
	_ = store.StoreMasterKey(ctx, packageID, masterKey)
	_ = store.StorePackage(ctx, control.PackageRecord{PackageID: packageID, Version: "1.0.0"})

	// Mark device as revoked
	_ = store.RecordDevice(ctx, control.DeviceRecord{
		DeviceID:     deviceID,
		Revoked:      true,
		RevokeReason: "tampered bootloader detected",
	})

	// Request grant with quarantined device
	reqPayload := control.GrantRequestPayload{
		GrantRequest: runtime.GrantRequest{
			PackageID: packageID,
			DeviceID:  deviceID,
			Version:   "1.0.0",
		},
	}
	reqData, _ := json.Marshal(reqPayload)

	resp, err := client.Post(testBaseURL+"/v1/grant/request", "application/json", bytes.NewReader(reqData))
	if err != nil {
		t.Fatalf("POST /v1/grant/request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for quarantined device, got %d", resp.StatusCode)
	}
}
