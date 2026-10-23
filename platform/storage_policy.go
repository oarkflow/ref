package platform

import (
	"fmt"
	"strings"
)

// Phase 5 of docs/aegis-protection-plan.md: a sealed Aegis run already
// writes nothing to disk, but only because nobody happened to configure a
// resource that would. storage_policy makes that a load-time guarantee
// instead of an accident of configuration.
//
//	storage_policy "ephemeral"       # refuse to load (default once set)
//	storage_policy "ephemeral_warn"  # log every violation, load anyway
//
// Resources checked: cache.file, queue.file, queue.broker, session.file and
// storage.fs (all take a "dir" naming where they write) and database.sql
// with driver "sqlite" and a DSN that is not in-memory. database.sql with
// any other driver (postgres, mysql) is a network resource, not a local
// disk path, and is never flagged — the application's own database is
// expected to outlive the process; what this guards against is the
// application silently keeping a *second*, local copy on this machine.
const (
	storagePolicyNone          = ""
	storagePolicyEphemeral     = "ephemeral"
	storagePolicyEphemeralWarn = "ephemeral_warn"
)

// diskResourceKinds names every resource kind that writes to a path named by
// its "dir" config key. database.sql is handled separately below, because
// whether it writes to disk depends on its driver and DSN, not its kind.
var diskResourceKinds = map[string]bool{
	"cache.file":   true,
	"queue.file":   true,
	"queue.broker": true,
	"session.file": true,
	"storage.fs":   true,
}

// storagePolicyViolation reports one resource that a storage_policy of
// "ephemeral" (or "ephemeral_warn") would refuse.
type storagePolicyViolation struct {
	resource, kind, detail string
}

func (v storagePolicyViolation) String() string {
	return fmt.Sprintf("resource %q (%s): %s", v.resource, v.kind, v.detail)
}

// checkStoragePolicy finds every resource that would write to a real disk
// path. It does not open any resource — this runs on the Document alone,
// before any connection exists, so it is safe to call from both Compile and
// the config-only Validate path.
func checkStoragePolicy(doc Document) []storagePolicyViolation {
	var violations []storagePolicyViolation
	for _, res := range doc.Resources {
		kind := normalizeName(res.Kind)
		switch {
		case diskResourceKinds[kind]:
			if dir := configString(res.Config, "dir", ""); dir != "" {
				violations = append(violations, storagePolicyViolation{res.Name, kind, fmt.Sprintf("writes to dir %q", dir)})
			}
		case kind == "database.sql":
			driver := strings.ToLower(configString(res.Config, "driver", ""))
			dsn := configString(res.Config, "dsn", "")
			if driver == "sqlite" && !looksInMemory(dsn) {
				violations = append(violations, storagePolicyViolation{res.Name, kind, fmt.Sprintf("sqlite dsn %q is a file, not :memory:", dsn)})
			}
		}
	}
	return violations
}

// looksInMemory reports whether an sqlite DSN names an in-memory database
// rather than a file. modernc.org/sqlite and mattn/go-sqlite3 both honour
// ":memory:" and "mode=memory"; a DSN with neither is a path on disk,
// whether or not it exists yet.
func looksInMemory(dsn string) bool {
	lower := strings.ToLower(dsn)
	return strings.Contains(lower, ":memory:") || strings.Contains(lower, "mode=memory")
}

// enforceStoragePolicy is checkStoragePolicy with the policy's mode applied:
// "ephemeral" turns any violation into a load error, "ephemeral_warn" into
// warnings only (returned, not logged — the caller decides how; Compile
// logs them, Validate reports them as ValidationReport warnings), and ""
// (or any value that is neither) does nothing.
func enforceStoragePolicy(doc Document) (warnings []string, err error) {
	mode := doc.StoragePolicy
	if mode != storagePolicyNone && mode != storagePolicyEphemeral && mode != storagePolicyEphemeralWarn {
		return nil, fmt.Errorf("ref/platform: storage_policy %q is not %q or %q", mode, storagePolicyEphemeral, storagePolicyEphemeralWarn)
	}
	if mode == storagePolicyNone {
		return nil, nil
	}
	violations := checkStoragePolicy(doc)
	if len(violations) == 0 {
		return nil, nil
	}
	lines := make([]string, len(violations))
	for i, v := range violations {
		lines[i] = v.String()
	}
	if mode == storagePolicyEphemeralWarn {
		return lines, nil
	}
	return nil, fmt.Errorf("ref/platform: storage_policy %q refuses to load: %s", mode, strings.Join(lines, "; "))
}
