// Package stages holds pipeline stages that are not part of the core.
//
// It exists to show how a stage is added. The core knows nothing about this
// package: a stage is a Go function registered with
// platform.RegisterActionDriver, and it joins a pipeline when the BCL puts a
// node for it between two others. This stage reads the draft message and
// refuses it when its text matches a deny pattern; the patterns, and which
// message types they apply to, are BCL config on the node:
//
//	node "content" {
//	  uses "sms.content_policy"
//	  kind read
//	  requires [draft]
//	  provides [vetted]
//	  config { types ["promotional"] deny ["(?i)\\blottery\\b"] message "Gambling content is not allowed." }
//	}
//
// and `route` and `accept` then read `vetted` instead of `draft`
// (config { draft_fact "vetted" }).
package stages

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/oarkflow/ref/intent"
	"github.com/oarkflow/ref/platform"

	"github.com/oarkflow/ref/examples/smsgateway/internal/sms"
)

var once sync.Once

// Register installs the stages. It is safe to call more than once.
func Register() {
	once.Do(func() {
		platform.RegisterActionDriver("sms.content_policy", platform.ActionFactoryFunc(buildContentPolicy), platform.ActionInfo{
			Family:  "sms",
			Summary: "Refuse messages whose text matches a deny pattern",
			Kind:    "read", Provides: "The draft, unchanged",
			Config: []platform.ConfigField{
				{Name: "deny", Type: "[]string", Required: true, Summary: "Go regular expressions"},
				{Name: "types", Type: "[]string", Summary: "Message types the policy applies to; empty means all"},
				{Name: "message", Type: "string", Summary: "What the caller is told"},
				{Name: "draft_fact", Type: "fact", Default: "draft"},
			},
		})
	})
}

func buildContentPolicy(_ platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
	var patterns []*regexp.Regexp
	for _, raw := range stringList(spec.Config["deny"]) {
		re, err := regexp.Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("node %q: deny pattern %q: %w", spec.Name, raw, err)
		}
		patterns = append(patterns, re)
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("node %q: sms.content_policy needs config.deny", spec.Name)
	}
	types := stringList(spec.Config["types"])
	message, _ := spec.Config["message"].(string)
	if message == "" {
		message = "the message text is not allowed"
	}
	fact, _ := spec.Config["draft_fact"].(string)
	if fact == "" {
		fact = "draft"
	}
	return platform.ActionFunc(func(c *platform.ActionContext) (platform.ActionResult, error) {
		d, ok := c.Inputs[fact].(*sms.Draft)
		if !ok {
			return platform.ActionResult{}, intent.Failure{Code: "INTERNAL_ERROR", Category: intent.CategoryInternal,
				Message: fmt.Sprintf("stage input %q is missing or is not a draft message", fact)}
		}
		if len(types) == 0 || slices.Contains(types, d.Type) {
			for _, re := range patterns {
				if re.MatchString(d.Text) {
					return platform.ActionResult{}, intent.Failure{Code: "CONTENT_BLOCKED", Category: intent.CategoryInvalidInput, Message: message}
				}
			}
		}
		if len(spec.Provides) == 0 {
			return platform.ActionResult{}, nil
		}
		return platform.ActionResult{Outputs: map[string]any{spec.Provides[0]: d}}, nil
	}), nil
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}
