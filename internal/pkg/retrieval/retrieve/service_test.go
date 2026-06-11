package retrieve

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
)

// --- fakes ---

// fakeVectorStore 按 query 返回预设 chunk 列表，并记录每次 Search 的参数。
type fakeVectorStore struct {
	// byQuery: query -> 返回的 chunks（模拟多路检索各自命中）
	byQuery map[string][]domain.KnowledgeChunk
	// searchErr: 若该 query 命中此 map，则该路返回错误
	searchErr map[string]error
	// 记录调用（parallelSearch 并发调用 Search，mu 保护这些 slice 的 append）
	mu         sync.Mutex
	gotFilters []port.SearchFilter
	gotLimits  []int
	gotQueries []string
}

func (f *fakeVectorStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	return nil
}
func (f *fakeVectorStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	return nil
}
func (f *fakeVectorStore) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	return nil, nil
}
func (f *fakeVectorStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	f.mu.Lock()
	f.gotFilters = append(f.gotFilters, filter)
	f.gotLimits = append(f.gotLimits, limit)
	f.gotQueries = append(f.gotQueries, query)
	f.mu.Unlock()
	if f.searchErr != nil {
		if err, ok := f.searchErr[query]; ok {
			return nil, err
		}
	}
	return f.byQuery[query], nil
}

// fakeRewriter 返回预设的改写 queries + HyDE，并记录入参。
type fakeRewriter struct {
	result    port.RewriteResult
	err       error
	gotQuery  string
	gotHist   []string
	callCount int
}

func (f *fakeRewriter) Rewrite(ctx context.Context, query string, history []string) (port.RewriteResult, error) {
	f.callCount++
	f.gotQuery = query
	f.gotHist = history
	if f.err != nil {
		return port.RewriteResult{}, f.err
	}
	return f.result, nil
}

// fakeDocStore 返回预设的启用文档 ID 列表。
type fakeDocStore struct {
	ids       []uint
	err       error
	gotUserID uint
	callCount int
}

func (f *fakeDocStore) ListEnabledDocIDs(ctx context.Context, userID uint) ([]uint, error) {
	f.callCount++
	f.gotUserID = userID
	if f.err != nil {
		return nil, f.err
	}
	return f.ids, nil
}

func chunk(id string, docID uint) domain.KnowledgeChunk {
	return domain.KnowledgeChunk{ID: id, DocumentID: docID, UserID: 1, Content: "content-" + id}
}

// --- tests: 三态 scope ---

func TestRetrieve_Scope_DocumentIDs(t *testing.T) {
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q1": {chunk("a", 10), chunk("b", 10)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q1",
		Scope{UserID: 1, DocumentIDs: []uint{10, 20}},
		Options{TopK: 10}, // 不改写、不重排
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(res.Chunks))
	}
	// filter 应携带 scope.DocumentIDs，limit = TopK
	if len(store.gotFilters) != 1 {
		t.Fatalf("want 1 search call, got %d", len(store.gotFilters))
	}
	if !reflect.DeepEqual(store.gotFilters[0].DocumentIDs, []uint{10, 20}) {
		t.Errorf("filter DocumentIDs = %v, want [10 20]", store.gotFilters[0].DocumentIDs)
	}
	if store.gotFilters[0].UserID != 1 {
		t.Errorf("filter UserID = %d, want 1", store.gotFilters[0].UserID)
	}
	if store.gotLimits[0] != 10 {
		t.Errorf("search limit = %d, want 10 (opts.TopK)", store.gotLimits[0])
	}
}

func TestRetrieve_Scope_AllEnabled(t *testing.T) {
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q1": {chunk("a", 7)},
	}}
	docStore := &fakeDocStore{ids: []uint{7, 8, 9}}
	svc := NewService(store, nil, docStore)

	res, err := svc.Retrieve(context.Background(), "q1",
		Scope{UserID: 42, AllEnabled: true},
		Options{TopK: 5},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if docStore.callCount != 1 || docStore.gotUserID != 42 {
		t.Errorf("docStore should be called once with userID=42, got count=%d userID=%d", docStore.callCount, docStore.gotUserID)
	}
	// 解析出的 docIDs 应进入 filter
	if !reflect.DeepEqual(store.gotFilters[0].DocumentIDs, []uint{7, 8, 9}) {
		t.Errorf("filter DocumentIDs = %v, want [7 8 9]", store.gotFilters[0].DocumentIDs)
	}
	if len(res.Chunks) != 1 {
		t.Errorf("want 1 chunk, got %d", len(res.Chunks))
	}
}

