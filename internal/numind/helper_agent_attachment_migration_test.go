package numind

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestStartupDoesNotAutoMigrateAgentAttachment(t *testing.T) {
	source, err := os.ReadFile("helper.go")
	if err != nil {
		t.Fatalf("read helper.go: %v", err)
	}
	helper := string(source)
	if regexp.MustCompile(`(?s)AutoMigrate\([^)]*AgentAttachment`).MatchString(helper) {
		t.Fatal("AgentAttachment schema must use reviewed explicit migrations, not startup AutoMigrate")
	}
	if !strings.Contains(helper, "HasTable(&model.AgentAttachment{})") {
		t.Fatal("startup must fail explicitly when the migrated agent_attachment table is absent")
	}
}
