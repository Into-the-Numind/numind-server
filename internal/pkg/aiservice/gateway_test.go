package aiservice_test

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Mock Provider (Chat)
// ----------------------------------------------------------------------------

type mockProvider struct {
	name     string
	chatResp *aiservice.ChatResponse
	chatErr  error
}

func (m *mockProvider) Name() string           { return m.name }
func (m *mockProvider) ProviderType() string   { return "mock" }
func (m *mockProvider) Capabilities() []string { return []string{"chat"} }
func (m *mockProvider) Chat(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return m.chatResp, m.chatErr
}
func (m *mockProvider) ChatStream(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return nil, errors.New("mock: stream not implemented")
}

// ----------------------------------------------------------------------------
// Mock Embed Provider
// ----------------------------------------------------------------------------

type mockEmbedProvider struct {
	name      string
	embedResp *aiservice.EmbedResponse
	embedErr  error
}

func (m *mockEmbedProvider) Name() string           { return m.name }
func (m *mockEmbedProvider) ProviderType() string   { return "mock" }
func (m *mockEmbedProvider) Capabilities() []string { return []string{"embed"} }
func (m *mockEmbedProvider) Embed(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.EmbedRequest) (*aiservice.EmbedResponse, error) {
	return m.embedResp, m.embedErr
}

// ----------------------------------------------------------------------------
// Mock Rerank Provider
// ----------------------------------------------------------------------------

type mockRerankProvider struct {
	name       string
	rerankResp *aiservice.RerankResponse
	rerankErr  error
}

func (m *mockRerankProvider) Name() string           { return m.name }
func (m *mockRerankProvider) ProviderType() string   { return "mock" }
func (m *mockRerankProvider) Capabilities() []string { return []string{"rerank"} }
func (m *mockRerankProvider) Rerank(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.RerankRequest) (*aiservice.RerankResponse, error) {
	return m.rerankResp, m.rerankErr
}

// ----------------------------------------------------------------------------
// Mock OCR Provider
// ----------------------------------------------------------------------------

type mockOCRProvider struct {
	name    string
	ocrResp *aiservice.OCRResponse
	ocrErr  error
}

func (m *mockOCRProvider) Name() string           { return m.name }
func (m *mockOCRProvider) ProviderType() string   { return "mock" }
func (m *mockOCRProvider) Capabilities() []string { return []string{"ocr"} }
func (m *mockOCRProvider) OCR(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.OCRRequest) (*aiservice.OCRResponse, error) {
	return m.ocrResp, m.ocrErr
}

// ----------------------------------------------------------------------------
// Mock ASR Provider
// ----------------------------------------------------------------------------

type mockASRProvider struct {
	name    string
	asrResp *aiservice.ASRResponse
	asrErr  error
}

func (m *mockASRProvider) Name() string           { return m.name }
func (m *mockASRProvider) ProviderType() string   { return "mock" }
func (m *mockASRProvider) Capabilities() []string { return []string{"asr"} }
func (m *mockASRProvider) ASR(_ context.Context, _ *registry.ResolvedRoute, _ aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
	return m.asrResp, m.asrErr
}

// ----------------------------------------------------------------------------
// Mock Registry
// ----------------------------------------------------------------------------

type mockRegistry struct {
	primary *registry.ResolvedRoute
	err     error
}

func (r *mockRegistry) GetService(_ context.Context, _ uint64) (*model.AIService, error) {
	return nil, nil
}
func (r *mockRegistry) ListServices(_ context.Context, _ registry.ServiceFilter) ([]*model.AIService, error) {
	return nil, nil
}
func (r *mockRegistry) ListServicesPaginated(_ context.Context, _ registry.ServiceFilter, _, _ int) ([]*model.AIService, int64, error) {
	return nil, 0, nil
}
func (r *mockRegistry) SaveService(_ context.Context, _ *model.AIService, _ uint64, _ string) error {
	return nil
}
func (r *mockRegistry) DeprecateService(_ context.Context, _ uint64, _ uint64, _ string, _ string) error {
	return nil
}
func (r *mockRegistry) RestoreService(_ context.Context, _ uint64, _ uint64, _ string, _ string) error {
	return nil
}
func (r *mockRegistry) GetTaskProfile(_ context.Context, _ string) (*model.TaskProfile, error) {
	return nil, nil
}
func (r *mockRegistry) ListTaskProfiles(_ context.Context) ([]*model.TaskProfile, error) {
	return nil, nil
}
func (r *mockRegistry) SaveTaskProfile(_ context.Context, _ *model.TaskProfile, _ []registry.TaskBinding, _ uint64, _ string) error {
	return nil
}
func (r *mockRegistry) ResolveTask(_ context.Context, _ string) (*registry.ResolvedRoute, []registry.ResolvedRoute, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return r.primary, nil, nil
}

