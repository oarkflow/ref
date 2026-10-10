package platform

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oarkflow/fh"
	responsemiddleware "github.com/oarkflow/fh/mw/httpsignature"
	"github.com/oarkflow/fh/mw/securetransport"
	responseprotocol "github.com/oarkflow/fh/pkg/httpsignature"
	protocol "github.com/oarkflow/fh/pkg/securetransport"
)

// SecureTransport is the transport.secure resource: github.com/oarkflow/fh's
// encrypted, device-bound, replay-protected fetch transport, installed in front
// of an application's browser-facing routes.
//
// The browser loads fh's WebAssembly client, which holds a non-extractable
// device key, negotiates an AES-GCM session with the server and sends every
// request to a protected route as an encrypted envelope: the body, the headers
// and the response come back encrypted and, when signing is on, signed. Plain
// HTTPS stays underneath; this is a second layer, not a replacement.
//
// Sessions are tied to the web session so that signing out, or an account
// change, ends the secure session too:
//
//   - before sign-in a device registers under an anonymous principal bound to a
//     short-lived HttpOnly cookie, and may only call the sign-in paths (preauth);
//   - after sign-in the client asks for a new grant, and the device registers
//     under "user:<id>", valid only while that web session is signed in.
//
// A registration grant is random, single use, expires in seconds and is bound
// to the principal and cookie that asked for it.
type SecureTransport struct {
	name       string
	sessions   *SessionManager
	transport  *securetransport.Transport
	origins    []string
	protect    []string
	preauth    []string
	configPath string
	assetBase  string
	integrity  map[string]string
	grants     *grantStore
	bootCookie string
	secure     bool
	embedded   bool
	keyID      string

	serverKey   []byte
	signKey     ed25519.PrivateKey
	signKeyID   string
	signPublic  string
	signEnabled bool
	grantTTL    time.Duration
}

const bootCookieName = "etl_boot"

func registerSecureTransportResources(r *Registry) {
	mustResource(r, "transport.secure", ResourceFactoryFunc(openSecureTransport), ResourceKindInfo{
		Family:   "security",
		Summary:  "Encrypted, device-bound fetch transport (fh securetransport and its WebAssembly client) for browser-facing routes",
		Provides: []string{"SecureTransport"},
		Config: []ConfigField{
			{Name: "session", Type: "resource", Required: true, Summary: "The session resource whose sign-in the secure session is tied to"},
			{Name: "origins", Type: "[]string", Required: true, Summary: "Exact browser origins allowed to use the transport, e.g. https://app.example.com"},
			{Name: "protect", Type: "[]string", Summary: "Paths that must arrive encrypted: a trailing / means everything under it, an exact path means its writes (default /ui/ and the sign-in paths)"},
			{Name: "preauth", Type: "[]string", Summary: "Protected paths a device may call before anyone has signed in (default /login /register /forgot /reset)"},
			{Name: "key_id", Type: "string", Default: "etl-transport-1"},
			{Name: "server_key", Type: "string", Summary: "Base64url X25519 private key (use env.required); prefer server_key_file"},
			{Name: "server_key_file", Type: "string", Summary: "File holding the key; with create_key_file it is created when missing"},
			{Name: "create_key_file", Type: "bool", Default: "false", Summary: "Development: generate and save the key file if it does not exist, so pins survive restarts"},
			{Name: "allow_ephemeral_key", Type: "bool", Default: "false", Summary: "Development: use a fresh key on every start"},
			{Name: "sign_responses", Type: "bool", Default: "true", Summary: "Sign the encrypted responses (RFC 9421) so the client verifies them before decrypting"},
			{Name: "signing_key_file", Type: "string", Summary: "File holding the Ed25519 response-signing key (created when missing if create_key_file)"},
			{Name: "signing_key_id", Type: "string", Default: "etl-response-1"},
			{Name: "config_path", Type: "string", Default: "/secure-config.json", Summary: "Where the client fetches its pins and one-time registration grant"},
			{Name: "wasm_dir", Type: "string", Default: "static/wasm", Summary: "Directory holding the built client and its asset-manifest.json (make wasm)"},
			{Name: "wasm_prefix", Type: "string", Default: "/static/wasm", Summary: "URL prefix the client files are served under"},
			{Name: "grant_ttl", Type: "duration", Default: "90s"},
			{Name: "require_embedded_trust", Type: "bool", Default: "false", Summary: "Require the client build to embed the origin and both public keys (always on outside loopback)"},
			{Name: "secure_cookies", Type: "bool", Summary: "Mark the bootstrap cookie Secure (default: when every origin is https)"},
		},
	})
}

