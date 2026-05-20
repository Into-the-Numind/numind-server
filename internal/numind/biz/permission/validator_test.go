package permission

import (
	"context"
	"testing"
)

// stubValidator 用于 compile-time check 接口实现 + pipeline 测试 fixture
type stubValidator struct {
	id     string
	result PermissionResult
}

func (s *stubValidator) ID() string { return s.id }
func (s *stubValidator) Validate(_ context.Context, _ PermissionRequest) PermissionResult {
	return s.result
}

func TestValidator_InterfaceContract(t *testing.T) {
	var _ Validator = (*stubValidator)(nil) // compile-time check
}

func TestStubValidator_ReturnsConfiguredResult(t *testing.T) {
	want := Deny("Stub", DecisionReasonRule, "deny msg")
	v := &stubValidator{id: "Stub", result: want}
	got := v.Validate(context.Background(), PermissionRequest{})
	if got.Behavior != want.Behavior || got.Message != want.Message {
		t.Errorf("stub validator behavior mismatch: got %+v want %+v", got, want)
	}
	if v.ID() != "Stub" {
		t.Errorf("stub ID() wrong")
	}
}
