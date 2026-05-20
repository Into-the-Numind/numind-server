package permission

import "testing"

func TestPassthrough_Builder(t *testing.T) {
	r := Passthrough("MyVal", DecisionReasonOther, "no match")
	if r.Behavior != BehaviorPassthrough {
		t.Errorf("Behavior = %s, want %s", r.Behavior, BehaviorPassthrough)
	}
	if r.ValidatorID != "MyVal" {
		t.Errorf("ValidatorID = %s, want MyVal", r.ValidatorID)
	}
	if r.DecisionReason != DecisionReasonOther {
		t.Errorf("DecisionReason mismatch")
	}
}

func TestAllow_Builder(t *testing.T) {
	r := Allow("Default", DecisionReasonOther, "all passthrough")
	if r.Behavior != BehaviorAllow {
		t.Errorf("Behavior = %s, want allow", r.Behavior)
	}
}

func TestDeny_Builder(t *testing.T) {
	r := Deny("Tenant", DecisionReasonRule, "禁止讨论 X")
	if r.Behavior != BehaviorDeny {
		t.Errorf("Behavior = %s, want deny", r.Behavior)
	}
	if r.Message != "禁止讨论 X" {
		t.Errorf("Message wrong")
	}
}

func TestAsk_Builder(t *testing.T) {
	r := Ask("L3", DecisionReasonMode, "确认继续？")
	if r.Behavior != BehaviorAsk {
		t.Errorf("Behavior = %s, want ask", r.Behavior)
	}
}

func TestBehaviorConstants_Distinct(t *testing.T) {
	set := map[string]bool{BehaviorAllow: true, BehaviorAsk: true, BehaviorDeny: true, BehaviorPassthrough: true}
	if len(set) != 4 {
		t.Errorf("Behavior constants not distinct")
	}
}

func TestDecisionReasonConstants_11(t *testing.T) {
	all := []DecisionReasonType{
		DecisionReasonRule, DecisionReasonMode, DecisionReasonSubcommandResults,
		DecisionReasonPermissionPromptTool, DecisionReasonHook, DecisionReasonAsyncAgent,
		DecisionReasonSandboxOverride, DecisionReasonClassifier, DecisionReasonWorkingDir,
		DecisionReasonSafetyCheck, DecisionReasonOther,
	}
	if len(all) != 11 {
		t.Errorf("Expected 11 DecisionReason constants, got %d", len(all))
	}
	set := make(map[DecisionReasonType]bool)
	for _, r := range all {
		set[r] = true
	}
	if len(set) != 11 {
		t.Errorf("DecisionReason constants not distinct")
	}
}