func openSecureTransport(_ context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	if err := rejectUnknownConfig("transport.secure", spec.Config, "session", "origins", "protect", "preauth", "key_id", "server_key", "server_key_file",
		"create_key_file", "allow_ephemeral_key", "sign_responses", "signing_key_file", "signing_key_id", "config_path", "wasm_dir", "wasm_prefix",
		"grant_ttl", "require_embedded_trust", "secure_cookies"); err != nil {
		return nil, nil, err
	}
	fail := func(format string, args ...any) (Resource, io.Closer, error) {
		return nil, nil, fmt.Errorf("transport.secure %q: %s", spec.Name, fmt.Sprintf(format, args...))
	}
	name, err := requiredString(spec.Config, "session")
	if err != nil {
		return fail("%v", err)
	}
	sessions, ok := spec.resolved[name].(*SessionManager)
	if !ok {
		return fail("session %q is not a session resource", name)
	}
	origins := configStrings(spec.Config, "origins")
	if len(origins) == 0 {
		return fail("origins is required: list the exact origins that may use the transport")
	}
	for i, o := range origins {
		origins[i] = strings.TrimRight(strings.TrimSpace(o), "/")
	}
	s := &SecureTransport{name: spec.Name, sessions: sessions, origins: origins, grants: newGrantStore(), bootCookie: bootCookieName,
		configPath: configString(spec.Config, "config_path", "/secure-config.json"), keyID: configString(spec.Config, "key_id", "etl-transport-1"),
		signKeyID: configString(spec.Config, "signing_key_id", "etl-response-1"), signEnabled: configBool(spec.Config, "sign_responses", true),
		assetBase: strings.TrimRight(configString(spec.Config, "wasm_prefix", "/static/wasm"), "/"), embedded: configBool(spec.Config, "require_embedded_trust", false)}
	if s.protect = configStrings(spec.Config, "protect"); len(s.protect) == 0 {
		s.protect = []string{"/ui/", "/login", "/logout", "/register", "/forgot", "/reset"}
	}
	if s.preauth = configStrings(spec.Config, "preauth"); len(s.preauth) == 0 {
		s.preauth = []string{"/login", "/register", "/forgot", "/reset"}
	}
	allHTTPS := true
	for _, o := range origins {
		allHTTPS = allHTTPS && strings.HasPrefix(o, "https://")
	}
	s.secure = configBool(spec.Config, "secure_cookies", allHTTPS)
	if s.grantTTL, err = configDuration(spec.Config, "grant_ttl", 90*time.Second); err != nil {
		return fail("%v", err)
	}
	create := configBool(spec.Config, "create_key_file", false)
	ephemeral := configBool(spec.Config, "allow_ephemeral_key", false)

	// X25519 transport key.
	keyText := configString(spec.Config, "server_key", "")
	if file := configString(spec.Config, "server_key_file", ""); keyText == "" && file != "" {
		if keyText, err = readOrCreateSecret(file, create, func() (string, error) {
			k, err := securetransport.GenerateServerPrivateKey()
			if err != nil {
				return "", err
			}
			return securetransport.EncodeServerPrivateKey(k)
		}); err != nil {
			return fail("server_key_file: %v", err)
		}
	}
	if keyText != "" {
		if s.serverKey, err = securetransport.DecodeServerPrivateKey(keyText); err != nil {
			return fail("%v", err)
		}
	} else if !ephemeral {
		return fail("a server key is required: set server_key_file (with create_key_file for development) or server_key, or allow_ephemeral_key for development")
	}

	// Ed25519 response-signing key.
	if s.signEnabled {
		text := ""
		if file := configString(spec.Config, "signing_key_file", ""); file != "" {
			if text, err = readOrCreateSecret(file, create, func() (string, error) {
				_, priv, err := responseprotocol.GenerateKey()
				if err != nil {
					return "", err
				}
				return responseprotocol.EncodePrivateKey(priv)
			}); err != nil {
				return fail("signing_key_file: %v", err)
			}
		}
		if text != "" {
			if s.signKey, err = responseprotocol.DecodePrivateKey(text); err != nil {
				return fail("%v", err)
			}
		} else if ephemeral {
			if _, s.signKey, err = responseprotocol.GenerateKey(); err != nil {
				return fail("%v", err)
			}
		} else {
			return fail("sign_responses needs signing_key_file (with create_key_file for development), or allow_ephemeral_key")
		}
		if s.signPublic, err = responseprotocol.EncodePublicKey(s.signKey.Public().(ed25519.PublicKey)); err != nil {
			return fail("%v", err)
		}
	}

	// The client's integrity pins come from the manifest `make wasm` wrote.
	raw, err := os.ReadFile(filepath.Join(configString(spec.Config, "wasm_dir", "static/wasm"), "asset-manifest.json"))
	if err != nil {
		return fail("the client has not been built (%v): run make wasm", err)
	}
	var manifest struct {
		Assets map[string]struct {
			Integrity string `json:"integrity"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fail("asset-manifest.json: %v", err)
	}
	s.integrity = map[string]string{}
	for file, a := range manifest.Assets {
		s.integrity[file] = a.Integrity
	}
	if s.integrity["securefetch.wasm"] == "" || s.integrity["wasm_exec.js"] == "" {
		return fail("asset-manifest.json lacks integrity pins for securefetch.wasm and wasm_exec.js: run make wasm")
	}

	t, err := securetransport.New(s.transportConfig())
	if err != nil {
		return fail("%v", err)
	}
	s.transport = t
	return s, nil, nil
}

// readOrCreateSecret reads a one-line secret file, creating it (mode 0600)
// when create is set and it does not exist.
func readOrCreateSecret(path string, create bool, generate func() (string, error)) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, os.ErrNotExist) || !create {
		return "", err
	}
	value, err := generate()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return "", err
	}
	slog.Warn("transport.secure: created a development key file", "path", path)
	return value, nil
}

// isProtected says whether a request must arrive encrypted. A path ending in /
// protects everything under it; an exact path protects its writes only, because
// the same path (/login) is also a page that is fetched in plain.
func (s *SecureTransport) isProtected(method, path string) bool {
	for _, p := range s.protect {
		switch {
		case strings.HasSuffix(p, "/") && strings.HasPrefix(path, p):
			return true
		case p == path && method != "GET" && method != "HEAD":
			return true
		}
	}
	return false
}

func (s *SecureTransport) isPreauth(path string) bool {
	for _, p := range s.preauth {
		if p == path {
			return true
		}
	}
	return false
}

func (s *SecureTransport) transportConfig() securetransport.Config {
	return securetransport.Config{
		KeyID:                   s.keyID,
		ServerPrivateKey:        s.serverKey,
		AllowEphemeralServerKey: len(s.serverKey) == 0,
		RequireSecure:           true,
		Protect:                 func(c fh.Ctx) bool { return s.isProtected(c.Method(), c.Path()) },
		AllowedOrigins:          s.origins,
		RequireOrigin:           true,
		AuthorizeDeviceRegistration: func(c fh.Ctx, _ protocol.DeviceRegistrationRequest) (string, error) {
			principal, binding, ok := s.identify(c, false)
			if !ok || !s.grants.consume(c.Get(protocol.HeaderDeviceRegistration), principal, binding) {
				slog.Warn("secure transport registration refused", "resource", s.name, "identified", ok)
				return "", fh.NewHTTPError(fh.StatusForbidden, "DEVICE_REGISTRATION_FORBIDDEN", "the registration grant is missing, expired or already used")
			}
			return principal, nil
		},
		// Every secure request must come from the web session the device was
		// registered for: sign out, a new sign-in or a lost cookie ends it.
		ValidateSession: func(c fh.Ctx, info securetransport.SessionInfo) error {
			principal, _, ok := s.identify(c, false)
			if !ok || subtle.ConstantTimeCompare([]byte(principal), []byte(info.Principal)) != 1 {
				return fh.NewHTTPError(fh.StatusUnauthorized, "SESSION_BINDING_FAILED", "this secure session no longer matches the signed-in session")
			}
			if strings.HasPrefix(principal, "anon:") && !s.isPreauth(c.Path()) {
				return fh.NewHTTPError(fh.StatusUnauthorized, "SIGN_IN_REQUIRED", "sign in first")
			}
			return nil
		},
		OnSecurityEvent: func(e securetransport.SecurityEvent) {
			slog.Warn("secure transport event", "resource", s.name, "type", e.Type, "device", e.DeviceID, "request", e.RequestID, "ip", e.IP, "detail", e.Detail)
		},
	}
}

// identify says who is asking: "user:<id>" bound to the web session id when
// someone is signed in, otherwise "anon:<id>" bound to the bootstrap cookie.
// With create, a missing bootstrap cookie is issued.
func (s *SecureTransport) identify(c fh.Ctx, create bool) (principal, binding string, ok bool) {
	if raw := c.GetCookie(s.sessions.CookieName()); raw != "" {
		if web, err := s.sessions.Load(c); err == nil && web != nil {
			if uid, _ := web.Get("user_id").(string); uid != "" {
				return "user:" + uid, strings.Clone(web.ID), true
			}
		}
	}
	boot := strings.Clone(c.GetCookie(s.bootCookie))
	if len(boot) != 32 {
		if !create {
			return "", "", false
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", "", false
		}
		boot = fmt.Sprintf("%x", b[:])
		c.SetCookie(&fh.Cookie{Name: s.bootCookie, Value: boot, Path: "/", MaxAge: 3600, HttpOnly: true, Secure: s.secure, SameSite: fh.SameSiteStrict})
	}
	return "anon:" + boot, boot, true
}

// install registers the middleware, the response signer and the client's
// bootstrap endpoint. Platform.Mount calls it before the routes.
func (s *SecureTransport) install(app *fh.App) error {
	app.Use(s.transport.Middleware())
	s.transport.Register(app)
	if s.signEnabled {
		signer, err := responsemiddleware.New(responsemiddleware.Config{
			PrivateKey:     s.signKey,
			KeyID:          s.signKeyID,
			Origin:         s.origins[0],
			AllowedOrigins: s.origins[1:],
			Validity:       90 * time.Second,
			MaxBodySize:    protocol.DefaultMaxBody + 64<<10,
			Skip:           func(c fh.Ctx) bool { return !s.isProtected(c.Method(), c.Path()) },
		})
		if err != nil {
			return fmt.Errorf("transport.secure %q: %w", s.name, err)
		}
		// After the transport: it encrypts first, the signer covers the ciphertext.
		app.Use(signer)
	}
	app.Get(s.configPath, s.bootstrap)
	return nil
}

func (s *SecureTransport) originFor(host string) (string, bool) {
	for _, o := range s.origins {
		if strings.EqualFold(strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://"), host) {
			return o, true
		}
	}
	return "", false
}

// bootstrap hands the client what it needs: the pins to verify the server by,
// the integrity pins of its own files, and one single-use registration grant.
func (s *SecureTransport) bootstrap(c fh.Ctx) error {
	origin, ok := s.originFor(c.Get(fh.HeaderHostStr))
	if !ok {
		return fh.NewHTTPError(fh.StatusMisdirectedRequest, "ORIGIN_NOT_ALLOWED", "this host is not one of the transport's origins")
	}
	principal, binding, ok := s.identify(c, true)
	if !ok {
		return fh.NewHTTPError(fh.StatusServiceUnavailable, "BOOTSTRAP_UNAVAILABLE", "could not start a secure session")
	}
	grant, err := s.grants.issue(principal, binding, s.grantTTL)
	if err != nil {
		return fh.NewHTTPError(fh.StatusServiceUnavailable, "GRANT_UNAVAILABLE", "registration grant unavailable")
	}
	c.Set("Cache-Control", "no-store")
	out := fh.Map{
		"baseURL":               origin,
		"pinnedServerKey":       s.transport.PublicKeyBase64(),
		"pinnedServerKeyID":     s.transport.KeyID(),
		"requireEmbeddedTrust":  s.embedded,
		"registrationToken":     grant,
		"wasmURL":               s.assetBase + "/securefetch.wasm",
		"wasmExecURL":           s.assetBase + "/wasm_exec.js",
		"wasmIntegrity":         s.integrity["securefetch.wasm"],
		"wasmExecIntegrity":     s.integrity["wasm_exec.js"],
		"requireAssetIntegrity": true,
		"authenticated":         strings.HasPrefix(principal, "user:"),
		// A short, one-way tag of who this session is for. The client compares it
		// with the tag its device was registered under and registers afresh when
		// they differ (after signing in or out, or as someone else).
		"principalTag": principalTag(principal),
	}
	if s.signEnabled {
		out["responseSigningPublicKey"] = s.signPublic
		out["responseSigningKeyID"] = s.signKeyID
		out["requireResponseSignature"] = true
	}
	return c.JSON(out)
}

func principalTag(principal string) string {
	sum := sha256.Sum256([]byte("etl-principal-tag:" + principal))
	return base64.RawURLEncoding.EncodeToString(sum[:9])
}

// grantStore holds registration grants: random, single use, short lived, and
// bound to the principal and cookie that asked for them.
type grantStore struct {
	mu     sync.Mutex
	grants map[[32]byte]grant
}

type grant struct {
	principal, binding string
	expires            time.Time
}

func newGrantStore() *grantStore { return &grantStore{grants: map[[32]byte]grant{}} }

func (g *grantStore) issue(principal, binding string, ttl time.Duration) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, v := range g.grants {
		if !v.expires.After(now) {
			delete(g.grants, k)
		}
	}
	if len(g.grants) >= 10000 {
		return "", errors.New("registration grant capacity exhausted")
	}
	// Strings taken from a request point into a buffer that is reused once the
	// request ends; the grant outlives it, so it keeps its own copies.
	g.grants[sha256.Sum256(raw[:])] = grant{strings.Clone(principal), strings.Clone(binding), now.Add(ttl)}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// consume is one shot: even a presentation that fails its binding burns the
// grant, so a token cannot be probed.
func (g *grantStore) consume(token, principal, binding string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return false
	}
	key := sha256.Sum256(raw)
	g.mu.Lock()
	v, ok := g.grants[key]
	delete(g.grants, key)
	g.mu.Unlock()
	if !ok || !v.expires.After(time.Now()) || v.principal != principal || v.binding != binding {
		return false
	}
	return true
}

// appInstaller is a resource that adds middleware or endpoints to the HTTP
// application before its routes are mounted.
type appInstaller interface {
	install(app *fh.App) error
}
