package middleware

import (
	"context"
	"testing"
)

func TestReservationRef_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// 注入后可取回原值。
	got := ReservationRefFromCtx(WithReservationRef(ctx, "sop_run:5:9"))
	if got != "sop_run:5:9" {
		t.Fatalf("round-trip: want %q, got %q", "sop_run:5:9", got)
	}

	// 未注入时返回 ""。
	if v := ReservationRefFromCtx(ctx); v != "" {
		t.Fatalf("absent: want empty, got %q", v)
	}

	// "" 为 no-op：返回同一个 ctx（不污染），取回仍为 ""。
	if WithReservationRef(ctx, "") != ctx {
		t.Fatal(`WithReservationRef(ctx, "") should return the same ctx (no-op)`)
	}
	if v := ReservationRefFromCtx(WithReservationRef(ctx, "")); v != "" {
		t.Fatalf(`empty inject: want empty, got %q`, v)
	}
}
