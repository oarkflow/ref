package platform

import (
	"fmt"
	"strings"

	"github.com/oarkflow/ref/platform/spi"
)

// registerSMSActions registers SMS-specific actions in the platform registry.
func registerSMSActions(r *Registry) {
	mustAction(r, "sms.send", smsSendAction, ActionInfo{
		Family:       "service",
		Summary:      "Submit an SMS message through an sms.router or spi.SMSProvider with automatic routing and failover",
		ResourceKind: "sms.router",
		Provides:     "{ ok, provider_id, provider_message_id, segments, latency_ms, attempts, error { kind, protocol, status, code, message, retryable } }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "to", Type: "template", Summary: "E.164 destination address"},
			{Name: "to_fact", Type: "fact", Summary: "Fact holding the destination address"},
			{Name: "from", Type: "template", Summary: "Originator / sender ID"},
			{Name: "from_fact", Type: "fact", Summary: "Fact holding sender ID"},
			{Name: "text", Type: "template", Summary: "Message body"},
			{Name: "text_fact", Type: "fact", Summary: "Fact holding message body"},
			{Name: "dlr", Type: "bool", Default: "true", Summary: "Request delivery receipt"},
			{Name: "dlr_job_type", Type: "string", Default: "sms.dlr", Summary: "Job type when publishing receipt to queue"},
			{Name: "country", Type: "template", Summary: "ISO country code hint (e.g. NP, US)"},
			{Name: "country_fact", Type: "fact", Summary: "Fact holding ISO country code"},
			{Name: "mno", Type: "template", Summary: "Mobile network operator hint"},
			{Name: "mno_fact", Type: "fact", Summary: "Fact holding MNO"},
			{Name: "tier", Type: "int", Summary: "Priority tier hint (1 = highest)"},
			{Name: "priority", Type: "int", Summary: "Priority score hint"},
			{Name: "tags", Type: "map", Summary: "Additional key-value metadata to pass with the message"},
			{Name: "capture", Type: "bool", Default: "true", Summary: "Publish outcome as data rather than failing the intent"},
		},
	})

	mustAction(r, "sms.record_dlr", smsRecordDLRAction, ActionInfo{
		Family:       "service",
		Summary:      "Record delivery receipt feedback into an sms.router resource for adaptive health scoring",
		ResourceKind: "sms.router",
		Provides:     "{ recorded, provider_id, status }",
		Kind:         "effect",
		Config: []ConfigField{
			{Name: "provider_id", Type: "template"},
			{Name: "provider_id_fact", Type: "fact"},
			{Name: "status", Type: "template", Summary: "DELIVRD, UNDELIV, REJECTD, etc."},
			{Name: "status_fact", Type: "fact"},
			{Name: "latency_ms", Type: "int"},
			{Name: "latency_ms_fact", Type: "fact"},
			{Name: "dlr_fact", Type: "fact", Summary: "Fact holding raw DLR object to extract status and provider from"},
		},
	})
}

var smsSendAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	// Resolve target resource: could be sms.router or an SMSProvider
	res, err := requireResource[any](build, spec, "an sms.router or SMSProvider resource")
	if err != nil {
		return nil, err
	}

	router, isRouter := res.(*SMSRouter)
	provider, isProvider := res.(spi.SMSProvider)
	if !isRouter && !isProvider {
		return nil, fmt.Errorf("node %q: resource %q must implement spi.SMSProvider or be an sms.router", spec.Name, spec.Resource)
	}

	toTmpl, _ := configTemplate(spec.Config, "to", "")
	toFact := configString(spec.Config, "to_fact", "")
	fromTmpl, _ := configTemplate(spec.Config, "from", "")
	fromFact := configString(spec.Config, "from_fact", "")
	textTmpl, _ := configTemplate(spec.Config, "text", "")
	textFact := configString(spec.Config, "text_fact", "")

	countryTmpl, _ := configTemplate(spec.Config, "country", "")
	countryFact := configString(spec.Config, "country_fact", "")
	mnoTmpl, _ := configTemplate(spec.Config, "mno", "")
	mnoFact := configString(spec.Config, "mno_fact", "")

	customerTierTmpl, _ := configTemplate(spec.Config, "customer_tier", "")
	customerTierFact := configString(spec.Config, "customer_tier_fact", "")
	trafficTypeTmpl, _ := configTemplate(spec.Config, "traffic_type", "")
	trafficTypeFact := configString(spec.Config, "traffic_type_fact", "")
	encodingTmpl, _ := configTemplate(spec.Config, "encoding", "")
	encodingFact := configString(spec.Config, "encoding_fact", "")
	providerFailedTmpl, _ := configTemplate(spec.Config, "provider_failed", "")
	providerFailedFact := configString(spec.Config, "provider_failed_fact", "")
	hintsFact := configString(spec.Config, "hints_fact", "")

	tier, _ := configInt(spec.Config, "tier", 0)
	priority, _ := configInt(spec.Config, "priority", 0)

	dlr := configBool(spec.Config, "dlr", true)
	dlrJobType := configString(spec.Config, "dlr_job_type", "sms.dlr")
	capture := configBool(spec.Config, "capture", true)

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)

		// 1. Resolve 'to'
		to := ""
		if toFact != "" {
			if val, ok := resolvePath(ctx.Inputs, toFact); ok {
				to = fmt.Sprintf("%v", val)
			}
		}
		if to == "" && toTmpl != nil {
			rendered, err := toTmpl.Render(env)
			if err == nil {
				to = strings.TrimSpace(rendered)
			}
		}
		if to == "" {
			return ActionResult{}, invalidInput("sms.send requires 'to' or 'to_fact'")
		}

		// 2. Resolve 'from'
		from := ""
		if fromFact != "" {
			if val, ok := resolvePath(ctx.Inputs, fromFact); ok {
				from = fmt.Sprintf("%v", val)
			}
		}
		if from == "" && fromTmpl != nil {
			rendered, err := fromTmpl.Render(env)
			if err == nil {
				from = strings.TrimSpace(rendered)
			}
		}

		// 3. Resolve 'text'
		text := ""
		if textFact != "" {
			if val, ok := resolvePath(ctx.Inputs, textFact); ok {
				text = fmt.Sprintf("%v", val)
			}
		}
		if text == "" && textTmpl != nil {
			rendered, err := textTmpl.Render(env)
			if err == nil {
				text = rendered
			}
		}
		if text == "" {
			return ActionResult{}, invalidInput("sms.send requires 'text' or 'text_fact'")
		}

		// 4. Resolve hints: country, mno, customer_tier, traffic_type, encoding, provider_failed, hints_fact
		hints := make(map[string]any)

		// Pre-populate from message map if available in inputs
		if msgInput, ok := ctx.Inputs["message"].(map[string]any); ok {
			for _, k := range []string{"country", "mno", "customer_tier", "traffic_type", "encoding", "provider_failed", "tier", "priority", "max_cost"} {
				if v, exists := msgInput[k]; exists && v != nil && v != "" {
					hints[k] = v
				}
			}
		}

		// Pre-populate from hints_fact if specified
		if hintsFact != "" {
			if hVal, ok := resolvePath(ctx.Inputs, hintsFact); ok {
				if hMap, ok := hVal.(map[string]any); ok {
					for k, v := range hMap {
						hints[k] = v
					}
				}
			}
		}

		if countryFact != "" {
			if val, ok := resolvePath(ctx.Inputs, countryFact); ok {
				hints["country"] = fmt.Sprintf("%v", val)
			}
		}
		if _, ok := hints["country"]; !ok && countryTmpl != nil {
			if c, err := countryTmpl.Render(env); err == nil && c != "" {
				hints["country"] = strings.TrimSpace(c)
			}
		}

		if mnoFact != "" {
			if val, ok := resolvePath(ctx.Inputs, mnoFact); ok {
				hints["mno"] = fmt.Sprintf("%v", val)
			}
		}
		if _, ok := hints["mno"]; !ok && mnoTmpl != nil {
			if m, err := mnoTmpl.Render(env); err == nil && m != "" {
				hints["mno"] = strings.TrimSpace(m)
			}
		}

		if customerTierFact != "" {
			if val, ok := resolvePath(ctx.Inputs, customerTierFact); ok {
				hints["customer_tier"] = fmt.Sprintf("%v", val)
			}
		}
		if _, ok := hints["customer_tier"]; !ok && customerTierTmpl != nil {
			if ct, err := customerTierTmpl.Render(env); err == nil && ct != "" {
				hints["customer_tier"] = strings.TrimSpace(ct)
			}
		}

		if trafficTypeFact != "" {
			if val, ok := resolvePath(ctx.Inputs, trafficTypeFact); ok {
				hints["traffic_type"] = fmt.Sprintf("%v", val)
			}
		}
		if _, ok := hints["traffic_type"]; !ok && trafficTypeTmpl != nil {
			if tt, err := trafficTypeTmpl.Render(env); err == nil && tt != "" {
				hints["traffic_type"] = strings.TrimSpace(tt)
			}
		}

		if encodingFact != "" {
			if val, ok := resolvePath(ctx.Inputs, encodingFact); ok {
				hints["encoding"] = fmt.Sprintf("%v", val)
			}
		}
		if _, ok := hints["encoding"]; !ok && encodingTmpl != nil {
			if enc, err := encodingTmpl.Render(env); err == nil && enc != "" {
				hints["encoding"] = strings.TrimSpace(enc)
			}
		}

		if providerFailedFact != "" {
			if val, ok := resolvePath(ctx.Inputs, providerFailedFact); ok {
				hints["provider_failed"] = fmt.Sprintf("%v", val)
			}
		}
		if _, ok := hints["provider_failed"]; !ok && providerFailedTmpl != nil {
			if pf, err := providerFailedTmpl.Render(env); err == nil && pf != "" {
				hints["provider_failed"] = strings.TrimSpace(pf)
			}
		}

		if tier > 0 {
			hints["tier"] = tier
		}
		if priority > 0 {
			hints["priority"] = priority
		}


		msg := spi.SMSMessage{
			From:       from,
			To:         to,
			Text:       text,
			DLR:        dlr,
			DLRJobType: dlrJobType,
			Tags:       make(map[string]string),
		}

		var res spi.SMSResult
		var submitErr error

		if isRouter {
			res, submitErr = router.RouteAndSubmit(ctx.Context, msg, hints)
		} else {
			res, submitErr = provider.Submit(ctx.Context, msg)
		}

		if submitErr != nil {
			if !capture {
				return ActionResult{}, unavailable("sms submission error: %v", submitErr)
			}
			res = spi.SMSResult{
				OK:         false,
				ProviderID: spec.Resource,
				Error: &spi.SMSError{
					Kind:      "internal",
					Message:   submitErr.Error(),
					Retryable: false,
				},
			}
		}

		out := map[string]any{
			"ok":                  res.OK,
			"provider":            res.ProviderID,
			"provider_id":         res.ProviderID,
			"provider_message_id": res.ProviderMsgID,
			"segments":            res.Segments,
			"latency_ms":          res.LatencyMs,
			"attempts":            res.Attempts,
		}

		if res.Error != nil {
			out["error"] = map[string]any{
				"kind":        res.Error.Kind,
				"protocol":    res.Error.Protocol,
				"status":      res.Error.StatusCode,
				"code":        res.Error.Code,
				"message":     res.Error.Message,
				"retryable":   res.Error.Retryable,
			}
		}

		if !res.OK && !capture {
			msg := "sms delivery failed"
			if res.Error != nil {
				msg = fmt.Sprintf("sms delivery failed: %s (%s)", res.Error.Message, res.Error.Code)
			}
			return ActionResult{}, unavailable("%s", msg)
		}

		return ActionResult{Outputs: map[string]any{provides: out}}, nil
	}), nil
})

