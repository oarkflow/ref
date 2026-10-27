package pipeline

import (
	"context"
	"strings"
)

// RecordPayment accepts a settled provider reference from a trusted verifier.
// Public applicants cannot mark their own case paid.
func (e *Engine) RecordPayment(_ context.Context, in *Case, actor Actor, reference string, amount int64, currency string) (*Case, error) {
	spec := e.C.Def.Payment
	if spec == nil {
		return nil, badState("this pipeline has no payment requirement")
	}
	if actor.ID == "" || actor.Link != nil || !actor.HasAnyRole(spec.VerifierRoles) {
		return nil, forbidden("you may not verify a payment")
	}
	if in.Terminal() {
		return nil, badState("this case is closed")
	}
	reference = strings.TrimSpace(reference)
	if reference == "" || amount <= 0 || currency != spec.Currency || (spec.Amount > 0 && amount != spec.Amount) {
		return nil, badState("payment reference, amount or currency is invalid")
	}
	if in.Payment != nil {
		return nil, badState("payment has already been recorded")
	}
	c := in.Clone()
	now := e.now()
	c.Payment = &PaymentRecord{Reference: reference, Amount: amount, Currency: currency, VerifiedBy: actor.ID, VerifiedAt: now}
	c.History = append(c.History, Entry{At: now, Actor: actor.ID, Stage: c.Stage, Action: "payment.verified"})
	c.UpdatedAt = now
	return c, nil
}