func (r *mockRegistry) ResolveByModelKey(_ context.Context, _ string, _ string) (*registry.ResolvedRoute, error) {
	// stub: always return not-found so gateway falls back to profile default
	return nil, errno.ErrAIServiceNotFound
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

// TestGateway_Chat_MockEndToEnd exercises the full Chat path through the
// Gateway: Build → SetMiddlewareChain (passthrough) → RegisterProvider →
// SetDefault → Chat → verify response.
func TestGateway_Chat_MockEndToEnd(t *testing.T) {
	wantContent := "Hello from mock"
	mockProv := &mockProvider{
		name: "mock-provider",
		chatResp: &aiservice.ChatResponse{
			Content:  wantContent,
			Provider: "mock-provider",
		},
	}

	route := &registry.ResolvedRoute{
		TaskID: "test.task",
		Provider: registry.ProviderInfo{
			Name: "mock-provider",
		},
	}

	reg := &mockRegistry{primary: route}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})

	// Passthrough middleware chain (calls next directly).
	passthrough := aiservice.MiddlewareChainFunc(func(next aiservice.GatewayHandler) aiservice.GatewayHandler {
		return next
	})
	gw.SetMiddlewareChain(passthrough)
	gw.RegisterProvider(mockProv)

	resp, err := gw.Chat(context.Background(), "test.task", aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hi"}},
		},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Content != wantContent {
		t.Errorf("got Content=%q, want %q", resp.Content, wantContent)
	}
}

// TestGateway_Chat_ProviderError verifies that provider errors are propagated.
func TestGateway_Chat_ProviderError(t *testing.T) {
	mockProv := &mockProvider{
		name:    "err-provider",
		chatErr: errors.New("upstream timeout"),
	}
	route := &registry.ResolvedRoute{
		TaskID:   "test.err",
		Provider: registry.ProviderInfo{Name: "err-provider"},
	}
	reg := &mockRegistry{primary: route}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(aiservice.MiddlewareChainFunc(func(next aiservice.GatewayHandler) aiservice.GatewayHandler {
		return next
	}))
	gw.RegisterProvider(mockProv)

	_, err := gw.Chat(context.Background(), "test.err", aiservice.ChatRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, mockProv.chatErr) && err.Error() != "upstream timeout" {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestGateway_Chat_NoProvider verifies that a missing provider returns an error.
func TestGateway_Chat_NoProvider(t *testing.T) {
	route := &registry.ResolvedRoute{
		TaskID:   "test.missing",
		Provider: registry.ProviderInfo{Name: "nonexistent"},
	}
	reg := &mockRegistry{primary: route}

	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(aiservice.MiddlewareChainFunc(func(next aiservice.GatewayHandler) aiservice.GatewayHandler {
		return next
	}))
	// Deliberately do NOT register any provider.

	_, err := gw.Chat(context.Background(), "test.missing", aiservice.ChatRequest{})
	if err == nil {
		t.Fatal("expected error for unregistered provider, got nil")
	}
}

// TestGateway_AdapterNames verifies that registered adapters are reported.
func TestGateway_AdapterNames(t *testing.T) {
	gw := aiservice.Build(aiservice.Deps{})
	names := gw.AdapterNames()
	if len(names) != 0 {
		t.Errorf("expected empty adapter list before registration, got %v", names)
	}

	gw.RegisterProvider(&mockProvider{name: "p1"})
	gw.RegisterProvider(&mockProvider{name: "p2"})
	names = gw.AdapterNames()
	if len(names) != 2 {
		t.Errorf("expected 2 adapters, got %d: %v", len(names), names)
	}
}

// TestGateway_SetDefault_Singleton verifies SetDefault/Default work correctly
// without polluting the global singleton between test runs.
func TestGateway_SetDefault_Singleton(t *testing.T) {
	// Restore nil after the test so other tests that expect no singleton don't panic.
	t.Cleanup(func() { aiservice.SetDefault(nil) })

	gw1 := aiservice.Build(aiservice.Deps{})
	aiservice.SetDefault(gw1)
	if aiservice.Default() != gw1 {
		t.Error("Default() did not return the installed gateway")
	}

	// Replace with a second gateway.
	gw2 := aiservice.Build(aiservice.Deps{})
	aiservice.SetDefault(gw2)
	if aiservice.Default() != gw2 {
		t.Error("Default() did not return the replaced gateway")
	}
}

// passthrough returns a MiddlewareChainFunc that calls next directly (no-op chain).
func passthrough() aiservice.MiddlewareChainFunc {
	return func(next aiservice.GatewayHandler) aiservice.GatewayHandler { return next }
}

// makeRoute returns a minimal ResolvedRoute pointing at the given provider name.
func makeRoute(providerName string) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		TaskID:   "test.task",
		Provider: registry.ProviderInfo{Name: providerName},
	}
}

