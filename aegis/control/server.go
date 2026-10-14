package control

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/runtime"
	"github.com/oarkflow/ref/signing"
)

// ServerOption configures the HTTP control server.
type ServerOption func(*Server)

// WithServerGrantService sets a custom GrantService for the server.
func WithServerGrantService(gs *GrantService) ServerOption {
	return func(s *Server) {
		s.grantService = gs
	}
}

// WithServerAuditLogger sets the audit logger for the server.
func WithServerAuditLogger(al *AuditLogger) ServerOption {
	return func(s *Server) {
		s.auditLogger = al
	}
}

// WithServerSigningKeys configures the trusted signing key set exposed on /v1/keys/jwks.
func WithServerSigningKeys(ks *signing.KeySet) ServerOption {
	return func(s *Server) {
		s.signingKeySet = ks
	}
}

// WithServerTTL sets the default session grant TTL on the internal grant service.
func WithServerTTL(ttl time.Duration) ServerOption {
	return func(s *Server) {
		s.ttl = ttl
	}
}

// Server provides standard net/http handlers exposing the Aegis Control Server API:
//
//	POST /v1/grant/request       - Requests execution authorization grant
//	POST /v1/grant/refresh       - Refreshes an active grant session
//	POST /v1/grant/revoke        - Revokes an active grant session
//	POST /v1/package/register    - Registers package version and master key
//	POST /v1/package/revoke      - Revokes package version
//	GET  /v1/package/{id}/status - Checks package revocation status
//	GET  /v1/keys/jwks           - Exposes public key set for manifest verification
//	POST /v1/integrity/report    - Collects client integrity telemetry signals
//	GET  /healthz                - Liveness / health check
type Server struct {
	store         Store
	keyManager    *KeyManager
	grantService  *GrantService
	auditLogger   *AuditLogger
	signingKeySet *signing.KeySet
	ttl           time.Duration
	mux           *http.ServeMux
}

