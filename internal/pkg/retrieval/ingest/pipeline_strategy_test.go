package ingest

import (
	"context"
	"os"
	"testing"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/pkg/retrieval/domain"

	"github.com/stretchr/testify/assert"
)

// ----------------------------------------------------------------------------
// Bug-from-customer reproduction (NDF Rule 11)
//
// Customer report: "语义切分经常不工作" (semantic chunking often falls back to
// fixed-length, silently). Root finding: when the semantic splitter is
// unavailable, the pipeline silently falls back to rule-based chunking and
// records NOTHING — nobody can tell it happened, so the real fallback rate is
// unmeasurable and problems can't be located.
//
// This test reproduces the gap: a document chunked via rule-fallback must
//   (a) NEVER fail the upload — it reaches COMPLETED with non-empty chunks, and
//   (b) be TRACEABLE — the document records split_strategy="rule_fallback".
//
// RED before T1: the pipeline discards the strategy and never persists
// split_strategy (the column/write don't exist). GREEN after T1+T2. Kept
// permanently as regression protection.
// ----------------------------------------------------------------------------

// fakeStrategySplitter simulates a splitter that fell back to rule-based chunking
// (semantic unavailable). Implements Split (the pre-feature path the pipeline
// uses today) AND SplitWithStrategy (the traceability mechanism this feature adds).
type fakeStrategySplitter struct{}

func (f *fakeStrategySplitter) Split(text string) ([]SplitChunk, error) {
	return []SplitChunk{
		{Content: "fallback chunk A", Headers: []string{}},
		{Content: "fallback chunk B", Headers: []string{}},
	}, nil
}

// SplitWithStrategy reports the fallback label + never returns an error (the
// never-fail invariant). Signature must match the StrategyAwareSplitter
// interface T1 introduces.
func (f *fakeStrategySplitter) SplitWithStrategy(text string) ([]SplitChunk, string, string, error) {
	chunks, _ := f.Split(text)
	return chunks, "rule_fallback", "semantic_unavailable", nil
}

// capturingDocStore records the columns written by the pipeline so the test can
// assert both the final status and the persisted split strategy.
type capturingDocStore struct {
	lastUpdates map[string]interface{}
	lastStatus  string
}

func (d *capturingDocStore) UpdateStatus(_ context.Context, _ uint, status string, _ string) error {
	d.lastStatus = status
	return nil
}

func (d *capturingDocStore) UpdateColumns(_ context.Context, _ uint, updates map[string]interface{}) error {
	d.lastUpdates = updates
	return nil
}

func TestIngestionPipeline_PersistsSplitStrategy_OnFallback(t *testing.T) {
	parser := adapter.NewSimpleParser()
	splitter := &fakeStrategySplitter{}
	tagger := NewContentTagger() // degrades gracefully offline (TagChunks returns nil on LLM failure)
	docStore := &capturingDocStore{}
	store := &MockStore{}
	chunkStore := &MockChunkStore{}

	pipeline := NewIngestionPipeline(parser, splitter, tagger, docStore, store, chunkStore)

	tmpFile, err := os.CreateTemp("", "strategy_doc_*.md")
	assert.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.WriteString("# Doc\n这是一段用于切块的测试内容。语义切分如果不可用就应走兜底。")
	_ = tmpFile.Close()

	doc := &domain.KnowledgeDocument{
		ID:       1,
		Name:     "test.md",
		FilePath: tmpFile.Name(),
		Status:   domain.DocStatusPending,
	}

	// Call process synchronously to avoid worker/channel timing.
	pipeline.process(context.Background(), doc)

	// (a) Never-fail: the document reached COMPLETED via the UpdateColumns path.
	assert.Equal(t, string(domain.DocStatusCompleted), docStore.lastUpdates["status"],
		"upload must succeed (COMPLETED) even when chunking fell back")

	// (b) Traceability: the silent fallback must be recorded.
	assert.Equal(t, "rule_fallback", docStore.lastUpdates["split_strategy"],
		"a fallback ingest must record split_strategy so it is traceable")
}