// TestGateway_Embed_Dispatch verifies the Embed dispatch path through Gateway.
func TestGateway_Embed_Dispatch(t *testing.T) {
	wantVec := []float32{0.1, 0.2, 0.3}
	prov := &mockEmbedProvider{
		name: "embed-provider",
		embedResp: &aiservice.EmbedResponse{
			Embeddings: [][]float32{wantVec},
			Provider:   "embed-provider",
		},
	}
	reg := &mockRegistry{primary: makeRoute("embed-provider")}
	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(passthrough())
	gw.RegisterProvider(prov)

	resp, err := gw.Embed(context.Background(), "test.task", aiservice.EmbedRequest{Texts: []string{"hello"}})
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(resp.Embeddings) == 0 || resp.Embeddings[0][0] != wantVec[0] {
		t.Errorf("unexpected embeddings: %v", resp.Embeddings)
	}
}

// TestGateway_Rerank_Dispatch verifies the Rerank dispatch path through Gateway.
func TestGateway_Rerank_Dispatch(t *testing.T) {
	prov := &mockRerankProvider{
		name: "rerank-provider",
		rerankResp: &aiservice.RerankResponse{
			Results:  []aiservice.RerankResult{{Index: 0, Score: 0.95}},
			Provider: "rerank-provider",
		},
	}
	reg := &mockRegistry{primary: makeRoute("rerank-provider")}
	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(passthrough())
	gw.RegisterProvider(prov)

	resp, err := gw.Rerank(context.Background(), "test.task", aiservice.RerankRequest{
		Query:     "test query",
		Documents: []string{"doc1"},
	})
	if err != nil {
		t.Fatalf("Rerank returned error: %v", err)
	}
	if len(resp.Results) == 0 || resp.Results[0].Score != 0.95 {
		t.Errorf("unexpected results: %v", resp.Results)
	}
}

// TestGateway_OCR_Dispatch verifies the OCR dispatch path through Gateway.
func TestGateway_OCR_Dispatch(t *testing.T) {
	prov := &mockOCRProvider{
		name: "ocr-provider",
		ocrResp: &aiservice.OCRResponse{
			Text:     "recognized text",
			Provider: "ocr-provider",
		},
	}
	reg := &mockRegistry{primary: makeRoute("ocr-provider")}
	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(passthrough())
	gw.RegisterProvider(prov)

	resp, err := gw.OCR(context.Background(), "test.task", aiservice.OCRRequest{ImageURL: "https://example.com/img.png"})
	if err != nil {
		t.Fatalf("OCR returned error: %v", err)
	}
	if resp.Text != "recognized text" {
		t.Errorf("unexpected Text: %q", resp.Text)
	}
}

// TestGateway_ASR_Dispatch verifies the ASR dispatch path through Gateway.
func TestGateway_ASR_Dispatch(t *testing.T) {
	prov := &mockASRProvider{
		name: "asr-provider",
		asrResp: &aiservice.ASRResponse{
			Text:     "hello world",
			Provider: "asr-provider",
		},
	}
	reg := &mockRegistry{primary: makeRoute("asr-provider")}
	gw := aiservice.Build(aiservice.Deps{Registry: reg})
	gw.SetMiddlewareChain(passthrough())
	gw.RegisterProvider(prov)

	resp, err := gw.ASR(context.Background(), "test.task", aiservice.ASRRequest{AudioURL: "https://example.com/audio.mp3"})
	if err != nil {
		t.Fatalf("ASR returned error: %v", err)
	}
	if resp.Text != "hello world" {
		t.Errorf("unexpected Text: %q", resp.Text)
	}
}
