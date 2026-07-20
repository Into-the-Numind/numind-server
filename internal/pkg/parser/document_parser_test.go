package parser

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentParserPythonEnvelopeAllowsMultiMiBExtractedText(t *testing.T) {
	content := strings.Repeat("客户资料-", 400_000) // well above the former 2 MiB stdout cap
	payload, err := json.Marshal(map[string]any{
		"success": true, "content": content, "page_count": 12,
	})
	require.NoError(t, err)
	require.Greater(t, len(payload), 2*1024*1024)

	var stdout limitedStringWriter
	stdout.limit = documentParserOutputLimit
	written, err := stdout.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), written)
	assert.False(t, stdout.truncated, "valid multi-MiB parser output must not be silently cut into invalid JSON")
	assert.Equal(t, payload, []byte(stdout.String()))
}

func TestDocumentParserOutputLimitCoversWorstCaseJSONEscaping(t *testing.T) {
	content := strings.Repeat("\x00", 1024)
	payload, err := json.Marshal(map[string]any{"success": true, "content": content})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(payload), 6*len(content))
	require.GreaterOrEqual(t, documentParserOutputLimit, 6*documentParserContentLimit+1024*1024)
}

func TestDecodePythonParserOutputRejectsOversizedExtractedText(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"success": true, "content": strings.Repeat("x", documentParserContentLimit+1), "page_count": 1,
	})
	require.NoError(t, err)

	_, _, err = decodePythonParserOutput(payload)
	require.ErrorContains(t, err, "exceeds 20 MiB")
}
