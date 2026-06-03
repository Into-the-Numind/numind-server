package credit

import "testing"

// T1 (agent-mode-billing): agent_run + image_gen Operation 枚举接入
// normalization (budgetOperationMap) 与 fallback 估算表 (estimatedCredits)。

func TestAgentOperationConstants(t *testing.T) {
	if OpAgentRun != "agent_run" {
		t.Fatalf("OpAgentRun = %q, want %q", OpAgentRun, "agent_run")
	}
	if OpImageGen != "image_gen" {
		t.Fatalf("OpImageGen = %q, want %q", OpImageGen, "image_gen")
	}
}

func TestAgentOperationNormalization(t *testing.T) {
	cases := []struct {
		raw  string
		want Operation
	}{
		{"agent_run", OpAgentRun},
		{"image_gen", OpImageGen},
	}
	for _, c := range cases {
		if got := budgetOperationMap[c.raw]; got != c.want {
			t.Errorf("budgetOperationMap[%q] = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestAgentEstimatedCredits(t *testing.T) {
	// 断言精确值（而非仅 >0）——否则 key 被误删时 fallback 返回 1 仍会通过，掩盖回归。
	cases := []struct {
		op   string
		want int64
	}{
		{"agent_run", 6},
		{"image_gen", 10},
	}
	for _, c := range cases {
		if got := GetEstimatedCredits(c.op); got != c.want {
			t.Errorf("GetEstimatedCredits(%q) = %d, want %d", c.op, got, c.want)
		}
	}
}
