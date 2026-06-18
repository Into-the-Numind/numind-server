package agent

import (
	"context"
	"encoding/json"

	"testing"
	"time"

	"gorm.io/datatypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/model"
)

// persistYieldTranscript must capture the agent's pre-yield work (user input +
// assistant steps) so a resumed run is not blank. Guards HW-33.
func TestPersistYieldTranscript_CapturesUserInputAndSteps(t *testing.T) {
	ms := newMockStore()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, ms.Create(context.Background(), run))
	r := NewAgentRunner(ms, newStaticRegistry(&loopTestTool{})).(*agentRunner)

	ctx := withStepCollector(context.Background())
	ctx = narration.WithCollector(ctx)
	stepCollectorFrom(ctx).add("我先联网检索莫小派的公开信息", "", time.Now())

	r.persistYieldTranscript(ctx, run.ID, "为莫小派做小红书定位调研", nil)

	stored, _ := ms.Get(context.Background(), run.ID)
	var turns []map[string]any
	require.NoError(t, json.Unmarshal(stored.Messages, &turns))
	require.GreaterOrEqual(t, len(turns), 2, "transcript must hold user input + at least one assistant step, not []")
	assert.Equal(t, "user", turns[0]["role"])
	assert.Equal(t, "为莫小派做小红书定位调研", turns[0]["content"])
	var joined string
	for _, tn := range turns {
		if c, ok := tn["content"].(string); ok {
			joined += c
		}
	}
	assert.Contains(t, joined, "我先联网检索", "assistant step must be persisted")
}

// Even with no assistant steps (agent asked on its first turn), the user input
// must still be persisted so the waiting run is never blank.
func TestPersistYieldTranscript_NoSteps_StillPersistsUserInput(t *testing.T) {
	ms := newMockStore()
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(`[]`), StartedAt: time.Now()}
	require.NoError(t, ms.Create(context.Background(), run))
	r := NewAgentRunner(ms, newStaticRegistry(&loopTestTool{})).(*agentRunner)

	ctx := withStepCollector(context.Background())
	ctx = narration.WithCollector(ctx)

	r.persistYieldTranscript(ctx, run.ID, "帮我调研", nil)

	stored, _ := ms.Get(context.Background(), run.ID)
	var turns []map[string]any
	require.NoError(t, json.Unmarshal(stored.Messages, &turns))
	require.Len(t, turns, 1)
	assert.Equal(t, "user", turns[0]["role"])
	assert.Equal(t, "帮我调研", turns[0]["content"])
}

// Multi-yield: a resumed run that pauses again must KEEP the first yield's
// transcript, not clobber it (review P1 / HW-33). The leading duplicate user
// turn (the answer, already appended by AnswerAndClear) is dropped on merge.
func TestPersistYieldTranscript_SecondYield_KeepsPriorContext(t *testing.T) {
	ms := newMockStore()
	// Simulate state after first yield + AnswerAndClear: original task, the
	// agent's first-round research, and the user's answer to Q1.
	prior := `[{"role":"user","content":"为莫小派做调研"},{"role":"assistant","content":"第一轮已搜索莫小派公开信息"},{"role":"user","content":"[user answered] Q1"}]`
	run := &model.AgentRun{UserID: 1, Status: "running", Messages: datatypes.JSON(prior), StartedAt: time.Now()}
	require.NoError(t, ms.Create(context.Background(), run))
	r := NewAgentRunner(ms, newStaticRegistry(&loopTestTool{})).(*agentRunner)

	// Resume run pauses again (Q2). userInput is the Q1 answer (== existing tail).
	ctx := withStepCollector(context.Background())
	ctx = narration.WithCollector(ctx)
	stepCollectorFrom(ctx).add("第二轮分析竞品", "", time.Now())
	r.persistYieldTranscript(ctx, run.ID, "[user answered] Q1", nil)

	stored, _ := ms.Get(context.Background(), run.ID)
	var turns []map[string]any
	require.NoError(t, json.Unmarshal(stored.Messages, &turns))
	var joined string
	for _, tn := range turns {
		if c, ok := tn["content"].(string); ok {
			joined += c + "|"
		}
	}
	assert.Contains(t, joined, "为莫小派做调研", "original task must survive a second yield")
	assert.Contains(t, joined, "第一轮已搜索", "first-round research must survive a second yield")
	assert.Contains(t, joined, "第二轮分析竞品", "second-round work must be appended")
	// No duplicate "[user answered] Q1" — leading dup dropped on merge.
	assert.Equal(t, 1, countSubstr(joined, "[user answered] Q1"), "the Q1 answer turn must not be duplicated")
}

func countSubstr(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
