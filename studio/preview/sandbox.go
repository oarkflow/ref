package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/oarkflow/ref/platform"
)

// Action says what Sandbox did to one block.
type Action string

const (
	ActionKeep    Action = "keep"    // local-only kind, left as written
	ActionRewrite Action = "rewrite" // same kind, storage moved into the temp root
	ActionReplace Action = "replace" // different kind
	ActionOffline Action = "offline" // kept, but its outbound calls are stubbed at open
	ActionDisable Action = "disable" // removed
)

// Change is one line of the sandbox report, shown to the editor so they know
// what the preview is not.
type Change struct {
	Block  string `json:"block"` // e.g. "resource/database"
	Kind   string `json:"kind,omitempty"`
	Action Action `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// SandboxOptions tunes Sandbox.
type SandboxOptions struct {
	// Root is the per-generation directory. Everything that must persist
	// (databases, queues, object stores) is placed inside it.
	Root string
	// ExtraKinds are host-registered resource kinds the host vouches for as
	// local-only. Any kind Sandbox does not know and is not listed here makes
	// Sandbox fail, so a new driver cannot reach a real backend by accident.
	ExtraKinds []string
	// AllowGuardEndpoints permits a security.tcpguard policy that declares HTTP
	// endpoints (webhook actions, remote detectors). tcpguard calls those with
	// http.DefaultClient, which the preview cannot intercept, so by default such
	// a policy makes Sandbox fail rather than let a preview call out.
	AllowGuardEndpoints bool
}

// kindPolicy classifies every built-in resource kind. A test asserts the
// catalog and this table agree, so adding a kind without deciding how it
// behaves in preview fails the build of this package's tests.
var kindPolicy = map[string]Action{
	// local-only, no external effect
	"cache.memory": ActionKeep, "cache.sql": ActionKeep,
	"session.memory": ActionKeep, "session.sql": ActionKeep,
	"queue.sql": ActionKeep, "store.sql": ActionKeep, "store.memory": ActionKeep,
	"storage.sql": ActionKeep, "search.sql": ActionKeep, "outbox.memory": ActionKeep,
	"lock.memory": ActionKeep, "lock.store": ActionKeep,
	"ratelimit.memory": ActionKeep, "ratelimit.store": ActionKeep,
	"circuit_breaker.store": ActionKeep,
	"auth.api_key":          ActionKeep, "auth.basic": ActionKeep, "auth.jwt": ActionKeep,
	"auth.session": ActionKeep, "auth.chain": ActionKeep,
	"authz.rbac": ActionKeep, "authz.engine": ActionKeep, "rules.engine": ActionKeep,
	"security.tcpguard": ActionKeep, "crypto.signer": ActionKeep,
	"org.hierarchy": ActionKeep, "identity.users": ActionKeep, "pipeline.cases": ActionKeep,
	"workflow.http": ActionKeep,
	// storage moves into the temp root
	"database.sql": ActionRewrite,
	"cache.file":   ActionRewrite, "queue.file": ActionRewrite, "session.file": ActionRewrite,
	"storage.fs": ActionRewrite, "secret.file": ActionRewrite,
	// replaced
	"secret.env": ActionReplace,
	"auth.oidc":  ActionReplace,
	// outbound: real config, stubbed I/O
	"service.http": ActionOffline, "service.smtp": ActionOffline, "service.llm": ActionOffline,
}

// KindPolicy reports how Sandbox treats a resource kind.
func KindPolicy(kind string) (Action, bool) {
	a, ok := kindPolicy[strings.ToLower(strings.TrimSpace(kind))]
	return a, ok
}

// Sandbox rewrites doc in place so it can run without touching anything real:
// datastores live under opts.Root, outbound providers are marked to be stubbed
// (the caller supplies the stubs through platform.WithOffline), and workers,
// schedules and triggers are removed. It returns what it did.
//
// It is meant to be used as a platform.LoadOptions.Mutate hook. It refuses
// resource kinds it has no rule for (see SandboxOptions.ExtraKinds), and it is
// a guard against accidents by trusted editors, not a security boundary
// against a hostile document: BCL can still read files the process can read.
func Sandbox(doc *platform.Document, opts SandboxOptions) ([]Change, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("preview sandbox: Root is required")
	}
	extra := map[string]bool{}
	for _, k := range opts.ExtraKinds {
		extra[strings.ToLower(k)] = true
	}
	var (
		changes   []Change
		unknown   []string
		guardErrs []string
		resource  = func(name string) string { return "resource/" + name }
	)
	for i := range doc.Resources {
		spec := &doc.Resources[i]
		kind := strings.ToLower(strings.TrimSpace(spec.Kind))
		action, known := kindPolicy[kind]
		if !known {
			if extra[kind] {
				changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: ActionKeep, Detail: "host-vouched kind"})
				continue
			}
			unknown = append(unknown, fmt.Sprintf("%s (%s)", spec.Name, spec.Kind))
			continue
		}
		switch kind {
		case "database.sql":
			cfg := cloneConfig(spec.Config)
			file := filepath.Join(opts.Root, "db", slug(spec.Name)+".db")
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				return nil, err
			}
			was := fmt.Sprint(cfg["driver"])
			detail := "driver " + was + " -> sqlite in the preview temp dir; starts empty"
			if was == "sqlite" {
				detail = "moved into the preview temp dir; starts empty"
			}
			cfg["driver"] = "sqlite"
			cfg["dsn"] = "file:" + file + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
			delete(cfg, "read_replica_dsn")
			spec.Config = cfg
			changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: ActionRewrite,
				Detail: detail})
		case "cache.file", "queue.file", "session.file", "storage.fs", "secret.file":
			cfg := cloneConfig(spec.Config)
			dir := filepath.Join(opts.Root, strings.ReplaceAll(kind, ".", "-"), slug(spec.Name))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
			cfg["dir"] = dir
			spec.Config = cfg
			detail := "dir moved into the preview temp dir"
			if kind == "secret.file" {
				detail = "reads an empty directory; no host secrets"
			}
			changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: ActionRewrite, Detail: detail})
		case "secret.env":
			dir := filepath.Join(opts.Root, "secret-file", slug(spec.Name))
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
			spec.Kind = "secret.file"
			spec.Config = map[string]any{"dir": dir}
			changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: ActionReplace,
				Detail: "secret.env would read the host environment; replaced by an empty secret.file"})
		case "auth.oidc":
			cfg := oidcToJWT(spec.Config, spec.Name)
			spec.Kind = "auth.jwt"
			spec.Config = cfg
			changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: ActionReplace,
				Detail: "OIDC discovery needs the network; replaced by auth.jwt with a throwaway HS256 key, so bearer tokens from the real provider are rejected"})
		case "security.tcpguard":
			cfg := cloneConfig(spec.Config)
			cfg["geoip"] = false
			spec.Config = cfg
			detail := "GeoIP off (its database download and ~/.ipdata cache are outside the sandbox); rules keyed on country see no location"
			if files := guardEndpoints(cfg); len(files) > 0 && !opts.AllowGuardEndpoints {
				guardErrs = append(guardErrs, fmt.Sprintf("%s (%s)", spec.Name, strings.Join(files, ", ")))
			} else if len(files) > 0 {
				detail += "; policy declares HTTP endpoints, which are NOT intercepted"
			}
			changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: ActionRewrite, Detail: detail})
		case "service.http", "service.smtp", "service.llm":
			changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: ActionOffline,
				Detail: "requests are recorded and answered by a stub; nothing leaves the process"})
		default:
			changes = append(changes, Change{Block: resource(spec.Name), Kind: kind, Action: action})
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("preview cannot run these resources because their kind has no sandbox rule: %s (add the kind to Options.ExtraKinds if it is local-only)",
			strings.Join(unknown, ", "))
	}
	if len(guardErrs) > 0 {
		return nil, fmt.Errorf("preview refuses security.tcpguard policies that declare HTTP endpoints, because tcpguard calls them itself and the sandbox cannot stub them: %s (set SandboxOptions.AllowGuardEndpoints to accept)",
			strings.Join(guardErrs, "; "))
	}
	for _, w := range doc.Workers {
		changes = append(changes, Change{Block: "worker/" + w.Name, Action: ActionDisable, Detail: "background consumers are off in preview"})
	}
	for _, s := range doc.Schedules {
		changes = append(changes, Change{Block: "schedule/" + s.Name, Action: ActionDisable, Detail: "timers are off in preview"})
	}
	for _, t := range doc.Triggers {
		changes = append(changes, Change{Block: "trigger/" + t.Name, Action: ActionDisable, Detail: "inbound webhooks are off in preview"})
	}
	doc.Workers, doc.Schedules, doc.Triggers = nil, nil, nil
	return changes, nil
}

var guardEndpointRE = regexp.MustCompile(`(?m)^[ \t]*endpoint[ \t]+\S`)

// guardEndpoints reports which policy files (or "source") of a security.tcpguard
// config declare an HTTP endpoint. It only reads.
func guardEndpoints(cfg map[string]any) []string {
	var hits []string
	if src, _ := cfg["source"].(string); src != "" && guardEndpointRE.MatchString(src) {
		hits = append(hits, "source")
	}
	if dir, _ := cfg["path"].(string); dir != "" {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".bcl") {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil && guardEndpointRE.Match(b) {
				hits = append(hits, p)
			}
			return nil
		})
	}
	return hits
}

func cloneConfig(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	maps.Copy(out, in)
	return out
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// oidcToJWT keeps the claim mapping and audience/issuer of an OIDC resource and
// swaps the key material for a throwaway secret.
func oidcToJWT(in map[string]any, name string) map[string]any {
	out := map[string]any{
		"algorithm": "HS256",
		"secret":    placeholder("oidc-"+name, 48),
	}
	for _, k := range []string{"issuer", "audience", "skew", "roles_claim", "scopes_claim", "tenant_claim", "email_claim", "username_claim"} {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	return out
}

// placeholder is a deterministic filler value of at least n bytes, used where
// the document insists on a secret and the preview must not use a real one.
func placeholder(name string, n int) string {
	sum := sha256.Sum256([]byte(name))
	s := "preview-" + hex.EncodeToString(sum[:])
	for len(s) < n {
		s += hex.EncodeToString(sum[:])
	}
	return s[:max(n, 0)]
}

var (
	envRequiredRE = regexp.MustCompile(`env\.required\(\s*"([^"]+)"`)
	envAttrRE     = regexp.MustCompile(`\benv\s+"([^"]+)"`)
)

// requiredEnv lists the environment variable names the source insists on
// (env.required calls and secret blocks' env attribute).
func requiredEnv(src []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, re := range []*regexp.Regexp{envRequiredRE, envAttrRE} {
		for _, m := range re.FindAllSubmatch(src, -1) {
			n := string(m[1])
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// previewEnv returns the Env function preview compiles with. It never reads the
// host environment: variables the source requires get a placeholder, everything
// else is unset so env("X", "default") yields its default. explicit wins.
func previewEnv(src []byte, explicit map[string]string) func(string) (string, bool) {
	values := map[string]string{}
	for _, n := range requiredEnv(src) {
		values[n] = placeholder(n, 64)
	}
	maps.Copy(values, explicit)
	return func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
}
