package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const rebindVisionTaskProfilesMigration = "20260803_103000_rebind_vision_task_profiles.sql"

func TestRebindVisionTaskProfilesMigrationRepairsRetiredDefaultService(t *testing.T) {
	data, err := os.ReadFile(rebindVisionTaskProfilesMigration)
	if err != nil {
		t.Fatalf("read required customer-bug migration %s: %v", rebindVisionTaskProfilesMigration, err)
	}

	var executable strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		executable.WriteString(line)
		executable.WriteByte('\n')
	}
	sql := strings.ToLower(executable.String())

	for _, required := range []string{
		"update task_profile",
		"default_service_id",
		"qwen3-vl-flash",
		"qwen3.5-flash",
		"new_service.is_active = 1",
		"new_service.deprecated_at is null",
		"route.is_active = 1",
		"provider.name = 'ali-dashscope'",
		"provider.is_active = 1",
		"remaining_retired_vision_defaults",
		"signal sqlstate '45000'",
		"start transaction",
		"rollback",
		"resignal",
		"commit",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration must contain %q", required)
		}
	}

	if regexp.MustCompile(`(?i)task_id\s+in\s*\(`).MatchString(sql) {
		t.Error("migration must repair every profile bound to the retired service, not a hard-coded task list")
	}
	if regexp.MustCompile(`(?i)default_service_id\s*=\s*[0-9]+`).MatchString(sql) {
		t.Error("migration must resolve service IDs by stable model_key, not environment-specific numeric IDs")
	}
	if regexp.MustCompile(`(?i)\b(alter\s+table|create\s+table|drop\s+table|truncate|rename\s+table)\b`).MatchString(sql) {
		t.Error("hotfix must be data-only and must not change database schema")
	}
	if containsProtectedDML(sql, protectedRolloutTablePattern) {
		t.Error("hotfix must not write customer, subscription, credit, attachment, or history tables")
	}
}
