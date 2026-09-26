package platform

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/oarkflow/ref/pipeline"
)

// Notifications of a pipeline.cases resource. A pipeline's notify rules turn
// committed events into notifications; they are planned when the outbox
// dispatcher delivers the event (under each recipient's preferences), stored
// durably beside the cases, and delivered by the resource's background loop
// once due: now, at the end of the recipient's quiet hours, or when their
// digest window closes. Each channel is delivered by an intent named in the
// resource's notify_channels.

// configureChannels reads notify_channels and checks every notify rule uses
// a configured channel.
func (p *PipelineCases) configureChannels(spec ResourceSpec) error {
	p.channels = map[string]string{}
	for name, v := range configMap(spec.Config, "notify_channels") {
		intentName := strings.TrimSpace(Stringify(v))
		if !validChannelName(name) || intentName == "" {
			return fmt.Errorf("pipeline.cases %q: notify_channels.%s must name the intent that delivers it", spec.Name, name)
		}
		p.channels[name] = intentName
	}
	for _, name := range p.order {
		if err := checkNotifyChannels(name, p.engines[name].C.Def, p.channels); err != nil {
			return fmt.Errorf("pipeline.cases %q: %w", spec.Name, err)
		}
	}
	return nil
}

func validChannelName(s string) bool { return identRe.MatchString(s) }

// checkNotifyChannels reports notify rules naming channels the resource
// does not deliver.
func checkNotifyChannels(pipelineName string, def *pipeline.Definition, channels map[string]string) error {
	if len(def.Notify) > 0 && len(channels) == 0 {
		return fmt.Errorf("pipeline %q declares notify rules: set notify_channels", pipelineName)
	}
	for _, rule := range def.Notify {
		for _, ch := range rule.Channels {
			if _, ok := channels[ch]; !ok {
				return fmt.Errorf("pipeline %q: notify %q uses channel %q, which notify_channels does not declare", pipelineName, rule.Event, ch)
			}
		}
	}
	return nil
}

// useNotify enables notifications when any pipeline declares notify rules.
func (p *PipelineCases) useNotify(store pipeline.NotifyStore) {
	for _, name := range p.order {
		if len(p.engines[name].C.Def.Notify) > 0 {
			p.notify = store
			return
		}
	}
}

func (p *PipelineCases) channelNames() []string {
	names := make([]string, 0, len(p.channels))
	for name := range p.channels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// planNotifications stores the notifications an outbox event produces.
func (p *PipelineCases) planNotifications(ctx context.Context, e *pipeline.Engine, c *pipeline.Case, ev pipeline.OutboxEvent) error {
	if p.notify == nil || len(e.C.Def.Notify) == 0 {
		return nil
	}
	items, err := e.PlanNotifications(ctx, c, ev.ID, ev.Event, p.channelNames(), func(user string) (*pipeline.NotifyPreferences, error) {
		return p.notify.Preferences(ctx, c.TenantID, user)
	}, p.now())
	if err != nil {
		return err
	}
	return p.notify.EnqueueNotifications(ctx, items)
}

// flushNotifications delivers every notification that is due.
func (p *PipelineCases) flushNotifications(ctx context.Context, platform *Platform) {
	for {
		n, err := pipeline.FlushNotifications(ctx, p.notify, p.now(), p.maxAttempts, func(ctx context.Context, b pipeline.NotificationBatch) error {
			return p.sendNotification(ctx, platform, b)
		})
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("pipeline notification flush failed", "resource", p.name, "error", err)
			}
			return
		}
		if n == 0 {
			return
		}
	}
}

var severityRank = map[string]int{pipeline.SeverityInfo: 0, pipeline.SeverityWarning: 1, pipeline.SeverityUrgent: 2, pipeline.SeverityCritical: 3}

