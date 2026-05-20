package validators

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/permission"
)

func TestSandboxOverride_AlwaysPassthrough(t *testing.T) {
	v := NewSandboxOverride()
	req := permission.PermissionRequest{
		Tool:      newFakeTool("bash_exec"),
		InputJSON: mustJSON(map[string]any{"command": "rm -rf /"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough, got %q", got.Behavior)
	}
	if got.DecisionReason != permission.DecisionReasonSandboxOverride {
		t.Errorf("want reason=sandboxOverride, got %q", got.DecisionReason)
	}
}

func TestSandboxOverride_ID(t *testing.T) {
	v := NewSandboxOverride()
	if v.ID() != "SandboxOverride" {
		t.Errorf("want ID SandboxOverride, got %q", v.ID())
	}
}
