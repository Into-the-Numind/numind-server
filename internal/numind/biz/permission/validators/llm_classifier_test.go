package validators

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/numind/biz/permission"
	"numind-server/internal/pkg/aiservice"
)

// --- chatFn seam helpers ---

type fakeChatFn struct {
	content string
	err     error
}

func withFakeChatFn(t *testing.T, fake *fakeChatFn) func() {
	t.Helper()
	orig := chatFn
	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		if fake.err != nil {
			return nil, fake.err
		}
		return &aiservice.ChatResponse{Content: fake.content}, nil
	}
	return func() { chatFn = orig }
}

// --- AiserviceLLMClassifier unit tests ---

func TestAIServicePermissionClassifier_Confirm(t *testing.T) {
	restore := withFakeChatFn(t, &fakeChatFn{content: "confirm"})
	defer restore()

	c := NewAIServiceLLMClassifier()
	needsConfirm, err := c.Classify(context.Background(), "bash_exec", `{"command":"rm -rf /tmp/foo"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !needsConfirm {
		t.Error("want needsConfirm=true when LLM returns 'confirm'")
	}
}

func TestAIServicePermissionClassifier_Allow(t *testing.T) {
	restore := withFakeChatFn(t, &fakeChatFn{content: "allow"})
	defer restore()

	c := NewAIServiceLLMClassifier()
	needsConfirm, err := c.Classify(context.Background(), "read_file", `{"path":"/etc/hosts"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if needsConfirm {
		t.Error("want needsConfirm=false when LLM returns 'allow'")
	}
}

func TestAIServicePermissionClassifier_Timeout_FailAllow(t *testing.T) {
	restore := withFakeChatFn(t, &fakeChatFn{err: context.DeadlineExceeded})
	defer restore()

	c := NewAIServiceLLMClassifier()
	needsConfirm, err := c.Classify(context.Background(), "bash_exec", `{"command":"rm -rf /"}`)
	if err != nil {
		t.Fatalf("unexpected non-nil error: %v (want nil for fail-allow)", err)
	}
	if needsConfirm {
		t.Error("want needsConfirm=false on timeout — fail-allow direction")
	}
}

func TestAIServicePermissionClassifier_Error_FailAllow(t *testing.T) {
	restore := withFakeChatFn(t, &fakeChatFn{err: errors.New("network unavailable")})
	defer restore()

	c := NewAIServiceLLMClassifier()
	needsConfirm, err := c.Classify(context.Background(), "bash_exec", `{"command":"rm -rf /"}`)
	if err != nil {
		t.Fatalf("unexpected non-nil error: %v (want nil for fail-allow)", err)
	}
	if needsConfirm {
		t.Error("want needsConfirm=false on generic error — fail-allow direction")
	}
}

// --- AutoModeLLMValidator integration tests ---

func TestAutoModeLLMValidator_Ask_WhenClassifierConfirm(t *testing.T) {
	restore := withFakeChatFn(t, &fakeChatFn{content: "confirm"})
	defer restore()

	v := NewAutoModeLLMValidator(NewAIServiceLLMClassifier())
	req := permission.PermissionRequest{
		Tool:      newFakeDestructiveTool("bash_exec"),
		InputJSON: mustJSON(map[string]any{"command": "rm -rf /tmp"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorAsk {
		t.Errorf("want BehaviorAsk, got %q", got.Behavior)
	}
	if got.DecisionReason != permission.DecisionReasonClassifier {
		t.Errorf("want reason=classifier, got %q", got.DecisionReason)
	}
}

func TestAutoModeLLMValidator_Passthrough_WhenClassifierAllow(t *testing.T) {
	restore := withFakeChatFn(t, &fakeChatFn{content: "allow"})
	defer restore()

	v := NewAutoModeLLMValidator(NewAIServiceLLMClassifier())
	req := permission.PermissionRequest{
		Tool:      newFakeTool("read_file"),
		InputJSON: mustJSON(map[string]any{"path": "/tmp/data.txt"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want BehaviorPassthrough, got %q", got.Behavior)
	}
}

func TestAutoModeLLMValidator_Passthrough_WhenNilTool(t *testing.T) {
	v := NewAutoModeLLMValidator(NewAIServiceLLMClassifier())
	req := permission.PermissionRequest{
		Tool:      nil,
		InputJSON: "{}",
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want BehaviorPassthrough for nil tool, got %q", got.Behavior)
	}
}

func TestAutoModeLLMValidator_ID(t *testing.T) {
	v := NewAutoModeLLMValidator(NewAIServiceLLMClassifier())
	if v.ID() != "AutoModeLLMValidator" {
		t.Errorf("want ID AutoModeLLMValidator, got %q", v.ID())
	}
}
