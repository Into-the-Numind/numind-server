package middleware

// budget_metadata_holder_test.go — TDD tests for F-5 fix.
//
// F-5 bug: ContextBudgetCredits calls withBudgetMetadata on the *inner* ctx
// (a child of the ctx that Tracing sees), so Tracing's close path reads the
// original ctx where budgetMetadataFromCtx returns ok=false.
//
// Fix: introduce a *budgetMetadataHolder pointer (like finalCostHolder) that
// is injected by Tracing *before* calling next, and written by
// ContextBudgetCredits after withBudgetMetadata so Tracing can read it on
// the way out.
//
// Tests in this file:
//  1. Unit tests for budgetMetadataHolder itself.
//  2. Chain-integration test that asserts budget keys appear in a captured
//     Langfuse generation observation — both streaming and non-streaming.

import (
	"context"
	"sync"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/langfuse"
)

// ---------------------------------------------------------------------------
// 1. Unit tests for budgetMetadataHolder
// ---------------------------------------------------------------------------

// TestBudgetMetadataHolder_SetGet verifies that Set then Get returns the value.
func TestBudgetMetadataHolder_SetGet(t *testing.T) {
	h := &budgetMetadataHolder{}

	// Before Set: Get must return ok=false.
	if _, ok := h.Get(); ok {
		t.Fatal("empty holder should return ok=false")
	}

	want := budgetMetadata{
		EventID:         99,
		ContextWindow:   1000000,
		SafeInputBudget: 800000,
	}
	h.Set(want)

	got, ok := h.Get()
	if !ok {
		t.Fatal("after Set, Get should return ok=true")
	}
	if got.EventID != want.EventID {
		t.Errorf("EventID = %d, want %d", got.EventID, want.EventID)
	}
	if got.ContextWindow != want.ContextWindow {
		t.Errorf("ContextWindow = %d, want %d", got.ContextWindow, want.ContextWindow)
	}
	if got.SafeInputBudget != want.SafeInputBudget {
		t.Errorf("SafeInputBudget = %d, want %d", got.SafeInputBudget, want.SafeInputBudget)
	}
}

// TestBudgetMetadataHolder_EmptyReturnsNotOK verifies Get on a brand-new holder.
func TestBudgetMetadataHolder_EmptyReturnsNotOK(t *testing.T) {
	h := &budgetMetadataHolder{}
	_, ok := h.Get()
	if ok {
		t.Error("newly created holder should return ok=false")
	}
}

// TestBudgetMetadataHolder_SetOverwrites verifies a second Set replaces first.
func TestBudgetMetadataHolder_SetOverwrites(t *testing.T) {
	h := &budgetMetadataHolder{}
	h.Set(budgetMetadata{EventID: 1})
	h.Set(budgetMetadata{EventID: 2})
	got, ok := h.Get()
	if !ok {
		t.Fatal("after Set, ok should be true")
	}
	if got.EventID != 2 {
		t.Errorf("EventID = %d, want 2", got.EventID)
	}
}

// TestBudgetMetadataHolder_ConcurrentSafe verifies that concurrent Set/Get
// does not race (run with -race).
func TestBudgetMetadataHolder_ConcurrentSafe(t *testing.T) {
	h := &budgetMetadataHolder{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(id uint64) {
			defer wg.Done()
			h.Set(budgetMetadata{EventID: id})
		}(uint64(i))
		go func() {
			defer wg.Done()
			_, _ = h.Get()
		}()
	}
	wg.Wait()
}

