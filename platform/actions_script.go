package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"

	interp "github.com/oarkflow/interpreter"
	"github.com/oarkflow/interpreter/pkg/eval"
	"github.com/oarkflow/ref/pipeline"
)

// The script action runs SPL (github.com/oarkflow/interpreter) as an intent
// node, so any place that calls an intent — a pipeline stage hook, an
// automated node, an event hook, a route — can run a script.
//
// Scripts are configuration, not trusted host code, so they always run in the
// interpreter's in-process strict mode with every host capability denied
// (filesystem, network, exec, env, database, ...). A script can compute over
// the node's facts and return a value; it cannot reach anything else, which is
// what lets the action be classed as pure. The limits (steps, heap, output,
// time) are bounded per run.
//
// The interpreter's security policy is process-wide, so concurrent scripts
// take turns. Keep scripts short; use a normal node for heavy work.

const (
	scriptDefaultTimeout = 2 * time.Second
	scriptDefaultSteps   = 1_000_000
	scriptMaxSource      = 256 << 10
)

// scriptDenied is every capability the interpreter knows. Deny-listing them all
// (rather than relying on an empty allow-list) is what stops the strict mode's
// default of "reads under the working directory are fine".
var scriptDenied = []string{
	"async", "db", "env_read", "env_write", "exec", "filesystem_read", "filesystem_write",
	"network", "policy", "process_exit", "scheduler", "server", "secrets", "system", "watch",
}

// Script is a compiled, named script: its source is loaded and parse-checked
// once at load time.
type Script struct {
	Name   string
	Source string
	Limits ScriptLimits
}

// Run evaluates the script over data.
func (s *Script) Run(data map[string]any) (any, error) {
	return RunScript(s.Source, data, s.Limits)
}

// compileScripts loads every script block. A duplicate name, a missing file or
// a parse error stops startup, not the first request that reaches the script.
func compileScripts(specs []ScriptSpec, fsys fs.FS) (map[string]*Script, error) {
	out := make(map[string]*Script, len(specs))
	for _, sp := range specs {
		if sp.Name == "" {
			return nil, fmt.Errorf("script block needs a name")
		}
		if _, dup := out[sp.Name]; dup {
			return nil, fmt.Errorf("script %q is declared twice", sp.Name)
		}
		src := sp.Source
		switch {
		case src != "" && sp.File != "":
			return nil, fmt.Errorf("script %q: set source or file, not both", sp.Name)
		case src == "" && sp.File == "":
			return nil, fmt.Errorf("script %q: needs source or file", sp.Name)
		case sp.File != "":
			if !strings.HasSuffix(sp.File, ".spl") {
				return nil, fmt.Errorf("script %q: file %q must be an .spl file", sp.Name, sp.File)
			}
			var err error
			if src, err = readScriptFile(fsys, sp.File); err != nil {
				return nil, fmt.Errorf("script %q: %w", sp.Name, err)
			}
		}
		lim := ScriptLimits{MaxSteps: sp.MaxSteps, MaxHeapMB: sp.MaxHeapMB}
		if sp.Timeout != "" {
			d, err := time.ParseDuration(sp.Timeout)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("script %q: timeout %q is not a duration", sp.Name, sp.Timeout)
			}
			lim.Timeout = d
		}
		// Running it with no data normally fails on an undefined variable, which
		// is fine: only a parse error is a defect in the script itself.
		if _, err := RunScript(src, map[string]any{}, ScriptLimits{Timeout: 500 * time.Millisecond, MaxSteps: 10_000}); err != nil &&
			strings.Contains(err.Error(), "parser errors") {
			return nil, fmt.Errorf("script %q: %w", sp.Name, err)
		}
		out[sp.Name] = &Script{Name: sp.Name, Source: src, Limits: lim}
	}
	return out, nil
}

func registerScriptActions(r *Registry) {
	mustAction(r, "script", scriptAction, ActionInfo{
		Family:   "compute",
		Summary:  "Run a named script block over the node's facts and publish its result; sandboxed, with no host access",
		Provides: "The script's final value. With several provides names the script returns an object and each name takes its key",
		Kind:     "pure",
		Config: []ConfigField{
			{Name: "script", Type: "string", Required: true, Summary: "Id of a top-level script block"},
		},
	})
}

var scriptAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if len(spec.Provides) == 0 {
		return nil, fmt.Errorf("node %q: script needs at least one name in provides", spec.Name)
	}
	id := configString(spec.Config, "script", "")
	if id == "" {
		return nil, fmt.Errorf("node %q: script needs config.script naming a script block", spec.Name)
	}
	var sc *Script
	if build.Platform != nil {
		sc = build.Platform.scripts[id]
	}
	if sc == nil {
		return nil, fmt.Errorf("node %q: unknown script %q%s", spec.Name, id, scriptHint(build.Platform))
	}
	provides := append([]string(nil), spec.Provides...)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)
		delete(env, "config")
		data, err := scriptData(env)
		if err != nil {
			return ActionResult{}, fmt.Errorf("script %s: %w", id, err)
		}
		value, err := sc.Run(data)
		if err != nil {
			return ActionResult{}, fmt.Errorf("script %s: %w", id, err)
		}
		if len(provides) == 1 {
			return ActionResult{Outputs: map[string]any{provides[0]: value}}, nil
		}
		obj, ok := value.(map[string]any)
		if !ok {
			return ActionResult{}, fmt.Errorf("script %s: provides names %v need the script to return an object, got %T", id, provides, value)
		}
		out := make(map[string]any, len(provides))
		for _, name := range provides {
			out[name] = obj[name]
		}
		return ActionResult{Outputs: out}, nil
	}), nil
})

// scriptHint lists the declared script ids for an "unknown script" error.
func scriptHint(p *Platform) string {
	if p == nil || len(p.scripts) == 0 {
		return " (the application declares no script blocks)"
	}
	names := make([]string, 0, len(p.scripts))
	for n := range p.scripts {
		names = append(names, n)
	}
	sort.Strings(names)
	return " (declared: " + strings.Join(names, ", ") + ")"
}

// ScriptLimits bound one script run. Zero values take the defaults.
type ScriptLimits struct {
	Timeout   time.Duration
	MaxSteps  int64
	MaxHeapMB int64
}

// RunScript evaluates SPL source with the given variables under the sandbox
// described above and returns its final value as plain Go data.
func RunScript(source string, data map[string]any, lim ScriptLimits) (any, error) {
	if len(source) > scriptMaxSource {
		return nil, fmt.Errorf("script is %d bytes, over the %d byte limit", len(source), scriptMaxSource)
	}
	if lim.Timeout <= 0 {
		lim.Timeout = scriptDefaultTimeout
	}
	if lim.MaxSteps <= 0 {
		lim.MaxSteps = scriptDefaultSteps
	}
	// The interpreter's default sandbox caps the heap at a fixed 256MB of the
	// *process* heap, which would fail every script once the server itself is
	// that large. Replace it: no heap limit unless the script asks for one.
	maxHeapMB := int64(1 << 20)
	if lim.MaxHeapMB > 0 {
		// The interpreter compares the process heap with an absolute ceiling, so
		// a script limit is expressed as growth: the heap at the start of the run
		// plus the allowance. Other requests allocating at the same time count
		// against it too, which makes it a guard against runaway scripts rather
		// than exact accounting.
		var ms goruntime.MemStats
		goruntime.ReadMemStats(&ms)
		maxHeapMB = int64(ms.Alloc>>20) + 1 + lim.MaxHeapMB
	}
	sandbox := interp.DefaultExecSandboxConfig()
	sandbox.MaxHeapMB = maxHeapMB
	obj, err := interp.ExecWithOptions(source, data, interp.ExecOptions{
		Timeout:  lim.Timeout,
		MaxSteps: lim.MaxSteps,
		// Off unless the script sets max_heap_mb; string, array and hash caps
		// bound a script's own memory regardless.
		MaxHeapMB:      maxHeapMB,
		Sandbox:        &sandbox,
		MaxOutputBytes: 64 << 10,
		MaxStringBytes: 1 << 20,
		MaxArrayLength: 100_000,
		MaxHashEntries: 100_000,
		Security: &interp.SecurityPolicy{
			StrictMode:         true,
			ProtectHost:        true,
			DeniedCapabilities: scriptDenied,
			DenyDynamicImports: true,
		},
	})
	if err != nil {
		return nil, err
	}
	return eval.ToNative(obj), nil
}