func TestRetrieve_Scope_Empty_ReturnsErrEmptyScope(t *testing.T) {
	store := &fakeVectorStore{}
	svc := NewService(store, nil, nil)

	_, err := svc.Retrieve(context.Background(), "q1",
		Scope{UserID: 1}, // 既无 DocumentIDs 也未 AllEnabled
		Options{TopK: 10},
	)
	if !errors.Is(err, ErrEmptyScope) {
		t.Fatalf("want ErrEmptyScope, got %v", err)
	}
	// 严格模式：不应触发任何检索
	if len(store.gotFilters) != 0 {
		t.Errorf("expected no search calls on empty scope, got %d", len(store.gotFilters))
	}
}

func TestRetrieve_Scope_AllEnabled_NoDocStore_Errors(t *testing.T) {
	store := &fakeVectorStore{}
	svc := NewService(store, nil, nil) // docStore == nil

	_, err := svc.Retrieve(context.Background(), "q1",
		Scope{UserID: 1, AllEnabled: true},
		Options{TopK: 10},
	)
	if err == nil {
		t.Fatal("want error when AllEnabled but no DocStore, got nil")
	}
	if errors.Is(err, ErrEmptyScope) {
		t.Errorf("AllEnabled-without-docstore should NOT be ErrEmptyScope, got %v", err)
	}
}

func TestRetrieve_Scope_AllEnabled_DocStoreError_Propagates(t *testing.T) {
	store := &fakeVectorStore{}
	docStore := &fakeDocStore{err: errors.New("db down")}
	svc := NewService(store, nil, docStore)

	_, err := svc.Retrieve(context.Background(), "q1",
		Scope{UserID: 1, AllEnabled: true},
		Options{TopK: 10},
	)
	if err == nil {
		t.Fatal("want error when docStore fails, got nil")
	}
	if len(store.gotFilters) != 0 {
		t.Errorf("expected no search calls when scope resolution fails, got %d", len(store.gotFilters))
	}
}

// --- tests: dedup（按 chunk.ID，顺序与 sales_rag.go 一致）---

func TestRetrieve_Dedup_ByChunkID_PreservesFirstSeenOrder(t *testing.T) {
	// q1 返回 a,b ; q2 返回 b,c,a ; q3 返回 d
	// 期望去重后保留首次出现顺序：a,b,c,d
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q1": {chunk("a", 1), chunk("b", 1)},
		"q2": {chunk("b", 1), chunk("c", 1), chunk("a", 1)},
		"q3": {chunk("d", 1)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "ignored",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, PrewrittenQueries: []string{"q1", "q2", "q3"}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotIDs := make([]string, len(res.Chunks))
	for i, c := range res.Chunks {
		gotIDs[i] = c.ID
	}
	// 注意：parallelSearch 是并发收集，跨 query 的相对顺序取决于 goroutine 完成顺序，
	// 但同一 query 内部顺序确定，且去重保证每个 ID 只出现一次。
	// 这里断言去重后 ID 集合正确且每个唯一（与 sales_rag.go seenIDs 行为一致）。
	if len(res.Chunks) != 4 {
		t.Fatalf("want 4 unique chunks after dedup, got %d (%v)", len(res.Chunks), gotIDs)
	}
	seen := map[string]int{}
	for _, id := range gotIDs {
		seen[id]++
	}
	for _, want := range []string{"a", "b", "c", "d"} {
		if seen[want] != 1 {
			t.Errorf("chunk %q appeared %d times, want exactly 1; got order %v", want, seen[want], gotIDs)
		}
	}
}

func TestRetrieve_Dedup_SingleQueryOrderStable(t *testing.T) {
	// 单 query 路径，去重后顺序应与 Search 返回顺序逐位一致
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q": {chunk("x", 1), chunk("y", 1), chunk("x", 1), chunk("z", 1)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotIDs := make([]string, len(res.Chunks))
	for i, c := range res.Chunks {
		gotIDs[i] = c.ID
	}
	want := []string{"x", "y", "z"}
	if !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("dedup order = %v, want %v", gotIDs, want)
	}
}