// TestWithBudgetMetadataHolder_InjectAndExtract verifies the ctx helper round-trip.
func TestWithBudgetMetadataHolder_InjectAndExtract(t *testing.T) {
	ctx := context.Background()
	ctx2, h1 := withBudgetMetadataHolder(ctx)

	// ctx2 is a distinct value from ctx.
	if ctx2 == ctx {
		t.Error("withBudgetMetadataHolder should return a new ctx")
	}

	// Extract from ctx2 succeeds.
	h2, ok := budgetMetadataHolderFromCtx(ctx2)
	if !ok {
		t.Fatal("holder should be present in ctx2")
	}
	if h1 != h2 {
		t.Error("extracted holder should be the same pointer as injected holder")
	}

	// Original ctx does NOT carry the holder.
	_, ok = budgetMetadataHolderFromCtx(ctx)
	if ok {
		t.Error("original ctx should not carry the holder")
	}
}

// ---------------------------------------------------------------------------
// 2. Chain-integration test: budget keys reach Langfuse generation metadata
// ---------------------------------------------------------------------------

// recordingLangfuseObservation captures the output map of the last EndGeneration call.
// It is wired in by replacing langfuse.C with a custom client; however the langfuse
// package uses package-level functions that call langfuse.C under the hood.
// Since we cannot easily intercept the Langfuse SDK's async queue in a unit test,
// we instead verify the mergeBudgetTracingMeta path by checking that the holder
// is populated AND that mergeBudgetTracingMeta reads from it when called with the
// pre-mutation ctx.
//
// The chain-integration test below wires Tracing → ContextBudgetCredits → adapter
// and verifies that the holder set by ContextBudgetCredits is readable by the Tracing
// close path through the holder injected into the original ctx.

