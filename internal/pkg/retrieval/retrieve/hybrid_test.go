package retrieve

import (
	"context"
	"testing"

	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
)

// indexOf 返回 id 在 chunks 中的位置（-1 表示不存在）。
func indexOf(chunks []domain.KnowledgeChunk, id string) int {
	for i, c := range chunks {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// TestFuseRRF_DocInBothRanksAbove 验证同时出现在 dense 与 keyword 两路的 doc，
// 其 RRF 融合分高于仅出现在单路的 doc，故排名更靠前。
func TestFuseRRF_DocInBothRanksAbove(t *testing.T) {
	// dense: [a, b, c]; keyword: [b, d]
	// b 在两路都靠前 → 应排在只出现一次的 a/c/d 之前。
	dense := []domain.KnowledgeChunk{chunk("a", 1), chunk("b", 1), chunk("c", 1)}
	keyword := []domain.KnowledgeChunk{chunk("b", 1), chunk("d", 1)}

	fused := fuseRRF(dense, keyword, 10)

	posB := indexOf(fused, "b")
	if posB < 0 {
		t.Fatalf("b missing from fused result: %+v", fused)
	}
	for _, only := range []string{"a", "c", "d"} {
		pos := indexOf(fused, only)
		if pos < 0 {
			t.Fatalf("%q missing from fused result", only)
		}
		if posB >= pos {
			t.Errorf("doc b (in both lists) should rank above %q (single list): posB=%d pos%s=%d",
				only, posB, only, pos)
		}
	}
	// 去重：所有 4 个唯一 ID 各出现一次。
	if len(fused) != 4 {
		t.Fatalf("want 4 unique fused chunks, got %d (%+v)", len(fused), fused)
	}
}

// TestFuseRRF_TopKTruncates 验证融合结果被 topK 截断。
func TestFuseRRF_TopKTruncates(t *testing.T) {
	dense := []domain.KnowledgeChunk{chunk("a", 1), chunk("b", 1), chunk("c", 1)}
	keyword := []domain.KnowledgeChunk{chunk("d", 1), chunk("e", 1)}

	fused := fuseRRF(dense, keyword, 2)
	if len(fused) != 2 {
		t.Fatalf("want topK=2 fused chunks, got %d", len(fused))
	}
}

// TestFuseRRF_DedupByID 验证按 chunk.ID 去重，dense 元数据优先保留。
func TestFuseRRF_DedupByID(t *testing.T) {
	denseB := chunk("b", 1)
	denseB.Content = "dense-content"
	kwB := chunk("b", 1)
	kwB.Content = "keyword-content"

	fused := fuseRRF([]domain.KnowledgeChunk{denseB}, []domain.KnowledgeChunk{kwB}, 10)
	if len(fused) != 1 {
		t.Fatalf("want 1 deduped chunk, got %d", len(fused))
	}
	if fused[0].Content != "dense-content" {
		t.Errorf("dedup should keep dense metadata, got content=%q", fused[0].Content)
	}
}

// --- hybrid store fake (实现 VectorStore + KeywordSearcher) ---

type fakeHybridStore struct {
	denseByQuery   map[string][]domain.KnowledgeChunk
	keywordResults []domain.KnowledgeChunk
	keywordErr     error
	keywordCalls   int
}

func (f *fakeHybridStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	return nil
}
func (f *fakeHybridStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	return nil
}
func (f *fakeHybridStore) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	return nil, nil
}
func (f *fakeHybridStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	return f.denseByQuery[query], nil
}
func (f *fakeHybridStore) SearchKeyword(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	f.keywordCalls++
	if f.keywordErr != nil {
		return nil, f.keywordErr
	}
	return f.keywordResults, nil
}

// 确保 fakeHybridStore 同时满足两个接口（编译期断言）。
var (
	_ port.VectorStore     = (*fakeHybridStore)(nil)
	_ port.KeywordSearcher = (*fakeHybridStore)(nil)
)

// TestRetrieve_Hybrid_FusesDenseAndKeyword 验证 Hybrid 开启 + store 支持关键词通道时，
// 两路结果被 RRF 融合，且关键词通道被调用一次。RerankTopN=0 跳过 rerank（无 LLM 调用）。
func TestRetrieve_Hybrid_FusesDenseAndKeyword(t *testing.T) {
	store := &fakeHybridStore{
		denseByQuery: map[string][]domain.KnowledgeChunk{
			"q": {chunk("a", 1), chunk("b", 1)},
		},
		keywordResults: []domain.KnowledgeChunk{chunk("b", 1), chunk("c", 1)},
	}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, Hybrid: true},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.keywordCalls != 1 {
		t.Errorf("keyword channel should be called once, got %d", store.keywordCalls)
	}
	// 融合后应含三个唯一 chunk a,b,c，且 b（两路都有）排第一。
	if len(res.Chunks) != 3 {
		t.Fatalf("want 3 fused chunks, got %d", len(res.Chunks))
	}
	if res.Chunks[0].ID != "b" {
		t.Errorf("doc b (in both channels) should rank first after RRF, got %q", res.Chunks[0].ID)
	}
}

// TestRetrieve_Hybrid_BypassWhenKeywordEmpty 验证关键词通道空结果时旁路 RRF，
// dense 结果保持原序原分（零回归）。
func TestRetrieve_Hybrid_BypassWhenKeywordEmpty(t *testing.T) {
	denseA := chunk("a", 1)
	denseA.Score = 0.91
	denseB := chunk("b", 1)
	denseB.Score = 0.82
	store := &fakeHybridStore{
		denseByQuery: map[string][]domain.KnowledgeChunk{
			"q": {denseA, denseB},
		},
		keywordResults: nil, // 关键词无结果 → 旁路
	}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, Hybrid: true},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Chunks) != 2 {
		t.Fatalf("want 2 dense chunks (bypass), got %d", len(res.Chunks))
	}
	// 原序保持：a 在前，b 在后；原分未被 RRF 覆盖。
	if res.Chunks[0].ID != "a" || res.Chunks[1].ID != "b" {
		t.Errorf("dense order should be preserved on bypass, got [%s, %s]",
			res.Chunks[0].ID, res.Chunks[1].ID)
	}
	if res.Chunks[0].Score != 0.91 {
		t.Errorf("dense score should be preserved on bypass, got %v", res.Chunks[0].Score)
	}
}

// TestRetrieve_HybridOff_NoKeywordCall 验证 Hybrid 关闭时不调用关键词通道（纯向量，零回归）。
func TestRetrieve_HybridOff_NoKeywordCall(t *testing.T) {
	store := &fakeHybridStore{
		denseByQuery: map[string][]domain.KnowledgeChunk{
			"q": {chunk("a", 1)},
		},
		keywordResults: []domain.KnowledgeChunk{chunk("b", 1)},
	}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, Hybrid: false},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.keywordCalls != 0 {
		t.Errorf("keyword channel must NOT be called when Hybrid=false, got %d calls", store.keywordCalls)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].ID != "a" {
		t.Errorf("want dense-only [a], got %+v", res.Chunks)
	}
}

// TestRetrieve_Hybrid_NonKeywordStore_FallsBackToDense 验证 store 不实现 KeywordSearcher 时，
// 即使 Hybrid=true 也走纯向量（不破坏 memory/dashvector 等 store）。
func TestRetrieve_Hybrid_NonKeywordStore_FallsBackToDense(t *testing.T) {
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q": {chunk("a", 1), chunk("b", 1)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, Hybrid: true},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Chunks) != 2 {
		t.Fatalf("want 2 dense chunks (non-keyword store), got %d", len(res.Chunks))
	}
}