// --- tests: query 确定 ---

func TestDetermineQueries_PrewrittenWins(t *testing.T) {
	rw := &fakeRewriter{result: port.RewriteResult{Queries: []string{"rewritten"}}}
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{"pre1": {chunk("a", 1)}}}
	svc := NewService(store, rw, nil)

	res, err := svc.Retrieve(context.Background(), "orig",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RewriteQuery: true, PrewrittenQueries: []string{"pre1", "pre2"}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Prewritten 优先 → rewriter 不应被调用（保 I1）
	if rw.callCount != 0 {
		t.Errorf("rewriter should NOT be called when PrewrittenQueries set, got %d calls", rw.callCount)
	}
	if !reflect.DeepEqual(res.RewriteQueries, []string{"pre1", "pre2"}) {
		t.Errorf("RewriteQueries = %v, want [pre1 pre2]", res.RewriteQueries)
	}
}

func TestDetermineQueries_RewriteWithHyDEAppended(t *testing.T) {
	rw := &fakeRewriter{result: port.RewriteResult{
		Queries: []string{"rw1", "rw2", "rw3"},
		HyDE:    "hyde-doc",
	}}
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{}}
	svc := NewService(store, rw, nil)

	res, err := svc.Retrieve(context.Background(), "orig",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RewriteQuery: true, History: []string{"h1"}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rw.callCount != 1 {
		t.Errorf("rewriter should be called once, got %d", rw.callCount)
	}
	if rw.gotQuery != "orig" || !reflect.DeepEqual(rw.gotHist, []string{"h1"}) {
		t.Errorf("rewriter got query=%q hist=%v, want orig / [h1]", rw.gotQuery, rw.gotHist)
	}
	// HyDE 追加到末尾，顺序与 sales_rag.go allSearchQueries 一致
	want := []string{"rw1", "rw2", "rw3", "hyde-doc"}
	if !reflect.DeepEqual(res.RewriteQueries, want) {
		t.Errorf("RewriteQueries = %v, want %v", res.RewriteQueries, want)
	}
}

func TestDetermineQueries_RewriteNoHyDE(t *testing.T) {
	rw := &fakeRewriter{result: port.RewriteResult{Queries: []string{"rw1", "rw2"}, HyDE: ""}}
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{}}
	svc := NewService(store, rw, nil)

	res, err := svc.Retrieve(context.Background(), "orig",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RewriteQuery: true},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"rw1", "rw2"}
	if !reflect.DeepEqual(res.RewriteQueries, want) {
		t.Errorf("RewriteQueries = %v, want %v (no HyDE appended)", res.RewriteQueries, want)
	}
}

func TestDetermineQueries_FallbackToOriginal(t *testing.T) {
	// RewriteQuery=false 且无 Prewritten → fallback 原 query
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{"orig": {chunk("a", 1)}}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "orig",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10}, // RewriteQuery=false
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(res.RewriteQueries, []string{"orig"}) {
		t.Errorf("RewriteQueries = %v, want [orig]", res.RewriteQueries)
	}
}

func TestDetermineQueries_RewriteQueryButNilRewriter_Fallback(t *testing.T) {
	// RewriteQuery=true 但 rewriter==nil → fallback 原 query（不 panic）
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{"orig": {chunk("a", 1)}}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "orig",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RewriteQuery: true},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(res.RewriteQueries, []string{"orig"}) {
		t.Errorf("RewriteQueries = %v, want [orig]", res.RewriteQueries)
	}
}

func TestDetermineQueries_RewriterError_Propagates(t *testing.T) {
	rw := &fakeRewriter{err: errors.New("rewrite boom")}
	store := &fakeVectorStore{}
	svc := NewService(store, rw, nil)

	_, err := svc.Retrieve(context.Background(), "orig",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RewriteQuery: true},
	)
	if err == nil {
		t.Fatal("want error when rewriter fails, got nil")
	}
	if len(store.gotFilters) != 0 {
		t.Errorf("expected no search calls when rewrite fails, got %d", len(store.gotFilters))
	}
}

// --- tests: parallelSearch 错误语义 ---

