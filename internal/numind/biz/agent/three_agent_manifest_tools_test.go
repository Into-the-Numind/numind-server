package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
)

func TestThreeAgentDefinitionManifest_CoversEveryPlatformToolExplicitly(t *testing.T) {
	db := newFactoryTestDB(t)
	factory := NewPlatformToolFactory(nil, store.NewTestStore(db))
	executor := &fakeLarkExecutor{}
	SetFactoryLarkWorkspaceExecutors(factory, &fakeSkillReadExecutor{}, &fakeLarkInspector{}, executor, executor)
	_, metadata, err := factory.LoadTools(context.Background())
	require.NoError(t, err)

	_, current, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", "..", "..", ".."))
	body, err := os.ReadFile(filepath.Join(root, "docs/agent-definitions/three-agent-feishu-pipeline/manifest.json"))
	require.NoError(t, err)
	var manifest struct {
		Agents []struct {
			Key       string          `json:"key"`
			ToolFlags map[string]bool `json:"tool_flags"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(body, &manifest))
	require.Len(t, manifest.Agents, 3)

	platformNames := make([]string, 0, len(metadata)+3)
	for _, item := range metadata {
		platformNames = append(platformNames, item.ToolName)
	}
	platformNames = append(platformNames, "code_sandbox", "media", "dangerous")
	sort.Strings(platformNames)

	for _, entry := range manifest.Agents {
		actualNames := make([]string, 0, len(entry.ToolFlags))
		for name := range entry.ToolFlags {
			actualNames = append(actualNames, name)
		}
		sort.Strings(actualNames)
		assert.Equal(t, platformNames, actualNames, "%s must explicitly enable or disable every current platform tool/category", entry.Key)

		wantEnabled := []string{"ask_user_question", "get_current_date", "lark_connect", "lark_execute", "lark_inspect", "lark_skill_read"}
		if entry.Key == "agent-1" {
			wantEnabled = append(wantEnabled, "xhs_note_list")
		} else {
			wantEnabled = append(wantEnabled, "file_read")
		}
		sort.Strings(wantEnabled)
		var gotEnabled []string
		for name, enabled := range entry.ToolFlags {
			if enabled {
				gotEnabled = append(gotEnabled, name)
			}
		}
		sort.Strings(gotEnabled)
		assert.Equal(t, wantEnabled, gotEnabled, "%s enabled tools must be least privilege", entry.Key)
	}
}
