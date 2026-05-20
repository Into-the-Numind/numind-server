package validators

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/pkg/model"

	"gorm.io/datatypes"
)

func TestToolFlag_NilStore_Passthrough(t *testing.T) {
	v := NewToolFlag(nil)
	req := permission.PermissionRequest{
		AgentDefinitionID: 1,
		Tool:              newFakeTool("bash_exec"),
		InputJSON:         `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for nil store, got %q", got.Behavior)
	}
}

func TestToolFlag_ZeroDefinitionID_Passthrough(t *testing.T) {
	s := &fakeAgentDefinitionStore{}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{
		AgentDefinitionID: 0,
		Tool:              newFakeTool("bash_exec"),
		InputJSON:         `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for zero definition ID, got %q", got.Behavior)
	}
}

func TestToolFlag_SkillLookupError_FailOpen(t *testing.T) {
	s := &fakeAgentDefinitionStore{err: errors.New("not found")}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{
		AgentDefinitionID: 99,
		Tool:              newFakeTool("bash_exec"),
		InputJSON:         `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want fail-open passthrough on lookup error, got %q", got.Behavior)
	}
}

func TestToolFlag_ToolEnabled_Passthrough(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"bash_exec": true}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{
			ID:        1,
			ToolFlags: datatypes.JSON(flags),
		},
	}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{
		AgentDefinitionID: 1,
		Tool:              newFakeTool("bash_exec"),
		InputJSON:         `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for enabled tool, got %q", got.Behavior)
	}
}

func TestToolFlag_ToolDisabled_Deny(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"bash_exec": false}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{
			ID:        1,
			ToolFlags: datatypes.JSON(flags),
		},
	}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{
		AgentDefinitionID: 1,
		Tool:              newFakeTool("bash_exec"),
		InputJSON:         `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorDeny {
		t.Errorf("want deny for disabled tool, got %q", got.Behavior)
	}
	if got.DecisionReason != permission.DecisionReasonRule {
		t.Errorf("want reason=rule, got %q", got.DecisionReason)
	}
}

func TestToolFlag_ToolNotInFlags_Passthrough(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"file_read": true}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{
			ID:        1,
			ToolFlags: datatypes.JSON(flags),
		},
	}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{
		AgentDefinitionID: 1,
		Tool:              newFakeTool("bash_exec"),
		InputJSON:         `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for tool not in flags, got %q", got.Behavior)
	}
}

func TestToolFlag_NoToolFlags_Passthrough(t *testing.T) {
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{
			ID:        1,
			ToolFlags: nil,
		},
	}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{
		AgentDefinitionID: 1,
		Tool:              newFakeTool("bash_exec"),
		InputJSON:         `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough for nil tool_flags, got %q", got.Behavior)
	}
}