func TestParallelSearch_AllFail_ReturnsError(t *testing.T) {
	store := &fakeVectorStore{
		byQuery:   map[string][]domain.KnowledgeChunk{},
		searchErr: map[string]error{"q1": errors.New("boom1"), "q2": errors.New("boom2")},
	}
	svc := NewService(store, nil, nil)

	_, err := svc.Retrieve(context.Background(), "ignored",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, PrewrittenQueries: []string{"q1", "q2"}},
	)
	if err == nil {
		t.Fatal("want error when all search queries fail, got nil")
	}
}

func TestParallelSearch_PartialFail_KeepsSuccess(t *testing.T) {
	store := &fakeVectorStore{
		byQuery:   map[string][]domain.KnowledgeChunk{"ok": {chunk("a", 1)}},
		searchErr: map[string]error{"bad": errors.New("boom")},
	}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "ignored",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, PrewrittenQueries: []string{"ok", "bad"}},
	)
	if err != nil {
		t.Fatalf("partial failure should not error, got %v", err)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].ID != "a" {
		t.Errorf("want only successful chunk 'a', got %+v", res.Chunks)
	}
}

// --- tests: rerank 降级（headless aiservice → Rerank 返错 → fallback 原 topN）---

func TestRetrieve_RerankFallback_OnRerankError(t *testing.T) {
	// headless aiservice (TestMain: no providers) → aiservice.Rerank 返错
	// → 应 fallback 到原始 chunks 的前 RerankTopN 条（对齐 sales_rag.go 降级语义）。
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q": {chunk("a", 1), chunk("b", 1), chunk("c", 1), chunk("d", 1)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RerankTopN: 2, BillingLabel: "test_rerank"},
	)
	if err != nil {
		t.Fatalf("rerank failure should fallback, not error: %v", err)
	}
	// fallback 取前 RerankTopN=2 条
	if len(res.Chunks) != 2 {
		t.Fatalf("want 2 chunks after rerank-fallback, got %d", len(res.Chunks))
	}
	if res.Chunks[0].ID != "a" || res.Chunks[1].ID != "b" {
		t.Errorf("fallback should keep first 2 in order (a,b), got %s,%s", res.Chunks[0].ID, res.Chunks[1].ID)
	}
}

func TestRetrieve_RerankFallback_FewerThanTopN(t *testing.T) {
	// chunks 少于 RerankTopN：fallback 返回全部（不越界）
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q": {chunk("a", 1), chunk("b", 1)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RerankTopN: 5, BillingLabel: "test_rerank"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Chunks) != 2 {
		t.Errorf("want all 2 chunks when fewer than topN, got %d", len(res.Chunks))
	}
}

func TestRetrieve_RerankSkipped_WhenTopNZero(t *testing.T) {
	// RerankTopN=0 → 不重排，返回去重后的全部 chunks（不触发 aiservice）
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q": {chunk("a", 1), chunk("b", 1), chunk("c", 1)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RerankTopN: 0},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Chunks) != 3 {
		t.Errorf("want 3 chunks (no rerank), got %d", len(res.Chunks))
	}
}

func TestRetrieve_RerankSingleChunk_ShortCircuit(t *testing.T) {
	// len(chunks)<=1 时 rerankWithLimit 直接返回（不调 aiservice），与 sales_rag.go 一致
	store := &fakeVectorStore{byQuery: map[string][]domain.KnowledgeChunk{
		"q": {chunk("only", 1)},
	}}
	svc := NewService(store, nil, nil)

	res, err := svc.Retrieve(context.Background(), "q",
		Scope{UserID: 1, DocumentIDs: []uint{1}},
		Options{TopK: 10, RerankTopN: 5, BillingLabel: "test_rerank"},
	)
	if err != nil {
		t.Fatalf("single-chunk rerank should not error, got %v", err)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].ID != "only" {
		t.Errorf("want single chunk 'only', got %+v", res.Chunks)
	}
}