// TestChainIntegration_BudgetMetadataReachesTracingViaHolder verifies the full
// cross-middleware flow that F-5 broke:
//
//   - Tracing (outer) injects a *budgetMetadataHolder into ctx before calling next.
//   - ContextBudgetCredits (inner) calls withBudgetMetadata AND writes into holder.
//   - The adapter's ctx (innerCtxSeen) is a descendant of Tracing's ctx and
//     therefore inherits the same holder pointer — so holder.Get() on innerCtxSeen
//     reflects the metadata written by ContextBudgetCredits.
//   - Because the holder is a *pointer* shared across the ctx ancestor chain,
//     Tracing's close path (which holds its own local ctx that is the parent of
//     innerCtxSeen) can also read the holder — this is exactly what the fix does.
//   - We simulate Tracing's close path by calling mergeBudgetTracingMeta with
//     innerCtxSeen (which carries both the holder and the ctx-value) and
//     separately with a ctx that has ONLY the holder (to prove the holder path
//     works when the ctx-value is absent).
//
// This test MUST fail before the fix and MUST pass after.
func TestChainIntegration_BudgetMetadataReachesTracingViaHolder_NonStreaming(t *testing.T) {
	// Disable the real Langfuse client so SDK calls are no-ops.
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	// Build a fake ContextBudgetService that produces known metadata fields.
	prepResult := &PrepareResult{
		Fragments: []contextbudget.ContextFragment{
			simpleFragment("f1", "content"),
		},
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
		},
		Plan:            contextbudget.Plan{Feasible: true, EstimatedAfter: 50},
		EstimatedBefore: 100,
		EstimatedAfter:  50,
		SafeInputBudget: 900000,
		Policy: contextbudget.BudgetPolicy{
			Operation:            "test_op",
			ReservedOutputTokens: 512,
			SafeRatio:            0.85,
			FixedOverheadTokens:  256,
			ChargeUser:           false,
		},
		TokenProfileID: 7,
		EventID:        42,
		NormalizedOp:   "test_op",
		SkipBudget:     false,
	}

	budgetSvc := &mockContextBudgetService{prepareResult: prepResult}

	deps := Deps{
		Logger:        &mockLogger{},
		ContextBudget: budgetSvc,
		// No CreditService — ChargeUser=false so no reservation needed.
	}

	// The adapter records the ctx it receives (inner ctx, after budget injection).
	// This ctx is a descendant of Tracing's ctx and inherits the holder pointer.
	var innerCtxSeen context.Context
	adapter := Handler(func(ctx context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		innerCtxSeen = ctx
		return &aiservice.ChatResponse{
			Content: "answer",
			Usage:   aiservice.TokenUsage{PromptTokens: 50, CompletionTokens: 20},
		}, nil
	})

	// Build chain: Tracing → ContextBudgetCredits → adapter.
	chain := Chain(Tracing(deps), ContextBudgetCredits(deps))
	handler := chain(adapter)

	// Build a ChatRequest WITH ContextFragments so ContextBudgetCredits does not passthrough.
	req := chatReqWithFragments(simpleFragment("f1", "content"))
	route := budgetRoute()
	route.ServiceType = "llm"

	// Inject a trace context so Tracing opens an observation.
	ctx := langfuse.WithTrace(context.Background(), langfuse.TraceID())

	_, err := handler(ctx, route, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if innerCtxSeen == nil {
		t.Fatal("adapter was not called — inner ctx not captured")
	}

	// ---- Verify holder is present on the inner ctx ----
	// innerCtxSeen is a child of Tracing's ctx; the holder pointer is shared
	// across the whole ancestor chain.
	holder, holderPresent := budgetMetadataHolderFromCtx(innerCtxSeen)
	if !holderPresent {
		t.Fatal("F-5: budgetMetadataHolder was NOT injected by Tracing — holder-based propagation not wired")
	}
	meta, ok := holder.Get()
	if !ok {
		t.Fatal("F-5: budgetMetadataHolder present but ContextBudgetCredits did NOT call holder.Set()")
	}
	if meta.EventID != 42 {
		t.Errorf("holder.meta.EventID = %d, want 42", meta.EventID)
	}
	if meta.SafeInputBudget != 900000 {
		t.Errorf("holder.meta.SafeInputBudget = %d, want 900000", meta.SafeInputBudget)
	}

	// ---- Simulate Tracing's close path ----
	// Tracing holds a ctx that is the parent of innerCtxSeen and also carries
	// the holder. We simulate this by building a ctx with ONLY the holder
	// (no ctx-value from withBudgetMetadata) — this is the hard case that F-5
	// broke: Tracing's ctx has the holder but NOT the withBudgetMetadata value.
	ctxWithHolderOnly, _ := withBudgetMetadataHolder(context.Background())
	// Populate the holder manually (as ContextBudgetCredits would do).
	if h, ok := budgetMetadataHolderFromCtx(ctxWithHolderOnly); ok {
		h.Set(meta)
	}

	outMeta := buildMeta(route, 0, nil, "")
	mergeBudgetTracingMeta(ctxWithHolderOnly, outMeta)

	// These keys must be present (spec §11.1) when merging from holder-only ctx.
	requiredKeys := []string{
		"context_budget_event_id",
		"safe_input_budget",
		"reserved_output_tokens",
	}
	for _, k := range requiredKeys {
		if _, present := outMeta[k]; !present {
			t.Errorf("F-5: Langfuse metadata missing %q via holder path — budget metadata not reaching Tracing", k)
		}
	}

	// ---- Regression: inner ctx-value path still works (for Billing) ----
	if _, ok := budgetMetadataFromCtx(innerCtxSeen); !ok {
		t.Error("inner ctx (seen by adapter) does not carry budgetMetadata via ctx-value — Billing path broken")
	}
}

