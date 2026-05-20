package compact

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// turn builds a 3-message turn: user, assistant, tool_result.
func turn(toolCallID string) []Message {
	return []Message{
		{Role: "user", Content: "u-" + toolCallID},
		{Role: "assistant", Content: "a-" + toolCallID},
		{Role: "tool", Content: "tr-" + toolCallID, ToolCallID: toolCallID},
	}
}

func concat(turns ...[]Message) []Message {
	var out []Message
	for _, t := range turns {
		out = append(out, t...)
	}
	return out
}

func countRole(msgs []Message, role string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == role {
			n++
		}
	}
	return n
}

func TestCollapseDrain_StripsToolResults(t *testing.T) {
	msgs := concat(turn("1"), turn("2"), turn("3"), turn("4"), turn("5"))
	// 5 user turns; keepTurns=4 protects turns 2-5
	out := CollapseDrain(msgs, 4)
	// Turn 1's tool stripped; turns 2-5 fully retained.
	assert.Equal(t, 5, countRole(out, "user"))
	assert.Equal(t, 5, countRole(out, "assistant"))
	assert.Equal(t, 4, countRole(out, "tool"), "turn 1's tool_result should be stripped")
}

func TestCollapseDrain_KeepsTextBlocks(t *testing.T) {
	msgs := concat(turn("1"), turn("2"), turn("3"), turn("4"), turn("5"))
	out := CollapseDrain(msgs, 4)
	// All user/assistant text from turn 1 should remain.
	found := false
	for _, m := range out {
		if m.Content == "u-1" {
			found = true
		}
	}
	assert.True(t, found, "user text from turn 1 must be preserved")
}

func TestCollapseDrain_RespectsCompactSummary(t *testing.T) {
	mark := Message{Role: "tool", Content: "old-summary-marker", IsCompactMark: true}
	msgs := concat([]Message{mark}, turn("1"), turn("2"), turn("3"), turn("4"), turn("5"))
	out := CollapseDrain(msgs, 4)
	hasMark := false
	for _, m := range out {
		if m.IsCompactMark {
			hasMark = true
		}
	}
	assert.True(t, hasMark, "IsCompactMark must not be dropped")
}

func TestCollapseDrain_RespectsFileRef(t *testing.T) {
	fileRef := Message{Role: "tool", Content: "file:foo.pdf", HasFileRef: true}
	msgs := concat([]Message{fileRef}, turn("1"), turn("2"), turn("3"), turn("4"), turn("5"))
	out := CollapseDrain(msgs, 4)
	hasFile := false
	for _, m := range out {
		if m.HasFileRef {
			hasFile = true
		}
	}
	assert.True(t, hasFile, "HasFileRef must not be dropped")
}

func TestCollapseDrain_RespectsRecentTurns(t *testing.T) {
	msgs := concat(turn("1"), turn("2"), turn("3"))
	// keepTurns=4 >= 3 user turns → return as-is
	out := CollapseDrain(msgs, 4)
	assert.Equal(t, len(msgs), len(out))
}

func TestCollapseDrain_KeepTurnsBoundary(t *testing.T) {
	msgs := concat(turn("1"), turn("2"))
	// keepTurns=0 → defaults to 4 internally; len(msgs)=6 > 4, so use default-4 path
	out := CollapseDrain(msgs, 0)
	// 2 turns < 4 → all preserved
	assert.Equal(t, len(msgs), len(out))
}

func TestCollapseDrain_EmptyInput(t *testing.T) {
	out := CollapseDrain([]Message{}, 4)
	assert.Empty(t, out)
}

func TestHeadDropRetry_DropsByGroup(t *testing.T) {
	// 12 turns → dropPercent=0.25 → 3 turn drop candidates (12 - 10 keep = 2 max though)
	// numTurns=12, max=12-10=2, target=12*0.25=3 → actual drop = 2
	turns := make([][]Message, 12)
	for i := range turns {
		turns[i] = turn(string(rune('a' + i)))
	}
	msgs := concat(turns...)
	out := headDropRetry(msgs, 0.25)
	assert.Less(t, len(out), len(msgs), "should drop some turns")
	assert.Equal(t, 10, countRole(out, "user"), "exactly 10 user turns remain")
}

func TestHeadDropRetry_KeepsRecentTen(t *testing.T) {
	// 8 turns ≤ 10 → return as-is
	turns := make([][]Message, 8)
	for i := range turns {
		turns[i] = turn(string(rune('a' + i)))
	}
	msgs := concat(turns...)
	out := headDropRetry(msgs, 0.5)
	assert.Equal(t, len(msgs), len(out))
}

func TestHeadDropRetry_StopsAtProtectedTurn(t *testing.T) {
	// 12 turns; turn at index 1 contains a protected message (IsCompactMark).
	turns := make([][]Message, 12)
	for i := range turns {
		turns[i] = turn(string(rune('a' + i)))
	}
	// Inject IsCompactMark into the user message of turn 1.
	turns[1][0].IsCompactMark = true
	msgs := concat(turns...)
	out := headDropRetry(msgs, 0.5)
	// Drop should stop at turn 1 → only turn 0 dropped.
	assert.Equal(t, 11, countRole(out, "user"), "only turn 0 should be dropped")
}

func TestHeadDropRetry_StopsAtFileRef(t *testing.T) {
	turns := make([][]Message, 12)
	for i := range turns {
		turns[i] = turn(string(rune('a' + i)))
	}
	turns[1][2].HasFileRef = true
	msgs := concat(turns...)
	out := headDropRetry(msgs, 0.5)
	assert.Equal(t, 11, countRole(out, "user"), "drop must stop at protected turn 1")
}

func TestReactiveCompact_HappyPath(t *testing.T) {
	m := &MockCompactProvider{PlaceholderSummary: "summary"}
	ctx := context.Background()
	msgs := concat(turn("1"), turn("2"))
	result, final, err := ReactiveCompact(ctx, m, msgs, DefaultConfig())
	require.NoError(t, err)
	assert.Equal(t, "summary", result.Summary)
	assert.Equal(t, len(msgs), len(final), "no inner drop on happy path")
}

func TestReactiveCompact_InnerRetryThenSuccess(t *testing.T) {
	m := &MockCompactProvider{
		PlaceholderSummary: "summary",
		FailureSequence:    []error{errors.New("first")},
	}
	ctx := context.Background()
	// Need >10 turns to trigger head-drop; otherwise headDropRetry returns input.
	turns := make([][]Message, 15)
	for i := range turns {
		turns[i] = turn(string(rune('a' + i)))
	}
	msgs := concat(turns...)
	result, final, err := ReactiveCompact(ctx, m, msgs, DefaultConfig())
	require.NoError(t, err)
	assert.Equal(t, "summary", result.Summary)
	assert.Less(t, len(final), len(msgs), "final messages must be the truncated set that succeeded")
}

func TestReactiveCompact_ExhaustsInnerRetries(t *testing.T) {
	want := errors.New("always-fail")
	m := &MockCompactProvider{
		PlaceholderSummary: "summary",
		FailureSequence:    []error{want, want, want, want},
	}
	ctx := context.Background()
	msgs := concat(turn("1"), turn("2"))
	result, final, err := ReactiveCompact(ctx, m, msgs, DefaultConfig())
	assert.Nil(t, result)
	assert.Nil(t, final)
	assert.ErrorIs(t, err, want)
}

func TestReactiveCompact_NilProvider(t *testing.T) {
	ctx := context.Background()
	result, final, err := ReactiveCompact(ctx, nil, nil, DefaultConfig())
	assert.Nil(t, result)
	assert.Nil(t, final)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil provider")
}
