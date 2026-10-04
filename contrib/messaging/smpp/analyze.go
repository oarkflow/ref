package smpp

import (
	"fmt"

	"github.com/oarkflow/smppflow/pkg/message"

	"github.com/oarkflow/ref/platform"
)

func registerAnalyze() {
	platform.RegisterActionDriver("smpp.analyze", platform.ActionFactoryFunc(buildAnalyze), platform.ActionInfo{
		Family:   "service",
		Summary:  "Measure an SMS text: its encoding (GSM-7 or UCS-2), how many segments it needs, whether it holds unicode",
		Provides: "{ length, segments, encoding, unicode, gsm_septets, per_segment, invisible }",
		Kind:     "pure",
		Config: []platform.ConfigField{
			{Name: "text", Type: "template", Required: true},
		},
	})
}

func buildAnalyze(_ platform.BuildContext, spec platform.NodeSpec) (platform.Action, error) {
	raw, _ := spec.Config["text"].(string)
	if raw == "" {
		return nil, fmt.Errorf("node %q: smpp.analyze needs config.text", spec.Name)
	}
	if len(spec.Provides) != 1 {
		return nil, fmt.Errorf("node %q: smpp.analyze provides exactly one fact", spec.Name)
	}
	text, err := platform.CompileTemplate(raw)
	if err != nil {
		return nil, fmt.Errorf("node %q: %w", spec.Name, err)
	}
	provides := spec.Provides[0]
	return platform.ActionFunc(func(ctx *platform.ActionContext) (platform.ActionResult, error) {
		env := platform.Env{}
		for k, v := range ctx.Inputs {
			env[k] = v
		}
		t, err := text.Render(env)
		if err != nil {
			return platform.ActionResult{}, err
		}
		a := message.AnalyzeText(t)
		per := a.SingleLimit
		if a.Parts > 1 {
			per = a.MultipartLimit
		}
		return platform.ActionResult{Outputs: map[string]any{provides: map[string]any{
			"length": a.Runes, "segments": max(a.Parts, 1), "encoding": string(a.Encoding),
			"unicode": a.ContainsUnicode || a.Encoding == message.EncodingUCS2, "gsm_septets": a.GSMSeptets,
			"per_segment": per, "invisible": a.ContainsInvisible,
		}}}, nil
	}), nil
}
