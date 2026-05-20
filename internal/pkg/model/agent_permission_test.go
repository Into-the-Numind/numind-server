package model

import "testing"

func TestAgentPermissionConfig_TableName(t *testing.T) {
	got := AgentPermissionConfig{}.TableName()
	want := "agent_permission_config"
	if got != want {
		t.Errorf("TableName() = %q, want %q", got, want)
	}
}

func TestAgentPermissionDecisionLog_TableName(t *testing.T) {
	got := AgentPermissionDecisionLog{}.TableName()
	want := "agent_permission_decision_log"
	if got != want {
		t.Errorf("TableName() = %q, want %q", got, want)
	}
}

// AgentPermissionConfig.IsActive 的 default:true Create 坑测试在 store 层
// （store/agent_permission_test.go 含 UpdateColumn fixup 集成测试）；
// model 层仅验证 struct + TableName。