// sendNotification runs the channel's intent with one message: a single
// notification or a digest.
func (p *PipelineCases) sendNotification(ctx context.Context, platform *Platform, b pipeline.NotificationBatch) error {
	intentName, ok := p.channels[b.Channel]
	if !ok {
		return fmt.Errorf("channel %q is not configured", b.Channel)
	}
	severity := pipeline.SeverityInfo
	items := make([]any, len(b.Items))
	for i, n := range b.Items {
		if severityRank[n.Severity] > severityRank[severity] {
			severity = n.Severity
		}
		items[i] = map[string]any{
			"id": n.ID, "event": n.Event, "stage": n.Stage, "severity": n.Severity, "subject": n.Subject, "body": n.Body,
			"case": map[string]any{"id": n.CaseID, "number": n.CaseNumber, "pipeline": n.Pipeline},
			"at":   n.At.Format(time.RFC3339), "deferred": n.Deferred,
		}
	}
	input := map[string]any{
		"channel": b.Channel, "user": b.User, "tenant_id": b.TenantID, "digest": b.Digest, "count": len(b.Items),
		"severity": severity, "subject": b.Subject(), "body": b.Body(), "items": items,
	}
	hctx, cancel := context.WithTimeout(withResidencyTenant(ctx, b.TenantID), 30*time.Second)
	defer cancel()
	if _, err := platform.CallIntent(hctx, intentName, input, nil); err != nil {
		slog.Warn("pipeline notification failed", "resource", p.name, "channel", b.Channel, "user", b.User, "error", err)
		return err
	}
	return nil
}

// notifyPrefs gets (pipeline.notify_prefs) or replaces
// (pipeline.notify_prefs_set) the caller's own notification preferences.
func (h *pipelineHandler) notifyPrefs(ctx *ActionContext) (ActionResult, error) {
	if h.res.notify == nil {
		return ActionResult{}, notFoundOrMessage("no pipeline of this resource sends notifications")
	}
	user := ctx.Principal.ID
	if user == "" {
		return ActionResult{}, permissionDenied("sign in to manage your notification preferences")
	}
	channels := h.res.channelNames()
	if h.op == "notify_prefs_set" {
		body, _ := h.body(ctx, "", "input").(map[string]any)
		if prefs, ok := body["preferences"].(map[string]any); ok {
			body = prefs
		}
		var prefs pipeline.NotifyPreferences
		if err := decodeInto(body, &prefs); err != nil {
			return ActionResult{}, invalidInput("the preferences are invalid: %v", err)
		}
		prefs.User, prefs.TenantID, prefs.UpdatedAt = user, ctx.TenantID, time.Now().UTC()
		if err := prefs.Validate(channels); err != nil {
			return ActionResult{}, pipelineFailure(err)
		}
		if err := h.res.notify.SetPreferences(ctx.Context, &prefs); err != nil {
			return ActionResult{}, pipelineFailure(err)
		}
	}
	prefs, err := h.res.notify.Preferences(ctx.Context, ctx.TenantID, user)
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	if prefs == nil {
		prefs = &pipeline.NotifyPreferences{User: user, TenantID: ctx.TenantID, Digest: pipeline.DigestImmediate}
	}
	pending, err := h.res.notify.PendingNotifications(ctx.Context, ctx.TenantID, user, 200)
	if err != nil {
		return ActionResult{}, pipelineFailure(err)
	}
	rows := make([]any, 0, len(pending))
	for _, n := range pending {
		rows = append(rows, map[string]any{
			"id": n.ID, "event": n.Event, "channel": n.Channel, "severity": n.Severity, "subject": n.Subject,
			"case_number": n.CaseNumber, "deliver_at": n.DeliverAt.Format(time.RFC3339), "deferred": n.Deferred,
			"digest": n.Digest != "", "attempts": n.Attempts,
		})
	}
	var events []string
	for _, name := range h.res.order {
		for _, rule := range h.res.engines[name].C.Def.Notify {
			if !slices.Contains(events, rule.Event) {
				events = append(events, rule.Event)
			}
		}
	}
	return h.out(map[string]any{"preferences": prefs, "channels": channels, "events": events, "pending": rows})
}
