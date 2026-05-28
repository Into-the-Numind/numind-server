package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRollback_AgentDefinitionHistoryColumn guards against the
// 2026-05-28 typo in 20260521_190000_seed_e2e_test_agent_rollback.sql
// where DELETE FROM agent_definition_history used WHERE agent_definition_id
// — a column that doesn't exist on this table. The real column is agent_id
// (see 20260522_220100_create_agent_definition_history.sql line 7 and
// internal/pkg/model/agent_definition_history.go AgentID field).
//
// Several sibling tables (agent_run, agent_session_memory, agent_permission_*)
// legitimately use agent_definition_id; this test only flags the combination
// of agent_definition_history + agent_definition_id within the same statement.
func TestRollback_AgentDefinitionHistoryColumn(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var failures []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, stmt := range strings.Split(string(data), ";") {
			lower := strings.ToLower(stmt)
			if strings.Contains(lower, "agent_definition_history") &&
				strings.Contains(lower, "agent_definition_id") {
				failures = append(failures, e.Name()+": "+strings.TrimSpace(stmt))
			}
		}
	}
	if len(failures) > 0 {
		t.Errorf("SQL references forbidden column agent_definition_id on agent_definition_history (real column is agent_id):\n  %s",
			strings.Join(failures, "\n  "))
	}
}
