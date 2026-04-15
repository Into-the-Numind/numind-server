package aiservice_test

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Mock Provider
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
		TaskID: "test.err",
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
		TaskID: "test.missing",
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

// TestGateway_SetDefault_Singleton verifies SetDefault/Default work correctly.
func TestGateway_SetDefault_Singleton(t *testing.T) {
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

	// Restore original to avoid polluting other test runs.
	aiservice.SetDefault(gw1)
}
