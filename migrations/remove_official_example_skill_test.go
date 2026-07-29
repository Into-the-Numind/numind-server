package migrations

import (
	"os"
	"strings"
	"testing"
)

const removeOfficialExampleSkillFile = "20260729_143500_remove_official_example_skill.sql"

func TestRemoveOfficialExampleSkillMigrationDeletesSeededOfficialSkill(t *testing.T) {
	data, err := os.ReadFile(removeOfficialExampleSkillFile)
	if err != nil {
		t.Fatalf("read %s: %v", removeOfficialExampleSkillFile, err)
	}
	sql := strings.ToLower(string(data))
	required := []string{
		"delete from skill",
		"官方示例技能",
		"visibility = 'official'",
		"parent_user_id = 0",
		"owner_user_id = 0",
	}
	for _, want := range required {
		if !strings.Contains(sql, strings.ToLower(want)) {
			t.Errorf("migration must contain %q", want)
		}
	}
}