// --- bug-from-customer 复现测试（Rule 11） ---
//
// 客户在 dev 实测：chatbot「AI 时代的创业第一课」（KB《创始人手册》）问
// 「创业要经过哪几个阶段」，召回的全是相关度 40-50% 的「老板做 IP/市场营销」
// 不相关内容。rerank 阈值 0.3 + 保底(keep top-1)把这些低相关度垃圾也当「资料」
// 喂进 prompt，bot 忠实 grounding 后给困惑回答。
//
// 根因 F2：检索主干没有「全部低于阈值时整体丢弃」的机制——
//   - 阈值写死 0.3 常量，无法按调用点抬高；
//   - 第一条永远保底（floor），即便它只有 0.45 的相关度。
//
// 期望（修复后）：调用点能传高阈值 + NoFloor，使「全部 chunk 都低于阈值」时
// 返回空，让 chatbot 走纯聊天而非 grounding 在垃圾上。
//
// 本测试针对纯过滤逻辑 applyRerankFilter（从 rerankWithLimit 抽出，可控 score，
// 不依赖 headless aiservice 的降级路径）：
//   - 修复前 FAIL（applyRerankFilter / Options.RerankMinScore / Options.RerankNoFloor
//     皆不存在 → 编译失败 = bug 在「无机制丢弃低相关度」上得证）；
//   - 修复后 PASS。
func TestApplyRerankFilter_HighThresholdNoFloor_DropsAllLowRelevance(t *testing.T) {
	// 模拟客户场景：4 个 chunk，rerank 分都在 40-50%（全是不相关内容）。
	chunks := []domain.KnowledgeChunk{
		chunk("ip", 1), chunk("marketing", 1), chunk("brand", 1), chunk("traffic", 1),
	}
	rerankResults := []aiservice.RerankResult{
		{Index: 0, Score: 0.48},
		{Index: 1, Score: 0.45},
		{Index: 2, Score: 0.42},
		{Index: 3, Score: 0.40},
	}

	// chatbot 调用点：高阈值 0.6 + NoFloor=true → 全部低于阈值 → 返回空。
	got := applyRerankFilter(chunks, rerankResults, Options{
		RerankMinScore: 0.6,
		RerankNoFloor:  true,
	})
	if len(got) != 0 {
		ids := make([]string, len(got))
		for i, c := range got {
			ids[i] = c.ID
		}
		t.Fatalf("high threshold + NoFloor: want 0 chunks (all 40-50%% below 0.6 dropped), got %d %v", len(got), ids)
	}
}

// TestApplyRerankFilter_DefaultOptions_SalesragBehaviorUnchanged 锁死 salesrag
// 现有行为：默认 Options（RerankMinScore=0 → 用 0.3 常量；RerankNoFloor=false → 保底）
// 必须与抽取前逐位一致——第一条始终保留，后续 <0.3 丢弃，结果空时兜底 top-1。
func TestApplyRerankFilter_DefaultOptions_SalesragBehaviorUnchanged(t *testing.T) {
	chunks := []domain.KnowledgeChunk{
		chunk("hi", 1), chunk("mid", 1), chunk("lo", 1),
	}
	rerankResults := []aiservice.RerankResult{
		{Index: 0, Score: 0.9},  // 第一条：保留
		{Index: 1, Score: 0.5},  // >=0.3：保留
		{Index: 2, Score: 0.12}, // <0.3：丢弃
	}
	got := applyRerankFilter(chunks, rerankResults, Options{}) // 默认值
	if len(got) != 2 || got[0].ID != "hi" || got[1].ID != "mid" {
		ids := make([]string, len(got))
		for i, c := range got {
			ids[i] = c.ID
		}
		t.Fatalf("default opts: want [hi mid] (0.3 阈值, lo 丢弃), got %v", ids)
	}
	if got[0].Score != 0.9 || got[1].Score != 0.5 {
		t.Errorf("scores should be written from rerank: got %v %v", got[0].Score, got[1].Score)
	}
}

// TestApplyRerankFilter_DefaultFloor_KeepsTop1WhenAllBelow 锁死 salesrag 保底语义：
// 默认（NoFloor=false）下，即便全部低于阈值，第一条永远保留（兜底 top-1）。
func TestApplyRerankFilter_DefaultFloor_KeepsTop1WhenAllBelow(t *testing.T) {
	chunks := []domain.KnowledgeChunk{chunk("a", 1), chunk("b", 1)}
	rerankResults := []aiservice.RerankResult{
		{Index: 0, Score: 0.2}, // 第一条：保底保留（即便 <0.3）
		{Index: 1, Score: 0.1}, // <0.3：丢弃
	}
	got := applyRerankFilter(chunks, rerankResults, Options{}) // 默认保底
	if len(got) != 1 || got[0].ID != "a" {
		ids := make([]string, len(got))
		for i, c := range got {
			ids[i] = c.ID
		}
		t.Fatalf("default floor: want [a] (top-1 保底), got %v", ids)
	}
}
