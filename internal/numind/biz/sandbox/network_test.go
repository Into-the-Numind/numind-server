package sandbox

import (
	"errors"
	"testing"
)

func TestNetworkPolicyForBackend_NoneReturnsNone(t *testing.T) {
	got, err := NetworkPolicyForBackend(NetworkPolicyNone)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "none" {
		t.Errorf("got %q; want \"none\"", got)
	}
}

func TestNetworkPolicyForBackend_AllowlistReturnsNotImplementedError(t *testing.T) {
	_, err := NetworkPolicyForBackend(NetworkPolicyAllowlist)
	if !errors.Is(err, ErrAllowlistNotImplemented) {
		t.Errorf("expected ErrAllowlistNotImplemented, got %v", err)
	}
}

func TestNetworkPolicyForBackend_UnknownDegradesToNone(t *testing.T) {
	got, err := NetworkPolicyForBackend(NetworkPolicy("weird-value"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "none" {
		t.Errorf("unknown policy degraded to %q; want \"none\" (safe-by-default)", got)
	}
}
