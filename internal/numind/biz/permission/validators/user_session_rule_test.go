package validators

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/permission"
)

func TestUserSessionRule_NonDestructive_Passthrough(t *testing.T) {
	v := NewUserSessionRule()
	req := permission.PermissionRequest{
		Tool:      newFakeTool("file_read"),
		InputJSON: `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for non-destructive tool, got %q", got.Behavior)
	}
}

func TestUserSessionRule_Destructive_Deny(t *testing.T) {
	v := NewUserSessionRule()
	req := permission.PermissionRequest{
		Tool:      newFakeDestructiveTool("db_drop"),
		InputJSON: `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorDeny {
		t.Errorf("want deny for destructive tool, got %q", got.Behavior)
	}
	if got.DecisionReason != permission.DecisionReasonMode {
		t.Errorf("want reason=mode, got %q", got.DecisionReason)
	}
}

func TestUserSessionRule_NilTool_Passthrough(t *testing.T) {
	v := NewUserSessionRule()
	req := permission.PermissionRequest{
		Tool:      nil,
		InputJSON: `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for nil tool, got %q", got.Behavior)
	}
}

func TestUserSessionRule_ID(t *testing.T) {
	v := NewUserSessionRule()
	if v.ID() != "UserSessionRule" {
		t.Errorf("want ID UserSessionRule, got %q", v.ID())
	}
}
