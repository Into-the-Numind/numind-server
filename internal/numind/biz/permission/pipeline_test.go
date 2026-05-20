package permission

import (
	"context"
	"testing"
)

func TestPipeline_NoValidators_DefaultAllow(t *testing.T) {
	p := NewPipeline()
	got := p.Check(context.Background(), PermissionRequest{})
	if got.Behavior != BehaviorAllow {
		t.Errorf("empty pipeline behavior = %s, want allow", got.Behavior)
	}
	if got.ValidatorID != "DefaultAllow" {
		t.Errorf("empty pipeline ValidatorID = %s, want DefaultAllow", got.ValidatorID)
	}
	if got.DecisionReason != DecisionReasonOther {
		t.Errorf("DecisionReason mismatch")
	}
}

func TestPipeline_AllPassthrough_DefaultAllow(t *testing.T) {
	passthroughs := []Validator{
		&stubValidator{id: "V1", result: Passthrough("V1", DecisionReasonOther, "")},
		&stubValidator{id: "V2", result: Passthrough("V2", DecisionReasonOther, "")},
		&stubValidator{id: "V3", result: Passthrough("V3", DecisionReasonOther, "")},
	}
	p := NewPipeline(passthroughs...)
	got := p.Check(context.Background(), PermissionRequest{})
	if got.Behavior != BehaviorAllow {
		t.Errorf("Behavior = %s, want allow", got.Behavior)
	}
	if got.ValidatorID != "DefaultAllow" {
		t.Errorf("ValidatorID = %s, want DefaultAllow", got.ValidatorID)
	}
}

func TestPipeline_NthDeny_EarlyTermination(t *testing.T) {
	pre := []Validator{
		&stubValidator{id: "V1", result: Passthrough("V1", DecisionReasonOther, "")},
		&stubValidator{id: "V2", result: Passthrough("V2", DecisionReasonOther, "")},
	}
	denyV := &stubValidator{id: "V3", result: Deny("V3", DecisionReasonRule, "denied")}
	post := &stubValidator{id: "V4", result: Allow("V4", DecisionReasonOther, "should not reach")}

	all := append(append(pre, denyV), post)
	p := NewPipeline(all...)
	got := p.Check(context.Background(), PermissionRequest{})

	if got.Behavior != BehaviorDeny {
		t.Errorf("Behavior = %s, want deny", got.Behavior)
	}
	if got.ValidatorID != "V3" {
		t.Errorf("ValidatorID = %s, want V3", got.ValidatorID)
	}
	if got.Message != "denied" {
		t.Errorf("Message = %s, want denied", got.Message)
	}
}

func TestPipeline_NthAsk_EarlyTermination(t *testing.T) {
	pre := &stubValidator{id: "V1", result: Passthrough("V1", DecisionReasonOther, "")}
	askV := &stubValidator{id: "V2", result: Ask("V2", DecisionReasonMode, "confirm?")}
	post := &stubValidator{id: "V3", result: Deny("V3", DecisionReasonRule, "should not reach")}

	p := NewPipeline(pre, askV, post)
	got := p.Check(context.Background(), PermissionRequest{})

	if got.Behavior != BehaviorAsk {
		t.Errorf("Behavior = %s, want ask", got.Behavior)
	}
	if got.ValidatorID != "V2" {
		t.Errorf("ValidatorID = %s, want V2", got.ValidatorID)
	}
}

func TestPipeline_NthAllow_EarlyTermination(t *testing.T) {
	pre := &stubValidator{id: "V1", result: Passthrough("V1", DecisionReasonOther, "")}
	allowV := &stubValidator{id: "V2", result: Allow("V2", DecisionReasonSandboxOverride, "in sandbox")}
	post := &stubValidator{id: "V3", result: Deny("V3", DecisionReasonRule, "should not reach")}

	p := NewPipeline(pre, allowV, post)
	got := p.Check(context.Background(), PermissionRequest{})

	if got.Behavior != BehaviorAllow {
		t.Errorf("Behavior = %s, want allow", got.Behavior)
	}
	if got.ValidatorID != "V2" {
		t.Errorf("ValidatorID = %s, want V2", got.ValidatorID)
	}
	if got.DecisionReason != DecisionReasonSandboxOverride {
		t.Errorf("DecisionReason = %s, want sandboxOverride", got.DecisionReason)
	}
}

func TestPipeline_FirstValidatorDeny_NoOthersInvoked(t *testing.T) {
	calls := 0
	wrap := &stubValidator{id: "V2", result: Deny("V2", DecisionReasonRule, "")}
	first := &stubValidator{id: "V1", result: Deny("V1", DecisionReasonRule, "first deny")}

	counted := &callCountingValidator{wrapped: wrap, counter: &calls}
	p := NewPipeline(first, counted)
	got := p.Check(context.Background(), PermissionRequest{})

	if got.ValidatorID != "V1" {
		t.Errorf("expected V1 to win, got %s", got.ValidatorID)
	}
	if calls != 0 {
		t.Errorf("V2 should not be invoked, but was called %d times", calls)
	}
}

type callCountingValidator struct {
	wrapped Validator
	counter *int
}

func (c *callCountingValidator) ID() string { return c.wrapped.ID() }
func (c *callCountingValidator) Validate(ctx context.Context, req PermissionRequest) PermissionResult {
	*c.counter++
	return c.wrapped.Validate(ctx, req)
}
