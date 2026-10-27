package pipeline

import (
	"context"
	"testing"
)

func TestPaymentGateRequiresTrustedSettlement(t *testing.T) {
	def := &Definition{Name: "paid", Payment: &PaymentSpec{Currency: "NPR", Amount: 50000, VerifierRoles: []string{"cashier"}}, Stages: []Stage{{Name: "application", Public: true, Actions: []ActionSpec{{Name: "submit", Outcome: OutcomeAdvance}}}}}
	compiled, err := Compile(def)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(compiled)
	ctx := context.Background()
	applicant := Actor{ID: "applicant"}
	c, err := e.Start(ctx, applicant, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Act(ctx, c, applicant, "application", "submit", ActInput{}); err == nil {
		t.Fatal("completed without payment")
	}
	if _, err := e.RecordPayment(ctx, c, applicant, "provider-1", 50000, "NPR"); err == nil {
		t.Fatal("applicant marked case paid")
	}
	cashier := Actor{ID: "cashier-1", Roles: []string{"cashier"}}
	if _, err := e.RecordPayment(ctx, c, cashier, "provider-1", 49999, "NPR"); err == nil {
		t.Fatal("accepted wrong amount")
	}
	paid, err := e.RecordPayment(ctx, c, cashier, "provider-1", 50000, "NPR")
	if err != nil {
		t.Fatal(err)
	}
	complete, err := e.Act(ctx, paid, applicant, "application", "submit", ActInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !complete.Terminal() || complete.Payment.Reference != "provider-1" {
		t.Fatalf("case: %+v", complete)
	}
}
