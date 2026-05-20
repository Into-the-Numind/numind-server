package validators

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/permission"
)

func TestPlatformHardRule_NonBashTool_Passthrough(t *testing.T) {
	v := NewPlatformHardRule()
	req := permission.PermissionRequest{
		Tool:      newFakeTool("file_read"),
		InputJSON: mustJSON(map[string]any{"command": "rm -rf /"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough, got %q", got.Behavior)
	}
}

func TestPlatformHardRule_NilTool_Passthrough(t *testing.T) {
	v := NewPlatformHardRule()
	req := permission.PermissionRequest{
		Tool:      nil,
		InputJSON: mustJSON(map[string]any{"command": "ls"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough, got %q", got.Behavior)
	}
}

func TestPlatformHardRule_BashSafeCommand_Passthrough(t *testing.T) {
	v := NewPlatformHardRule()
	req := permission.PermissionRequest{
		Tool:      newFakeTool("bash_exec"),
		InputJSON: mustJSON(map[string]any{"command": "ls -la /workdir"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for safe bash cmd, got %q", got.Behavior)
	}
}

func TestPlatformHardRule_BashControlChar_Deny(t *testing.T) {
	v := NewPlatformHardRule()
	// \x01 is a control char that triggers ControlChar validator
	req := permission.PermissionRequest{
		Tool:      newFakeTool("bash_exec"),
		InputJSON: mustJSON(map[string]any{"command": "ls\x01hidden"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorDeny {
		t.Errorf("want deny for control char bash cmd, got %q", got.Behavior)
	}
	if got.DecisionReason != permission.DecisionReasonRule {
		t.Errorf("want reason=rule, got %q", got.DecisionReason)
	}
}

func TestPlatformHardRule_BashNoCommandField_Passthrough(t *testing.T) {
	v := NewPlatformHardRule()
	req := permission.PermissionRequest{
		Tool:      newFakeTool("bash_exec"),
		InputJSON: mustJSON(map[string]any{"other": "value"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for missing command field, got %q", got.Behavior)
	}
}

func TestPlatformHardRule_ID(t *testing.T) {
	v := NewPlatformHardRule()
	if v.ID() != "PlatformHardRule" {
		t.Errorf("want ID PlatformHardRule, got %q", v.ID())
	}
}
