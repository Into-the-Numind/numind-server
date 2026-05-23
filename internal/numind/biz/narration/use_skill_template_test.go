package narration

import (
	"strings"
	"testing"
)

// TestUseSkill_RenderFromYAML verifies that the production tool-display.yaml
// ships an entry for the `use_skill` AgentTool (agent-mode v2 #2) and that the
// use/result/error templates render the expected user-facing copy with the
// 📚/⚠ icons and the skill name interpolated from input.name.
//
// Path resolves from internal/numind/biz/narration/ → repo root (4 levels up).
// Mirrors the disk-load pattern in TestNewRendererFromPath_RepoRootYAML.
func TestUseSkill_RenderFromYAML(t *testing.T) {
	r, err := NewRendererFromPath("../../../../configs/tool-display.yaml")
	if err != nil {
		t.Fatalf("NewRendererFromPath: %v (if file moved/renamed, update path)", err)
	}
	if r.tools["use_skill"] == nil {
		t.Fatal("use_skill entry missing from configs/tool-display.yaml")
	}

	const skillName = "销售话术训练"
	input := map[string]any{"name": skillName}

	cases := []struct {
		name       string
		state      State
		wantSubstr []string // all must appear in message
	}{
		{
			name:       "use state shows loading icon + skill name",
			state:      StateUse,
			wantSubstr: []string{"📚", "正在加载技能", skillName},
		},
		{
			name:       "result state shows success icon + skill name",
			state:      StateResult,
			wantSubstr: []string{"📚", "已调用技能", skillName},
		},
		{
			name:       "error state shows warning icon + reason",
			state:      StateError,
			wantSubstr: []string{"⚠", "技能加载失败", "网络异常"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb, detail, msg := r.Render(renderRequest{
				ToolName:       "use_skill",
				State:          tc.state,
				Input:          input,
				Result:         map[string]any{},
				ReasonFriendly: "网络异常", // only consumed by error_template
			})
			if verb != "调用技能" {
				t.Errorf("verb: want 调用技能, got %q", verb)
			}
			// detail_template = "{{ .input.name }}" → should equal skillName.
			if detail != skillName {
				t.Errorf("detail: want %q, got %q", skillName, detail)
			}
			for _, want := range tc.wantSubstr {
				if !strings.Contains(msg, want) {
					t.Errorf("message missing %q; got %q", want, msg)
				}
			}
		})
	}
}
