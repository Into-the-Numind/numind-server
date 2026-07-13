package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestFeishuPersonalWorkspaceMigration_AllowsDurableExternalResumeStates(t *testing.T) {
	forward, err := os.ReadFile("20260713_130000_feishu_personal_workspace.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(forward))
	for _, required := range []string{
		"drop check `chk_ar_state_reason`",
		"add constraint `chk_ar_state_reason` check",
		"'external_resume_ready'",
		"state_reason like 'ext_resume:%'",
		"'running'",
		"'waiting_for_user_choice'",
		"'permission_denied'",
		"'context_exhausted'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("forward migration is missing %q", required)
		}
	}
}

func TestFeishuPersonalWorkspaceRollback_NormalizesResumeRowsAndRestoresCurrentPreFeatureStates(t *testing.T) {
	rollback, err := os.ReadFile("20260713_130000_feishu_personal_workspace_rollback.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(rollback))
	normalize := strings.Index(sql, "update `agent_run`")
	restore := strings.LastIndex(sql, "add constraint `chk_ar_state_reason` check")
	if normalize < 0 || restore < 0 || normalize > restore {
		t.Fatalf("rollback must normalize external resume rows before restoring the CHECK")
	}
	for _, required := range []string{
		"state_reason like 'ext_resume:%'",
		"'external_resume_ready'",
		"'running'",
		"'waiting_for_user_choice'",
		"'permission_denied'",
		"'context_exhausted'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("rollback contract is missing %q", required)
		}
	}
}
