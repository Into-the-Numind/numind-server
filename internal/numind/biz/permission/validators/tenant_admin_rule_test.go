package validators

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/pkg/model"
)

func TestTenantAdminRule_NilStore_Passthrough(t *testing.T) {
	v := NewTenantAdminRule(nil)
	req := permission.PermissionRequest{
		ParentUserID: 1,
		Tool:         newFakeTool("bash_exec"),
		InputJSON:    `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough, got %q", got.Behavior)
	}
}

func TestTenantAdminRule_NoParentUserID_Passthrough(t *testing.T) {
	s := &fakeAgentPermissionStore{}
	v := NewTenantAdminRule(s)
	req := permission.PermissionRequest{
		ParentUserID: 0,
		Tool:         newFakeTool("bash_exec"),
		InputJSON:    `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough, got %q", got.Behavior)
	}
}

func TestTenantAdminRule_NoRules_Passthrough(t *testing.T) {
	s := &fakeAgentPermissionStore{rules: nil}
	v := NewTenantAdminRule(s)
	req := permission.PermissionRequest{
		ParentUserID: 42,
		Tool:         newFakeTool("bash_exec"),
		InputJSON:    `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough when no rules, got %q", got.Behavior)
	}
}

func TestTenantAdminRule_ToolBlacklist_Deny(t *testing.T) {
	s := &fakeAgentPermissionStore{
		rules: []model.AgentPermissionConfig{
			{RuleType: "tool_blacklist", RuleKey: "bash_exec", Action: "deny", Message: "bash not allowed"},
		},
	}
	v := NewTenantAdminRule(s)
	req := permission.PermissionRequest{
		ParentUserID: 42,
		Tool:         newFakeTool("bash_exec"),
		InputJSON:    `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorDeny {
		t.Errorf("want deny, got %q", got.Behavior)
	}
	if got.Message != "bash not allowed" {
		t.Errorf("want message 'bash not allowed', got %q", got.Message)
	}
}

func TestTenantAdminRule_ToolInputRegexDeny_Deny(t *testing.T) {
	s := &fakeAgentPermissionStore{
		rules: []model.AgentPermissionConfig{
			{RuleType: "tool_input_regex_deny", RuleKey: "bash_exec", RuleValue: `rm\s+-rf`, Action: "deny", Message: "rm -rf blocked"},
		},
	}
	v := NewTenantAdminRule(s)
	req := permission.PermissionRequest{
		ParentUserID: 42,
		Tool:         newFakeTool("bash_exec"),
		InputJSON:    mustJSON(map[string]any{"command": "rm -rf /tmp"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorDeny {
		t.Errorf("want deny, got %q", got.Behavior)
	}
}

func TestTenantAdminRule_TopicBlacklist_Deny(t *testing.T) {
	s := &fakeAgentPermissionStore{
		rules: []model.AgentPermissionConfig{
			{RuleType: "topic_blacklist", RuleKey: "confidential", Action: "deny", Message: "topic blocked"},
		},
	}
	v := NewTenantAdminRule(s)
	req := permission.PermissionRequest{
		ParentUserID: 42,
		Tool:         newFakeTool("file_write"),
		InputJSON:    `{"text": "this is confidential data"}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorDeny {
		t.Errorf("want deny, got %q", got.Behavior)
	}
}

func TestTenantAdminRule_ActionAsk_ReturnsAsk(t *testing.T) {
	s := &fakeAgentPermissionStore{
		rules: []model.AgentPermissionConfig{
			{RuleType: "tool_blacklist", RuleKey: "bash_exec", Action: "ask", Message: "please confirm"},
		},
	}
	v := NewTenantAdminRule(s)
	req := permission.PermissionRequest{
		ParentUserID: 42,
		Tool:         newFakeTool("bash_exec"),
		InputJSON:    `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorAsk {
		t.Errorf("want ask, got %q", got.Behavior)
	}
}

func TestTenantAdminRule_StoreError_FailOpen(t *testing.T) {
	s := &fakeAgentPermissionStore{err: errors.New("db down")}
	v := NewTenantAdminRule(s)
	req := permission.PermissionRequest{
		ParentUserID: 42,
		Tool:         newFakeTool("bash_exec"),
		InputJSON:    `{}`,
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want fail-open passthrough on store error, got %q", got.Behavior)
	}
}