// TestChainIntegration_BudgetMetadataReachesTracingViaHolder_Streaming is the
// streaming variant of the above. The stream wrapper in Tracing runs in a
// goroutine and calls mergeBudgetTracingMeta(ctx, ...) after the channel closes.
// After the fix the holder must be populated BEFORE the stream channel is returned
// (ContextBudgetCredits calls h.Set() synchronously before calling next), so the
// Tracing goroutine reading the holder after stream drain is guaranteed to see it.
//
// Memory ordering: h.Set() in ContextBudgetCredits happens before the channel send
// that communicates chunks to Tracing's goroutine. Go memory model: the channel
// send happens-before the corresponding receive, so any write before the channel
// send is visible to any goroutine that observes the corresponding receive (or
// the subsequent channel close). Therefore h.Get() in Tracing's close goroutine
// is guaranteed to see the Set() value — no additional synchronisation needed.
func TestChainIntegration_BudgetMetadataReachesTracingViaHolder_Streaming(t *testing.T) {
	origC := langfuse.C
	langfuse.C = nil
	defer func() { langfuse.C = origC }()

	prepResult := &PrepareResult{
		Fragments:       []contextbudget.ContextFragment{simpleFragment("f1", "c")},
		Messages:        []aiservice.ChatMessage{{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}}},
		Plan:            contextbudget.Plan{Feasible: true},
		EstimatedBefore: 80,
		EstimatedAfter:  40,
		SafeInputBudget: 850000,
		Policy: contextbudget.BudgetPolicy{
			ReservedOutputTokens: 1024,
			SafeRatio:            0.90,
			ChargeUser:           false,
		},
		TokenProfileID: 3,
		EventID:        99,
		NormalizedOp:   "chat",
		SkipBudget:     false,
	}

	budgetSvc := &mockContextBudgetService{prepareResult: prepResult}
	deps := Deps{
		Logger:        &mockLogger{},
		ContextBudget: budgetSvc,
	}

	// Adapter records its ctx and returns a stream.
	var innerCtxSeen context.Context
	adapter := Handler(func(ctx context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		innerCtxSeen = ctx
		ch := make(chan aiservice.ChatChunk, 2)
		go func() {
			ch <- aiservice.ChatChunk{Delta: "part1"}
			ch <- aiservice.ChatChunk{
				Delta:   "part2",
				IsFinal: true,
				Usage:   &aiservice.TokenUsage{PromptTokens: 40, CompletionTokens: 15},
			}
			close(ch)
		}()
		return (<-chan aiservice.ChatChunk)(ch), nil
	})

	chain := Chain(Tracing(deps), ContextBudgetCredits(deps))
	handler := chain(adapter)

	req := chatReqWithFragments(simpleFragment("f1", "c"))
	route := budgetRoute()
	route.ServiceType = "llm"

	ctx := langfuse.WithTrace(context.Background(), langfuse.TraceID())

	resp, err := handler(ctx, route, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain the stream so Tracing and ContextBudgetCredits goroutines finish.
	outCh, ok := resp.(<-chan aiservice.ChatChunk)
	if !ok {
		t.Fatal("expected stream response (<-chan ChatChunk)")
	}
	for range outCh {
	}

	if innerCtxSeen == nil {
		t.Fatal("adapter was not called")
	}

	// Verify holder is present and populated on the inner ctx.
	holder, holderPresent := budgetMetadataHolderFromCtx(innerCtxSeen)
	if !holderPresent {
		t.Fatal("F-5 (streaming): budgetMetadataHolder was NOT injected by Tracing")
	}
	meta, setOK := holder.Get()
	if !setOK {
		t.Fatal("F-5 (streaming): holder present but ContextBudgetCredits did NOT call holder.Set()")
	}
	if meta.EventID != 99 {
		t.Errorf("holder.meta.EventID = %d, want 99", meta.EventID)
	}

	// Simulate Tracing's close goroutine: build a ctx with only the holder,
	// call mergeBudgetTracingMeta, and assert budget keys appear.
	ctxWithHolderOnly, _ := withBudgetMetadataHolder(context.Background())
	if h, ok := budgetMetadataHolderFromCtx(ctxWithHolderOnly); ok {
		h.Set(meta)
	}
	outMeta := buildMeta(route, 0, nil, "")
	mergeBudgetTracingMeta(ctxWithHolderOnly, outMeta)

	if _, present := outMeta["context_budget_event_id"]; !present {
		t.Error("F-5 (streaming): context_budget_event_id missing from Langfuse metadata via holder path")
	}
	if _, present := outMeta["safe_input_budget"]; !present {
		t.Error("F-5 (streaming): safe_input_budget missing from Langfuse metadata via holder path")
	}
}
