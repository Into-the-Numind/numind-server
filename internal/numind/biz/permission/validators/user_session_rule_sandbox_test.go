package validators

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/permission"
)

// test(qa): reproduce dev run #115 — run_python (and bash_exec) denied by
// UserSessionRule with "该操作可能修改你的数据" although both execute inside a
// disposable sandbox container and cannot touch the user's data. Root cause:
// open-tools (2026-06-01) un-gated tool registration while
// remove-permission-backdoor (2026-06-02) armed the real pipeline, whose v1
// UserSessionRule blanket-denies IsDestructive tools. Product decision
// 2026-05-31: bash_exec/run_python ship un-gated in v1 — the sandbox is the
// security boundary. Expected: sandbox-isolated exec tools pass this rule;
// generic destructive tools (e.g. db_drop) remain denied.
func TestUserSessionRule_SandboxedExecTools_Passthrough(t *testing.T) {
	v := NewUserSessionRule()
	for _, name := range []string{"run_python", "bash_exec"} {
		req := permission.PermissionRequest{
			Tool:      newFakeDestructiveTool(name),
			InputJSON: `{}`,
		}
		got := v.Validate(context.Background(), req)
		if got.Behavior != permission.BehaviorPassthrough {
			t.Errorf("%s: want passthrough (sandbox-isolated exec), got %q (msg=%q)",
				name, got.Behavior, got.Message)
		}
		if got.DecisionReason != permission.DecisionReasonSandboxOverride {
			t.Errorf("%s: want auditable reason %q, got %q",
				name, permission.DecisionReasonSandboxOverride, got.DecisionReason)
		}
	}
}

// Map lookup is exact-match: case variants and empty names are NOT exempt.
func TestUserSessionRule_NonExactNames_StillDenied(t *testing.T) {
	v := NewUserSessionRule()
	for _, name := range []string{"Bash_Exec", "RUN_PYTHON", "Run_Python", ""} {
		req := permission.PermissionRequest{
			Tool:      newFakeDestructiveTool(name),
			InputJSON: `{}`,
		}
		got := v.Validate(context.Background(), req)
		if got.Behavior != permission.BehaviorDeny {
			t.Errorf("%q: destructive tool with non-exact name must stay denied, got %q", name, got.Behavior)
		}
	}
}
