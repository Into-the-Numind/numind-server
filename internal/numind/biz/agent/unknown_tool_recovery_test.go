package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentToolsNodeConfig_UnknownToolIsRecoverable reproduces Dev Agent 3
// run 268. The model hallucinated run_python even though the definition had
// run_python=false; without UnknownToolsHandler Eino returned a NodeRunError
// and terminated the whole run before any Feishu write.
func TestAgentToolsNodeConfig_UnknownToolIsRecoverable(t *testing.T) {
	ctx := context.Background()
	config := agentToolsNodeConfig(nil)
	node, err := compose.NewToolNode(ctx, &config)
	require.NoError(t, err)

	messages, err := node.Invoke(ctx, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   "call-hallucinated",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "run_python",
				Arguments: `{"code":"do-not-echo-this-input"}`,
			},
		}},
	})

	require.NoError(t, err, "an unavailable tool must remain model-recoverable")
	require.Len(t, messages, 1)
	assert.Equal(t, schema.Tool, messages[0].Role)
	assert.Equal(t, "call-hallucinated", messages[0].ToolCallID)
	assert.Contains(t, messages[0].Content, "run_python")
	assert.Contains(t, messages[0].Content, "not available")
	assert.Contains(t, messages[0].Content, "Do not retry")
	assert.NotContains(t, messages[0].Content, "do-not-echo-this-input",
		"model-generated arguments must not be reflected into the recovery result")
}