var smsRecordDLRAction = ActionFactoryFunc(func(build BuildContext, spec NodeSpec) (Action, error) {
	if err := exactlyOneOutput(spec); err != nil {
		return nil, err
	}
	provides := spec.Provides[0]

	router, err := requireResource[*SMSRouter](build, spec, "an sms.router resource")
	if err != nil {
		return nil, err
	}

	providerTmpl, _ := configTemplate(spec.Config, "provider_id", "")
	providerFact := configString(spec.Config, "provider_id_fact", "")
	statusTmpl, _ := configTemplate(spec.Config, "status", "")
	statusFact := configString(spec.Config, "status_fact", "")
	dlrFact := configString(spec.Config, "dlr_fact", "")
	latencyFact := configString(spec.Config, "latency_ms_fact", "")

	return ActionFunc(func(ctx *ActionContext) (ActionResult, error) {
		env := actionEnv(ctx)

		providerID := ""
		status := ""
		var latencyMs int64

		if dlrFact != "" {
			if val, ok := resolvePath(ctx.Inputs, dlrFact); ok {
				if m, ok := val.(map[string]any); ok {
					if pid, ok := m["provider_id"].(string); ok {
						providerID = pid
					}
					if st, ok := m["status"].(string); ok {
						status = st
					}
					if lat, ok := m["latency_ms"]; ok {
						latencyMs = int64(toInt(lat))
					}
				}
			}
		}

		if providerID == "" && providerFact != "" {
			if val, ok := resolvePath(ctx.Inputs, providerFact); ok {
				providerID = fmt.Sprintf("%v", val)
			}
		}
		if providerID == "" && providerTmpl != nil {
			providerID, _ = providerTmpl.Render(env)
		}

		if status == "" && statusFact != "" {
			if val, ok := resolvePath(ctx.Inputs, statusFact); ok {
				status = fmt.Sprintf("%v", val)
			}
		}
		if status == "" && statusTmpl != nil {
			status, _ = statusTmpl.Render(env)
		}

		if latencyMs == 0 && latencyFact != "" {
			if val, ok := resolvePath(ctx.Inputs, latencyFact); ok {
				latencyMs = int64(toInt(val))
			}
		}

		if providerID != "" && status != "" {
			router.RecordDLR(ctx.Context, strings.TrimSpace(providerID), strings.TrimSpace(status), latencyMs)
		}

		return ActionResult{
			Outputs: map[string]any{
				provides: map[string]any{
					"recorded":    true,
					"provider_id": providerID,
					"status":      status,
				},
			},
		}, nil
	}), nil
})
