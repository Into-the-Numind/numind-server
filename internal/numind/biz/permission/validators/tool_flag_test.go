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

// Historical category keys are compatibility metadata under the global policy.
func TestToolFlag_InertForCategoryKeyConfig(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"code_sandbox": true, "media": true, "enable_skills": true}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{ID: 1, ToolFlags: datatypes.JSON(flags)},
	}
	v := NewToolFlag(s)
	for _, tool := range []string{"bash_exec", "image_gen", "load_skill", "run_python", "get_current_date"} {
		req := permission.PermissionRequest{
			AgentDefinitionID: 1,
			Tool:              newFakeTool(tool),
			InputJSON:         `{}`,
		}
		got := v.Validate(context.Background(), req)
		if got.Behavior != permission.BehaviorPassthrough {
			t.Errorf("ToolFlag must passthrough %q under a category-key config (full-open not re-denied), got %q",
				tool, got.Behavior)
		}
	}
}

func TestToolFlag_ToolDisabled_PassthroughUnderGlobalPolicy(t *testing.T) {
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
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("tool_flags must not deny globally available tools, got %q", got.Behavior)
	}
}

// Category false values from historical Agent definitions no longer restrict tools.
func TestToolFlag_CategoryDisabled_PassthroughUnderGlobalPolicy(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"code_sandbox": false, "media": false, "dangerous": false}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{ID: 1, ToolFlags: datatypes.JSON(flags)},
	}
	v := NewToolFlag(s)

	// Historical category values are compatibility metadata and cannot deny tools.
	req := permission.PermissionRequest{AgentDefinitionID: 1, Tool: newFakeTool("bash_exec"), InputJSON: `{}`}
	if got := v.Validate(context.Background(), req); got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("bash_exec: disabled legacy category must passthrough, got %q", got.Behavior)
	}

	// image_gen is NO LONGER category-gated (2026-06-17): 文生图永远可用，即便 media=false。
	req = permission.PermissionRequest{AgentDefinitionID: 1, Tool: newFakeTool("image_gen"), InputJSON: `{}`}
	if got := v.Validate(context.Background(), req); got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("image_gen must passthrough (ungated) even with media=false, got %q", got.Behavior)
	}

	// Baseline tools are never gated by risk categories.
	req = permission.PermissionRequest{AgentDefinitionID: 1, Tool: newFakeTool("kb_search"), InputJSON: `{}`}
	if got := v.Validate(context.Background(), req); got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("baseline kb_search must passthrough even with categories off, got %q", got.Behavior)
	}
}

// Mixed category/direct legacy values are equally non-authoritative.
func TestToolFlag_MixedLegacyFlags_PassthroughUnderGlobalPolicy(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"bash_exec": true, "code_sandbox": false}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{ID: 1, ToolFlags: datatypes.JSON(flags)},
	}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{AgentDefinitionID: 1, Tool: newFakeTool("bash_exec"), InputJSON: `{}`}
	if got := v.Validate(context.Background(), req); got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("legacy category and direct flags must not deny tools, got %q", got.Behavior)
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