// NewServer initializes a new Server implementing http.Handler.
func NewServer(store Store, opts ...ServerOption) *Server {
	if store == nil {
		store = NewMemoryStore()
	}

	km := NewKeyManager(store)
	al := NewAuditLogger(nil)
	gs := NewGrantService(store, km, WithAuditLogger(al))

	s := &Server{
		store:        store,
		keyManager:   km,
		grantService: gs,
		auditLogger:  al,
		ttl:          time.Hour,
		mux:          http.NewServeMux(),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.registerRoutes()
	return s
}

// ServeHTTP delegates request handling to the internal HTTP ServeMux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Store returns the underlying Store.
func (s *Server) Store() Store {
	return s.store
}

// KeyManager returns the KeyManager.
func (s *Server) KeyManager() *KeyManager {
	return s.keyManager
}

// GrantService returns the GrantService.
func (s *Server) GrantService() *GrantService {
	return s.grantService
}

// AuditLogger returns the AuditLogger.
func (s *Server) AuditLogger() *AuditLogger {
	return s.auditLogger
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("POST /v1/grant/request", s.handleGrantRequest)
	s.mux.HandleFunc("POST /v1/grant/refresh", s.handleGrantRefresh)
	s.mux.HandleFunc("POST /v1/grant/revoke", s.handleGrantRevoke)
	s.mux.HandleFunc("POST /v1/package/register", s.handlePackageRegister)
	s.mux.HandleFunc("POST /v1/package/revoke", s.handlePackageRevoke)
	s.mux.HandleFunc("GET /v1/package/{id}/status", s.handlePackageStatus)
	s.mux.HandleFunc("GET /v1/package/", s.handlePackageStatusFallback)
	s.mux.HandleFunc("GET /v1/keys/jwks", s.handleJWKS)
	s.mux.HandleFunc("POST /v1/integrity/report", s.handleIntegrityReport)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
}

func (s *Server) handleGrantRequest(w http.ResponseWriter, r *http.Request) {
	var payload GrantRequestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	grant, err := s.grantService.RequestGrantWithSession(r.Context(), payload.GrantRequest, payload.SessionID)
	if err != nil {
		status := http.StatusForbidden
		if strings.Contains(err.Error(), "required") {
			status = http.StatusBadRequest
		} else if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		s.writeJSONError(w, status, err.Error())
		return
	}

	// Format response to support both top-level unmarshaling into runtime.Grant
	// and unmarshaling into GrantResponse
	resp := map[string]any{
		"success":     true,
		"key":         grant.Key,
		"expires_at":  grant.ExpiresAt,
		"session_id":  grant.SessionID,
		"issued_at":   grant.IssuedAt,
		"conditions":  grant.Conditions,
		"grant":       grant,
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGrantRefresh(w http.ResponseWriter, r *http.Request) {
	var payload GrantRefreshPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if payload.SessionID == "" {
		s.writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	refreshed, err := s.grantService.RefreshGrant(r.Context(), payload.SessionID, payload.PackageID)
	if err != nil {
		s.writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	resp := map[string]any{
		"success":    true,
		"key":        refreshed.Key,
		"expires_at": refreshed.ExpiresAt,
		"session_id": refreshed.SessionID,
		"issued_at":  refreshed.IssuedAt,
		"conditions": refreshed.Conditions,
		"grant":      refreshed,
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGrantRevoke(w http.ResponseWriter, r *http.Request) {
	var payload GrantRevokePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if payload.GrantID == "" {
		s.writeJSONError(w, http.StatusBadRequest, "grant_id is required")
		return
	}

	if err := s.grantService.RevokeGrant(r.Context(), payload.GrantID, payload.Reason); err != nil {
		s.writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"grant_id": payload.GrantID,
		"revoked":  true,
	})
}

func (s *Server) handlePackageRegister(w http.ResponseWriter, r *http.Request) {
	var payload PackageRegisterPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	payload.PackageID = strings.TrimSpace(payload.PackageID)
	if payload.PackageID == "" {
		s.writeJSONError(w, http.StatusBadRequest, "package_id is required")
		return
	}

	payload.MasterKey = strings.TrimSpace(payload.MasterKey)
	if payload.MasterKey == "" {
		s.writeJSONError(w, http.StatusBadRequest, "master_key is required")
		return
	}

	keyBytes, err := hex.DecodeString(payload.MasterKey)
	if err != nil || len(keyBytes) != 32 {
		s.writeJSONError(w, http.StatusBadRequest, "master_key must be a 64-character hex-encoded 32-byte key")
		return
	}

	// 1. Store master symmetric key
	if err := s.keyManager.StoreMasterKey(r.Context(), payload.PackageID, keyBytes); err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, "failed to store master key: "+err.Error())
		return
	}

	// 2. Store package metadata record
	now := time.Now().UTC()
	record := PackageRecord{
		PackageID:     payload.PackageID,
		ApplicationID: payload.ApplicationID,
		Version:       payload.Version,
		BuildID:       payload.BuildID,
		Revoked:       false,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.store.StorePackage(r.Context(), record); err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, "failed to store package record: "+err.Error())
		return
	}

	if s.auditLogger != nil {
		s.auditLogger.Log(r.Context(), SecurityEvent{
			EventType:     EventPackageRegistered,
			PackageID:     payload.PackageID,
			ApplicationID: payload.ApplicationID,
			Success:       true,
			Timestamp:     now,
			Details: map[string]any{
				"version": payload.Version,
			},
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"package_id": payload.PackageID,
	})
}

func (s *Server) handlePackageRevoke(w http.ResponseWriter, r *http.Request) {
	var payload PackageRevokePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	payload.PackageID = strings.TrimSpace(payload.PackageID)
	if payload.PackageID == "" {
		s.writeJSONError(w, http.StatusBadRequest, "package_id is required")
		return
	}

	if err := s.store.RevokePackage(r.Context(), payload.PackageID, payload.Reason); err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if s.auditLogger != nil {
		s.auditLogger.Log(r.Context(), SecurityEvent{
			EventType: EventPackageRevoked,
			PackageID: payload.PackageID,
			Success:   true,
			Reason:    payload.Reason,
			Timestamp: time.Now().UTC(),
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"package_id": payload.PackageID,
		"revoked":    true,
		"reason":     payload.Reason,
	})
}

func (s *Server) handlePackageStatus(w http.ResponseWriter, r *http.Request) {
	pkgID := r.PathValue("id")
	if pkgID == "" {
		s.writeJSONError(w, http.StatusBadRequest, "package id is required")
		return
	}

	s.respondPackageStatus(w, r, pkgID)
}

func (s *Server) handlePackageStatusFallback(w http.ResponseWriter, r *http.Request) {
	// Fallback path matching for /v1/package/{id}/status if pathValue is empty
	path := strings.TrimPrefix(r.URL.Path, "/v1/package/")
	parts := strings.Split(path, "/")
	if len(parts) >= 2 && parts[1] == "status" {
		s.respondPackageStatus(w, r, parts[0])
		return
	}
	s.writeJSONError(w, http.StatusNotFound, "endpoint not found")
}

func (s *Server) respondPackageStatus(w http.ResponseWriter, r *http.Request, pkgID string) {
	pkgRec, _ := s.store.GetPackage(r.Context(), pkgID)
	revoked, reason, _ := s.store.IsPackageRevoked(r.Context(), pkgID)

	resp := PackageStatusResponse{
		PackageID:    pkgID,
		Revoked:      revoked,
		RevokeReason: reason,
	}

	if pkgRec != nil {
		resp.ApplicationID = pkgRec.ApplicationID
		resp.Version = pkgRec.Version
		resp.RevokedAt = pkgRec.RevokedAt
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	if s.signingKeySet != nil {
		jwks := s.signingKeySet.JWKS()
		s.writeJSON(w, http.StatusOK, jwks)
		return
	}

	// Empty keyset fallback
	s.writeJSON(w, http.StatusOK, signing.JWKS{Keys: []signing.JWK{}})
}

func (s *Server) handleIntegrityReport(w http.ResponseWriter, r *http.Request) {
	var payload IntegrityReportPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	now := time.Now().UTC()
	if payload.Timestamp > 0 {
		now = time.Unix(payload.Timestamp, 0).UTC()
	}

	if s.auditLogger != nil {
		s.auditLogger.Log(r.Context(), SecurityEvent{
			EventType: EventIntegrityReport,
			PackageID: payload.PackageID,
			SessionID: payload.SessionID,
			DeviceID:  payload.DeviceID,
			Success:   true,
			Timestamp: now,
			Details:   payload.Signals,
		})
	}

	if payload.DeviceID != "" {
		_ = s.store.RecordDevice(r.Context(), DeviceRecord{
			DeviceID:   payload.DeviceID,
			LastSeenAt: now,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"recorded": true,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "aegis-control",
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"error":   message,
	})
}

// HTTPGrantProviderOption configures optional behavior of HTTPGrantProvider.
type HTTPGrantProviderOption func(*HTTPGrantProvider)

// WithHTTPClient configures a custom HTTP client for HTTPGrantProvider.
func WithHTTPClient(client *http.Client) HTTPGrantProviderOption {
	return func(p *HTTPGrantProvider) {
		if client != nil {
			p.client = client
		}
	}
}

// WithSessionID configures a fixed or predetermined session ID for HTTPGrantProvider.
func WithSessionID(sessionID string) HTTPGrantProviderOption {
	return func(p *HTTPGrantProvider) {
		p.sessionID = sessionID
	}
}

// HTTPGrantProvider implements runtime.GrantProvider by communicating with an Aegis control server over HTTP.
type HTTPGrantProvider struct {
	baseURL   string
	client    *http.Client
	sessionID string
}

// NewHTTPGrantProvider constructs an HTTPGrantProvider pointing to the given control server URL.
func NewHTTPGrantProvider(baseURL string, opts ...HTTPGrantProviderOption) *HTTPGrantProvider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	p := &HTTPGrantProvider{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// RequestGrant satisfies the runtime.GrantProvider interface by calling POST /v1/grant/request.
func (p *HTTPGrantProvider) RequestGrant(ctx context.Context, req runtime.GrantRequest) (*runtime.Grant, error) {
	payload := GrantRequestPayload{
		GrantRequest: req,
		SessionID:    p.sessionID,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("control: failed to marshal grant request: %w", err)
	}

	url := p.baseURL + "/v1/grant/request"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("control: failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("control: grant request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("control: failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBytes, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("control: %s", errResp.Error)
		}
		return nil, fmt.Errorf("control: grant request returned HTTP status %d: %s", resp.StatusCode, string(respBytes))
	}

	// Try unmarshaling GrantResponse first
	var grantResp GrantResponse
	if err := json.Unmarshal(respBytes, &grantResp); err == nil && grantResp.Grant != nil {
		return grantResp.Grant, nil
	}

	// Fallback to direct runtime.Grant unmarshal
	var grant runtime.Grant
	if err := json.Unmarshal(respBytes, &grant); err != nil {
		return nil, fmt.Errorf("control: failed to decode grant response: %w", err)
	}

	if len(grant.Key) == 0 {
		return nil, errors.New("control: received empty key in grant response")
	}

	return &grant, nil
}