// scriptData reduces facts to plain JSON-shaped data. The interpreter walks
// any struct it is given by reflection and panics on unexported fields (a
// time.Time, for one), so nothing but maps, slices, strings, numbers and bools
// is allowed through.
func scriptData(env Env) (map[string]any, error) {
	raw, err := json.Marshal(map[string]any(env))
	if err != nil {
		return nil, fmt.Errorf("facts are not plain data: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	for k, v := range out {
		out[k] = wholeNumbers(v)
	}
	return out, nil
}

// wholeNumbers turns JSON's float64 into int64 where the value is whole, so
// 3 stays 3 in the script rather than 3.0.
func wholeNumbers(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case []any:
		for i := range t {
			t[i] = wholeNumbers(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = wholeNumbers(t[k])
		}
		return t
	}
	return v
}

// scriptHookPrefix marks a hook name as a script id rather than an intent.
const scriptHookPrefix = "script:"

// callScript runs a script block as a hook. The input (a pipeline hook's
// case/data/stage/event, say) is available both whole as `input` and with its
// top-level keys as variables (`case` is renamed `kase`, being an SPL
// keyword). A script that returns an object with `passed: false` fails an
// automated node; any returned value is the hook's result, exactly as an
// intent's output would be.
func (p *Platform) callScript(ctx context.Context, id string, input any) (any, error) {
	sc := p.scripts[id]
	if sc == nil {
		return nil, invalidInput("script hook %q: no script block with that id%s", id, scriptHint(p))
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, invalidInput("script hook %q: input is not plain data: %v", id, err)
	}
	var plain any
	_ = json.Unmarshal(raw, &plain)
	data := map[string]any{}
	if m, ok := plain.(map[string]any); ok {
		for k, v := range m {
			data[k] = wholeNumbers(v)
		}
	}
	data["input"] = wholeNumbers(plain)
	if c, ok := data["case"]; ok {
		data["kase"] = c
		delete(data, "case")
	}
	if rid, ok := requestIdentityFrom(ctx); ok {
		data["principal"] = principalMap(rid.principal)
		data["tenant"] = rid.tenant
	}
	type result struct {
		v   any
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := sc.Run(data)
		done <- result{v, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("script %s: %w", id, r.err)
		}
		return r.v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// readScriptFile reads an application script from fsys (the sealed in-memory
// filesystem when there is one), falling back to the disk only when the
// application has no virtual filesystem.
func readScriptFile(fsys fs.FS, path string) (string, error) {
	var (
		data []byte
		err  error
	)
	if fsys != nil {
		data, err = fs.ReadFile(fsys, strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "/"))
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	if len(data) > scriptMaxSource {
		return "", fmt.Errorf("script is %d bytes, over the %d byte limit", len(data), scriptMaxSource)
	}
	return string(data), nil
}

// checkScriptHook verifies at load time that a "script:<id>" hook names a
// declared script block.
func checkScriptHook(known map[string]bool, hook string) error {
	id, ok := strings.CutPrefix(hook, scriptHookPrefix)
	if !ok {
		return nil
	}
	if !known[id] {
		return fmt.Errorf("hook %q: no script block %q", hook, id)
	}
	return nil
}

// scriptHooks lists every hook a pipeline definition names, for validation.
func scriptHooks(def *pipeline.Definition) []string {
	var hooks []string
	for _, h := range def.On {
		hooks = append(hooks, h.Hook)
	}
	for _, st := range def.Stages {
		hooks = append(hooks, st.OnEnter, st.OnComplete)
		for _, n := range st.Nodes {
			hooks = append(hooks, n.Hook)
		}
	}
	return hooks
}
