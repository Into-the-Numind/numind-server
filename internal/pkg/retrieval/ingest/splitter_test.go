package ingest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarkdownSplitter_Split(t *testing.T) {
	markdown := `# Sales Playbook
Introduction to sales.

## Chapter 1: Discovery
Discovery is crucial.
You need to ask open questions.

### Question Types
1. Open-ended
2. Close-ended

## Chapter 2: Negotiation
Never give up.
`

	// Test with explicit config
	splitter := NewMarkdownSplitter(SplitterConfig{
		MaxChunkSize: 50,
		MinChunkSize: 10,
	})

	chunks, err := splitter.Split(markdown)
	assert.NoError(t, err)

	// Check results
	assert.NotEmpty(t, chunks)

	// Print chunks for debugging
	for i, c := range chunks {
		t.Logf("Chunk %d: %s", i, c.Content)
	}

	// Verify context injection
	// We expect headers to be included in chunks that are children of those headers

	foundChapter1 := false
	foundQuestionTypes := false

	for _, c := range chunks {
		// Check context inheritance
		if strings.Contains(c.Content, "Discovery is crucial") {
			if strings.Contains(c.Content, "Chapter 1: Discovery") {
				foundChapter1 = true
			}
		}
		if strings.Contains(c.Content, "Open-ended") {
			if strings.Contains(c.Content, "Question Types") {
				foundQuestionTypes = true
			}
		}
	}

	assert.True(t, foundChapter1, "Chunk should contain parent header 'Chapter 1: Discovery'")
	assert.True(t, foundQuestionTypes, "Chunk should contain parent header 'Question Types'")
}
