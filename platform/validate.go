package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"

	"github.com/oarkflow/bcl"
	"github.com/oarkflow/ref/pipeline"
)

// ValidationReport is the result of statically checking an application
// document: everything Compile checks before it opens a resource, plus the
// pipelines, entities, flags and every expression they declare.
type ValidationReport struct {
	Valid    bool            `json:"valid"`
	Errors   []string        `json:"errors,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	Summary  DocumentSummary `json:"summary"`
	// Document is the parsed model (not serialised), for diffing.
	Document *Document `json:"-"`
}

// DocumentSummary names what a document declares.
type DocumentSummary struct {
	Name      string   `json:"name"`
	Version   string   `json:"version,omitempty"`
	Resources []string `json:"resources"`
	Intents   []string `json:"intents"`
	Routes    []string `json:"routes"`
	Processes []string `json:"processes,omitempty"`
	Pipelines []string `json:"pipelines,omitempty"`
	Entities  []string `json:"entities,omitempty"`
	Flags     []string `json:"flags,omitempty"`
}

// Validate checks an application document without opening any resource: no
// database connection, no migration, no listener. It is what a config review
// runs before a revision may be approved; Compile still has the last word
// when the revision is activated.
//
// Secrets that cannot be resolved in this process are warnings, not errors:
// the deployment that activates the revision may have them.
func Validate(ctx context.Context, src []byte, baseDir string, opts LoadOptions) ValidationReport {
	if opts.Registry == nil {
		opts.Registry = NewRegistry()
	}
	env := opts.Env
	if env == nil {
		env = os.LookupEnv
	}
	var r ValidationReport
	// Missing environment values must not abort parsing (env.required would):
	// record them and substitute a placeholder.
	lenient := func(name string) (string, bool) {
		if v, ok := env(name); ok {
			return v, true
		}
		r.Warnings = append(r.Warnings, fmt.Sprintf("environment variable %s is not set here", name))
		return "validation-placeholder-" + name, true
	}
	var doc Document
	if err := bcl.UnmarshalWithOptions(src, &doc, &bcl.Options{
		Profile: opts.Profile, BaseDir: baseDir, AllowEnv: opts.AllowEnv, Env: lenient,
		ResolveImports: opts.ResolveImports, Strict: opts.Strict,
	}); err != nil {
		r.Errors = append(r.Errors, fmt.Sprintf("parse: %v", err))
		return r
	}
	fail := func(err error) {
		if err != nil {
			r.Errors = append(r.Errors, err.Error())
		}
	}
	for _, s := range doc.Secrets {
		if s.Required && s.Value == "" && s.Env != "" {
			if _, ok := env(s.Env); !ok {
				r.Warnings = append(r.Warnings, fmt.Sprintf("secret %s: environment variable %s is not set here", s.Name, s.Env))
			}
		}
	}
	_, err := compileSchemas(doc.Shapes)
	fail(err)
	fail(validateRoles(doc.Roles))
	doc = applyFamilyDefaults(doc, opts.Registry)
	expanded, err := expandEntities(doc)
	fail(err)
	if err == nil {
		doc = expanded
	}
	fail(validateDocument(doc, opts.Registry))
	for _, res := range doc.Resources {
		if _, ok := opts.Registry.resource(res.Kind); !ok {
			r.Errors = append(r.Errors, fmt.Sprintf("resource %q uses unregistered kind %q", res.Name, res.Kind))
		}
	}
	for i := range doc.Pipelines {
		compiled, err := pipeline.Compile(&doc.Pipelines[i])
		if err != nil {
			fail(err)
			continue
		}
		for where, expr := range compiled.Expressions() {
			if _, err := CompileExpr(expr); err != nil {
				r.Errors = append(r.Errors, fmt.Sprintf("pipeline %s: %s: %v", doc.Pipelines[i].Name, where, err))
			}
		}
	}
	r.Errors = append(r.Errors, validateNotifyChannels(doc)...)
	flagDoc := doc
	flagDoc.FlagStore = "" // the store is a resource: checked when it opens
	_, err = compileFlags(flagDoc, nil)
	fail(err)

	sort.Strings(r.Errors)
	r.Valid = len(r.Errors) == 0
	r.Summary = summarize(doc)
	r.Document = &doc
	_ = ctx
	return r
}

// validateNotifyChannels checks every pipeline.cases resource delivers the
// channels its pipelines' notify rules use, through intents that exist.
func validateNotifyChannels(doc Document) []string {
	var errs []string
	intents := map[string]bool{}
	for _, in := range doc.Intents {
		intents[in.Name] = true
	}
	for _, res := range doc.Resources {
		if res.Kind != "pipeline.cases" {
			continue
		}
		channels := map[string]string{}
		for name, v := range configMap(res.Config, "notify_channels") {
			channels[name] = Stringify(v)
			if !intents[Stringify(v)] {
				errs = append(errs, fmt.Sprintf("resource %q: notify_channels.%s names undeclared intent %q", res.Name, name, Stringify(v)))
			}
		}
		names := configStrings(res.Config, "pipelines")
		for i := range doc.Pipelines {
			def := &doc.Pipelines[i]
			if len(names) > 0 && !slices.Contains(names, def.Name) {
				continue
			}
			if err := checkNotifyChannels(def.Name, def, channels); err != nil {
				errs = append(errs, fmt.Sprintf("resource %q: %v", res.Name, err))
			}
		}
	}
	return errs
}

func summarize(doc Document) DocumentSummary {
	s := DocumentSummary{Name: doc.Name, Version: doc.Version}
	for _, x := range doc.Resources {
		s.Resources = append(s.Resources, x.Name)
	}
	for _, x := range doc.Intents {
		s.Intents = append(s.Intents, x.Name)
	}
	for _, x := range doc.Routes {
		s.Routes = append(s.Routes, x.Name)
	}
	for _, x := range doc.Processes {
		s.Processes = append(s.Processes, x.Name)
	}
	for _, x := range doc.Pipelines {
		s.Pipelines = append(s.Pipelines, x.Name)
	}
	for _, x := range doc.Entities {
		s.Entities = append(s.Entities, x.Name)
	}
	for _, x := range doc.Flags {
		s.Flags = append(s.Flags, x.Name)
	}
	return s
}

// DocumentChange is one named block added, removed or changed between two
// documents.
type DocumentChange struct {
	Kind   string `json:"kind"` // resource, intent, route, process, pipeline, entity, flag, shape, role
	Name   string `json:"name"`
	Change string `json:"change"` // added, removed, changed
}

// DiffDocuments compares two documents block by block.
func DiffDocuments(before, after *Document) []DocumentChange {
	var out []DocumentChange
	index := func(items any, name func(i int) string, n int) map[string]string {
		m := map[string]string{}
		raw, _ := json.Marshal(items)
		var list []json.RawMessage
		_ = json.Unmarshal(raw, &list)
		for i := 0; i < n && i < len(list); i++ {
			m[name(i)] = string(list[i])
		}
		return m
	}
	compare := func(kind string, a, b map[string]string) {
		keys := map[string]bool{}
		for k := range a {
			keys[k] = true
		}
		for k := range b {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			av, inA := a[k]
			bv, inB := b[k]
			switch {
			case !inA:
				out = append(out, DocumentChange{kind, k, "added"})
			case !inB:
				out = append(out, DocumentChange{kind, k, "removed"})
			case av != bv:
				out = append(out, DocumentChange{kind, k, "changed"})
			}
		}
	}
	if before == nil {
		before = &Document{}
	}
	if after == nil {
		after = &Document{}
	}
	compare("resource", index(before.Resources, func(i int) string { return before.Resources[i].Name }, len(before.Resources)),
		index(after.Resources, func(i int) string { return after.Resources[i].Name }, len(after.Resources)))
	compare("shape", index(before.Shapes, func(i int) string { return before.Shapes[i].Name }, len(before.Shapes)),
		index(after.Shapes, func(i int) string { return after.Shapes[i].Name }, len(after.Shapes)))
	compare("role", index(before.Roles, func(i int) string { return before.Roles[i].Name }, len(before.Roles)),
		index(after.Roles, func(i int) string { return after.Roles[i].Name }, len(after.Roles)))
	compare("intent", index(before.Intents, func(i int) string { return before.Intents[i].Name }, len(before.Intents)),
		index(after.Intents, func(i int) string { return after.Intents[i].Name }, len(after.Intents)))
	compare("route", index(before.Routes, func(i int) string { return before.Routes[i].Name }, len(before.Routes)),
		index(after.Routes, func(i int) string { return after.Routes[i].Name }, len(after.Routes)))
	compare("process", index(before.Processes, func(i int) string { return before.Processes[i].Name }, len(before.Processes)),
		index(after.Processes, func(i int) string { return after.Processes[i].Name }, len(after.Processes)))
	compare("pipeline", index(before.Pipelines, func(i int) string { return before.Pipelines[i].Name }, len(before.Pipelines)),
		index(after.Pipelines, func(i int) string { return after.Pipelines[i].Name }, len(after.Pipelines)))
	compare("entity", index(before.Entities, func(i int) string { return before.Entities[i].Name }, len(before.Entities)),
		index(after.Entities, func(i int) string { return after.Entities[i].Name }, len(after.Entities)))
	compare("flag", index(before.Flags, func(i int) string { return before.Flags[i].Name }, len(before.Flags)),
		index(after.Flags, func(i int) string { return after.Flags[i].Name }, len(after.Flags)))
	return out
}
