package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestBuildEinoMessages_IncludesPriorTurns reproduces the agent-mode multi-turn
// context loss bug (User-reported, dev 2026-06-08): within one session, prior
// turns were never fed to the LLM, so the second turn had no memory of the
// first. Pre-fix buildEinoMessages returns ONLY the current input (len==1),
// dropping req.History — so this test FAILS until History is prepended.
func TestBuildEinoMessages_IncludesPriorTurns(t *testing.T) {
	req := RunRequest{
		Input: "把这些内容做成一份PPT",
		History: []*schema.Message{
			{Role: schema.User, Content: "帮我做一个教培机构调研"},
			{Role: schema.Assistant, Content: "调研结果：教培机构当前痛点是..."},
		},
	}

	msgs := buildEinoMessages(req)

	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (2 history + 1 current input), got %d — prior turns dropped", len(msgs))
	}
	if msgs[0].Role != schema.User || msgs[0].Content != "帮我做一个教培机构调研" {
		t.Errorf("msgs[0] = {%v, %q}, want first history user turn", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != schema.Assistant || msgs[1].Content != "调研结果：教培机构当前痛点是..." {
		t.Errorf("msgs[1] = {%v, %q}, want history assistant turn", msgs[1].Role, msgs[1].Content)
	}
	if msgs[2].Role != schema.User || msgs[2].Content != "把这些内容做成一份PPT" {
		t.Errorf("msgs[2] = {%v, %q}, want current input last", msgs[2].Role, msgs[2].Content)
	}
}
