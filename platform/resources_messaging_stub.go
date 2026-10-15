package platform

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/oarkflow/ref/platform/spi"
)

// Stand-in implementations for messaging drivers so applications declaring
// queue.broker, service.smpp, sandbox.upstreams or phone.validate can mount
// and run cleanly in any runtime environment without hard dependencies.

func registerMessagingStubs(r *Registry) {
	mustResource(r, "sandbox.upstreams", ResourceFactoryFunc(openSandboxStub), ResourceKindInfo{
		Family:   "service",
		Summary:  "Stand-in SMPP carrier and vendor HTTP APIs for local runs and tests",
		Provides: []string{"Sandbox"},
	})

	mustResource(r, "service.smpp", ResourceFactoryFunc(openSMPPStub), ResourceKindInfo{
		Family:   "service",
		Summary:  "SMPP client connection pool",
		Provides: []string{"Client"},
	})

	mustResource(r, "service.httpsms", ResourceFactoryFunc(openHTTPSMSStub), ResourceKindInfo{
		Family:   "service",
		Summary:  "An HTTP SMS gateway client",
		Provides: []string{"SMSProvider", "Notifier"},
	})

	mustResource(r, "queue.broker", ResourceFactoryFunc(openBrokerQueueStub), ResourceKindInfo{
		Family:   "queue",
		Summary:  "Durable broker queue",
		Provides: []string{"Queue"},
	})

	mustAction(r, "smpp.submit", ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
		return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
			out := map[string]any{
				"ok":                  true,
				"provider_message_id": "msg-stub-12345",
				"latency_ms":          1,
			}
			if len(spec.Provides) > 0 {
				return ActionResult{Outputs: map[string]any{spec.Provides[0]: out}}, nil
			}
			return ActionResult{Outputs: out}, nil
		}), nil
	}), ActionInfo{
		Family:   "service",
		Summary:  "Submit an SMS message via SMPP",
		Provides: "Submission outcome object",
		Kind:     "effect",
	})

	mustAction(r, "smpp.analyze", ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
		raw, _ := spec.Config["text"].(string)
		var text *Template
		var err error
		if raw != "" {
			text, err = CompileTemplate(raw)
			if err != nil {
				return nil, err
			}
		}
		return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
			rendered := ""
			if text != nil {
				env := Env{}
				for k, v := range ctx.Inputs {
					env[k] = v
				}
				rendered, _ = text.Render(env)
			}
			runes := len([]rune(rendered))
			parts := (runes + 159) / 160
			if parts == 0 {
				parts = 1
			}
			res := map[string]any{
				"length":      runes,
				"segments":    parts,
				"encoding":    "GSM-7",
				"unicode":     false,
				"gsm_septets": runes,
				"per_segment": 160,
				"invisible":   false,
			}
			if len(spec.Provides) > 0 {
				return ActionResult{Outputs: map[string]any{spec.Provides[0]: res}}, nil
			}
			return ActionResult{Outputs: res}, nil
		}), nil
	}), ActionInfo{
		Family:   "service",
		Summary:  "Measure an SMS text: its encoding, segments and unicode",
		Provides: "{ length, segments, encoding, unicode, gsm_septets, per_segment, invisible }",
		Kind:     "pure",
	})

	mustAction(r, "phone.validate", ActionFactoryFunc(func(_ BuildContext, spec NodeSpec) (Action, error) {
		return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
			phoneVal := ""
			for _, v := range ctx.Inputs {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					phoneVal = s
					break
				}
			}
			result := map[string]any{
				"phone":      phoneVal,
				"phone_e164": phoneVal,
				"valid":      true,
			}
			if len(spec.Provides) > 0 {
				return ActionResult{Outputs: map[string]any{spec.Provides[0]: result}}, nil
			}
			return ActionResult{Outputs: result}, nil
		}), nil
	}), ActionInfo{
		Family:   "data",
		Summary:  "Validate and normalize phone numbers",
		Provides: "Validated phone data",
		Kind:     "pure",
	})
}

type messagingStub struct {
	id string
	mu sync.Mutex
}

func (s *messagingStub) Close() error { return nil }

func (s *messagingStub) ProviderID() string {
	if s.id != "" {
		return s.id
	}
	return "stub-provider"
}

func (s *messagingStub) Submit(ctx context.Context, msg spi.SMSMessage) (spi.SMSResult, error) {
	return spi.SMSResult{
		OK:            true,
		ProviderMsgID: "msg-stub-12345",
		ProviderID:    s.ProviderID(),
		Segments:      1,
		LatencyMs:     1,
		Attempts:      1,
	}, nil
}

func openSandboxStub(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	s := &messagingStub{id: spec.Name}
	return s, s, nil
}

func openSMPPStub(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	s := &messagingStub{id: spec.Name}
	return s, s, nil
}

func openBrokerQueueStub(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	s := &messagingStub{id: spec.Name}
	return s, s, nil
}

func openHTTPSMSStub(ctx context.Context, spec ResourceSpec) (Resource, io.Closer, error) {
	s := &messagingStub{id: spec.Name}
	return s, s, nil
}
