package agent

import (
	"context"
	"testing"
)

func TestPermissionSink_RoundTrip(t *testing.T) {
	sink := make(chan *PermissionDenialDetail, 1)
	ctx := WithPermissionSink(context.Background(), sink)
	got := PermissionSinkFromCtx(ctx)
	if got == nil {
		t.Fatalf("expected sink from ctx, got nil")
	}
	// 验证 channel 同一（写一边 + 读一边）
	d := &PermissionDenialDetail{ToolName: "test"}
	got <- d
	got2 := <-sink
	if got2 != d {
		t.Errorf("channel not same instance")
	}
}

func TestPermissionSink_NilCtx(t *testing.T) {
	got := PermissionSinkFromCtx(context.Background())
	if got != nil {
		t.Errorf("expected nil from empty ctx, got %v", got)
	}
}
