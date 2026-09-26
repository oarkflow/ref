package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// ErasedMarker replaces personal data removed from a case.
const ErasedMarker = "[erased]"

// SubjectRef is a one-way reference to a data subject, for erasure receipts
// that must not themselves hold the personal data.
func SubjectRef(identifiers map[string]string) string {
	keys := make([]string, 0, len(identifiers))
	for k := range identifiers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, strings.ToLower(strings.TrimSpace(identifiers[k])))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// MatchesSubject reports whether a case belongs to the data subject: every
// given identifier must be one of the pipeline's subject paths and equal the
// case's value (case-insensitively).
func (e *Engine) MatchesSubject(c *Case, identifiers map[string]string) (bool, error) {
	if len(identifiers) == 0 {
		return false, nil
	}
	for path, want := range identifiers {
		if !slices.Contains(e.C.Def.Subject, path) {
			return false, fmt.Errorf("%w: %q is not a subject identifier of %s", ErrNotFound, path, e.C.Def.Name)
		}
		got, ok := c.Get(path)
		if !ok || !strings.EqualFold(strings.TrimSpace(fmt.Sprint(got)), strings.TrimSpace(want)) {
			return false, nil
		}
	}
	return true, nil
}

// piiPaths lists the personal-data paths of the definition.
func (e *Engine) piiPaths() (single []string, repeat map[string][]string) {
	repeat = map[string][]string{}
	for name, cf := range e.C.forms {
		for _, in := range cf.inputs {
			if !in.PII && !slices.Contains(e.C.Def.Subject, name+"."+in.Name) {
				continue
			}
			if cf.Repeatable {
				repeat[name] = append(repeat[name], in.Name)
			} else {
				single = append(single, name+"."+in.Name)
			}
		}
	}
	sort.Strings(single)
	return single, repeat
}

// Anonymize removes the case's personal data (pii inputs and subject paths),
// scrubs those values from notes and history, and drops sealed values and
// external links. Certificates are kept unchanged: they are records the law
// usually requires to be kept, and altering them would break verification.
// A case under legal hold cannot be anonymised.
func (e *Engine) Anonymize(ctx context.Context, in *Case, actor Actor, reason string) (*Case, error) {
	c := in.Clone()
	if c.Hold != nil {
		return nil, badState("the case is under legal hold: %s", c.Hold.Reason)
	}
	if c.Erased != nil {
		return nil, badState("the case was already anonymised")
	}
	e.anonymize(c, actor.ID, reason, e.now())
	return c, nil
}

func (e *Engine) anonymize(c *Case, actor, reason string, now time.Time) {
	single, repeat := e.piiPaths()
	var removed []string
	for _, path := range single {
		if v, ok := c.Get(path); ok && !isEmpty(v) {
			removed = append(removed, fmt.Sprint(v))
			c.Set(path, ErasedMarker)
		}
	}
	for form, names := range repeat {
		list, _ := c.Data[form].([]any)
		for _, raw := range list {
			entry, _ := raw.(map[string]any)
			for _, n := range names {
				if v, ok := entry[n]; ok && !isEmpty(v) {
					removed = append(removed, fmt.Sprint(v))
					entry[n] = ErasedMarker
				}
			}
		}
	}
	// Snapshots kept for diff reviews hold the same personal data.
	for i := range c.Snapshots {
		snap := &Case{Data: c.Snapshots[i].Data}
		for _, path := range single {
			if v, ok := snap.Get(path); ok && !isEmpty(v) {
				snap.Set(path, ErasedMarker)
			}
		}
		for form, names := range repeat {
			for _, raw := range asList(snap.Data[form]) {
				entry, _ := raw.(map[string]any)
				for _, n := range names {
					if v, ok := entry[n]; ok && !isEmpty(v) {
						entry[n] = ErasedMarker
					}
				}
			}
		}
	}
	scrub := func(s string) string {
		for _, v := range removed {
			if len(v) >= 3 {
				s = strings.ReplaceAll(s, v, ErasedMarker)
			}
		}
		return s
	}
	for i := range c.Notes {
		c.Notes[i].Body = scrub(c.Notes[i].Body)
	}
	for i := range c.History {
		c.History[i].Comment = scrub(c.History[i].Comment)
	}
	for _, ss := range c.Stages {
		for k, f := range ss.Flags {
			f.Comment = scrub(f.Comment)
			ss.Flags[k] = f
		}
		for _, ns := range ss.Nodes {
			ns.Comment = scrub(ns.Comment)
			for k, v := range ns.Verdicts {
				v.Comment = scrub(v.Comment)
				ns.Verdicts[k] = v
			}
		}
	}
	c.Sealed = nil
	c.Links = nil
	c.AccessKeyHash = ""
	c.Erased = &now
	c.History = append(c.History, Entry{At: now, Actor: actor, Action: "anonymized", Comment: reason})
	c.emit("erased", c.Stage, actor, now, map[string]any{"fields": len(removed)})
	c.UpdatedAt = now
}

// PlaceHold puts the case under legal hold; ReleaseHold lifts it.
func (e *Engine) PlaceHold(ctx context.Context, in *Case, actor Actor, reason string) (*Case, error) {
	c := in.Clone()
	if strings.TrimSpace(reason) == "" {
		return nil, &ValidationError{Message: "a reason is required", Fields: []FieldError{{Path: "reason", Rule: "required", Message: "say why the case is held"}}}
	}
	if c.Hold != nil {
		return nil, badState("the case is already under legal hold")
	}
	now := e.now()
	c.Hold = &LegalHold{Reason: reason, PlacedBy: actor.ID, At: now}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Action: "legal_hold", Comment: reason})
	c.UpdatedAt = now
	return c, nil
}

// ReleaseHold lifts a legal hold.
func (e *Engine) ReleaseHold(ctx context.Context, in *Case, actor Actor, comment string) (*Case, error) {
	c := in.Clone()
	if c.Hold == nil {
		return nil, badState("the case is not under legal hold")
	}
	now := e.now()
	c.Hold = nil
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Action: "legal_hold_released", Comment: comment})
	c.UpdatedAt = now
	return c, nil
}
