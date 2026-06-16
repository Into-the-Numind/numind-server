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

// TestToolFlag_FullOpenCategoryConfig_Passthrough guards the full-open default:
// when the frontend stores its risk-CATEGORY keys all ENABLED (code_sandbox / media
// / enable_skills = true), no tool is denied — bash_exec/image_gen pass because their
// gating category is true, and load_skill/run_python/get_current_date are not gated
// at all. (The complementary deny path — a category set false — is covered by
// TestToolFlag_CategoryDisabled_Denies.)
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

// TestToolFlag_CategoryDisabled_Denies reproduces the broken-toggle bug: the
// AgentAdvancedEdit UI stores risk-CATEGORY keys (code_sandbox / media / dangerous),
// but the validator only ever checked raw tool names — so a parent disabling
// "代码沙箱" wrote {"code_sandbox": false} yet bash_exec kept running (the validator
// looked up flags["bash_exec"], found nothing, and passed through). A disabled risk
// category must DENY the tools it gates.
func TestToolFlag_CategoryDisabled_Denies(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"code_sandbox": false, "media": false, "dangerous": false}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{ID: 1, ToolFlags: datatypes.JSON(flags)},
	}
	v := NewToolFlag(s)

	for _, tool := range []string{"bash_exec", "image_gen"} {
		req := permission.PermissionRequest{AgentDefinitionID: 1, Tool: newFakeTool(tool), InputJSON: `{}`}
		got := v.Validate(context.Background(), req)
		if got.Behavior != permission.BehaviorDeny {
			t.Errorf("tool %q: a disabled risk category must DENY, got %q", tool, got.Behavior)
		}
	}

	// Baseline tools are never gated by risk categories.
	req := permission.PermissionRequest{AgentDefinitionID: 1, Tool: newFakeTool("kb_search"), InputJSON: `{}`}
	if got := v.Validate(context.Background(), req); got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("baseline kb_search must passthrough even with categories off, got %q", got.Behavior)
	}
}

// TestToolFlag_CategoryDisabled_OverridesStaleToolKey guards the legacy-data path:
// an agent created before this fix has tool-name keys (bash_exec:true) in its flags.
// If the parent later disables the category, the category-deny must win over the
// stale per-tool true (so the UI toggle always takes effect regardless of merge/replace).
func TestToolFlag_CategoryDisabled_OverridesStaleToolKey(t *testing.T) {
	flags, _ := datatypes.JSON([]byte(`{"bash_exec": true, "code_sandbox": false}`)).MarshalJSON()
	s := &fakeAgentDefinitionStore{
		definition: &model.AgentDefinition{ID: 1, ToolFlags: datatypes.JSON(flags)},
	}
	v := NewToolFlag(s)
	req := permission.PermissionRequest{AgentDefinitionID: 1, Tool: newFakeTool("bash_exec"), InputJSON: `{}`}
	if got := v.Validate(context.Background(), req); got.Behavior != permission.BehaviorDeny {
		t.Errorf("disabled category must override a stale per-tool true, got %q", got.Behavior)
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
