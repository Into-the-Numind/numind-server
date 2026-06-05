package agent

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// testSoftDenyCfg is a small-threshold config for deterministic trip tests.
func testSoftDenyCfg() SoftDenyConfig {
	return SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 5, MaxLifetime: 10}
}

// TestSoftDenyController_Resolve_SingleDeny: a single denial is soft (not tripped)
// and the returned message carries the pending reason fed by the permission hook.
func TestSoftDenyController_Resolve_SingleDeny(t *testing.T) {
	c := NewSoftDenyController(testSoftDenyCfg())
	c.SetPending(&PermissionDenialDetail{ToolName: "bash_exec", Message: "命令含毁灭性 rm -rf /"})
	tripped, msg := c.Resolve("bash_exec", `{"command":"rm -rf /"}`)
	if tripped {
		t.Fatalf("single deny should be soft (not tripped), got tripped=true")
	}
	if !strings.Contains(msg, "rm -rf /") {
		t.Errorf("msg should carry the pending reason, got %q", msg)
	}
	if !strings.Contains(msg, "被平台安全策略拦截") {
		t.Errorf("msg should contain the standard soft-deny preamble, got %q", msg)
	}
}

// TestSoftDenyController_Resolve_TripSameConsecutive: maxSame consecutive denials of
// the SAME (tool+input) fingerprint trips (anti-loop on identical retries).
func TestSoftDenyController_Resolve_TripSameConsecutive(t *testing.T) {
	c := NewSoftDenyController(testSoftDenyCfg()) // MaxSame=3
	input := `{"command":"rm -rf /"}`
	for i := 1; i <= 2; i++ {
		if tripped, _ := c.Resolve("bash_exec", input); tripped {
			t.Fatalf("resolve #%d should be soft, got tripped", i)
		}
	}
	if tripped, _ := c.Resolve("bash_exec", input); !tripped {
		t.Errorf("resolve #3 (MaxSame=3) should trip on same fingerprint")
	}
}

// TestSoftDenyController_Resolve_TripTotalConsecutive: maxTotal consecutive denials
// across DIFFERENT fingerprints (sameStreak stays 1) trips on the total counter.
func TestSoftDenyController_Resolve_TripTotalConsecutive(t *testing.T) {
	c := NewSoftDenyController(testSoftDenyCfg()) // MaxTotal=5
	for i := 1; i <= 4; i++ {
		// distinct inputs => distinct fingerprints => sameStreak never reaches MaxSame
		if tripped, _ := c.Resolve("web_fetch", `{"url":"http://10.0.0.`+strconv.Itoa(i)+`"}`); tripped {
			t.Fatalf("resolve #%d should be soft, got tripped", i)
		}
	}
	if tripped, _ := c.Resolve("web_fetch", `{"url":"http://10.0.0.9"}`); !tripped {
		t.Errorf("resolve #5 (MaxTotal=5) should trip on total consecutive")
	}
}

// TestSoftDenyController_Resolve_TripLifetimeAcrossSuccess: interposing a success
// between each denial resets consecutive/sameStreak but NOT the per-fingerprint
// lifetime counter — so a persistent attacker still trips at MaxLifetime (R2-B).
func TestSoftDenyController_Resolve_TripLifetimeAcrossSuccess(t *testing.T) {
	cfg := SoftDenyConfig{Enabled: true, MaxSame: 100, MaxTotal: 100, MaxLifetime: 3}
	c := NewSoftDenyController(cfg)
	input := `{"command":"cat /etc/shadow"}`
	for i := 1; i <= 2; i++ {
		if tripped, _ := c.Resolve("bash_exec", input); tripped {
			t.Fatalf("resolve #%d should be soft (lifetime not reached), got tripped", i)
		}
		c.OnSuccess() // a benign success in between — must NOT clear lifetime
	}
	if tripped, _ := c.Resolve("bash_exec", input); !tripped {
		t.Errorf("resolve #3 (MaxLifetime=3) should trip despite OnSuccess between each")
	}
}

// TestSoftDenyController_OnSuccess_ResetsConsecutive: a success clears the consecutive
// and sameStreak counters so a healthy agent that bounces off one block then makes
// progress never trips.
func TestSoftDenyController_OnSuccess_ResetsConsecutive(t *testing.T) {
	c := NewSoftDenyController(testSoftDenyCfg()) // MaxSame=3, MaxTotal=5
	input := `{"command":"rm -rf /"}`
	c.Resolve("bash_exec", input) // sameStreak=1, consecutive=1
	c.Resolve("bash_exec", input) // sameStreak=2, consecutive=2
	c.OnSuccess()                 // reset
	// After reset, two more same-fp denials must still be soft (streak restarted).
	if tripped, _ := c.Resolve("bash_exec", input); tripped {
		t.Errorf("after OnSuccess, resolve should restart streak (soft), got tripped")
	}
	if tripped, _ := c.Resolve("bash_exec", input); tripped {
		t.Errorf("after OnSuccess, second resolve (streak=2) should be soft, got tripped")
	}
}

// TestSoftDenyController_Enabled: enabled=false reports Enabled()==false (caller falls
// back to legacy hard-terminate).
func TestSoftDenyController_Enabled(t *testing.T) {
	on := NewSoftDenyController(SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 5, MaxLifetime: 10})
	if !on.Enabled() {
		t.Errorf("Enabled() should be true")
	}
	off := NewSoftDenyController(SoftDenyConfig{Enabled: false})
	if off.Enabled() {
		t.Errorf("Enabled() should be false")
	}
}

// TestSoftDenyController_NilPending_NoPanic: if no SetPending was called (e.g. a deny
// source that didn't populate the controller), Resolve must not panic and must return
// a generic message.
func TestSoftDenyController_NilPending_NoPanic(t *testing.T) {
	c := NewSoftDenyController(testSoftDenyCfg())
	c.SetPending(nil) // explicit: a deny source that did not populate a reason
	tripped, msg := c.Resolve("some_tool", `{"x":1}`)
	if tripped {
		t.Errorf("single deny should be soft")
	}
	if msg == "" {
		t.Errorf("msg should be non-empty even with nil pending")
	}
	if !strings.Contains(msg, "被平台安全策略拦截") {
		t.Errorf("generic msg should still contain the preamble, got %q", msg)
	}
}

// TestSoftDenyToolResult_Format: the formatter carries the reason and escalates wording
// on repeated same-fingerprint attempts.
func TestSoftDenyToolResult_Format(t *testing.T) {
	first := softDenyToolResult("内网地址被禁", 1)
	if !strings.Contains(first, "内网地址被禁") {
		t.Errorf("formatter must include reason, got %q", first)
	}
	if strings.Contains(first, "已拦截") {
		t.Errorf("first attempt should not contain escalation wording, got %q", first)
	}
	boundary := softDenyToolResult("内网地址被禁", 2) // exact escalation boundary
	if !strings.Contains(boundary, "请立即停止重试") {
		t.Errorf("streak=2 boundary should escalate wording, got %q", boundary)
	}
	repeated := softDenyToolResult("内网地址被禁", 3)
	if !strings.Contains(repeated, "请立即停止重试") {
		t.Errorf("repeated attempt (streak>=2) should escalate wording, got %q", repeated)
	}
}

// TestSoftDenyController_Resolve_FPChangeResetsStreak: a denial of a DIFFERENT
// fingerprint must restart sameStreak at 1 (not inherit the prior fp's streak), so a
// switch of blocked operation does not falsely accelerate the same-streak trip.
func TestSoftDenyController_Resolve_FPChangeResetsStreak(t *testing.T) {
	cfg := SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 100, MaxLifetime: 100}
	c := NewSoftDenyController(cfg)
	c.Resolve("bash_exec", `{"command":"rm -rf /"}`) // fp-A, sameStreak=1
	c.Resolve("bash_exec", `{"command":"rm -rf /"}`) // fp-A, sameStreak=2
	// switch to fp-B: sameStreak must reset to 1, so this must NOT trip even though
	// total consecutive is now 3 (MaxTotal=100 keeps that path clear).
	if tripped, _ := c.Resolve("bash_exec", `{"command":"mkfs /dev/sda"}`); tripped {
		t.Errorf("fp change should reset sameStreak to 1; should be soft, got tripped")
	}
}

// TestSoftDenyController_CtxRoundTrip: WithSoftDenyController / SoftDenyFromCtx round-trip;
// missing controller returns nil.
func TestSoftDenyController_CtxRoundTrip(t *testing.T) {
	if SoftDenyFromCtx(context.Background()) != nil {
		t.Errorf("missing controller should yield nil")
	}
	c := NewSoftDenyController(testSoftDenyCfg())
	ctx := WithSoftDenyController(context.Background(), c)
	if got := SoftDenyFromCtx(ctx); got != c {
		t.Errorf("SoftDenyFromCtx should return the injected controller")
	}
}

// TestSoftDenyConfig_GlobalDefault: the package-level config defaults to enabled with
// sane thresholds so a missing biz.go wire still soft-intercepts (R2-E safe default).
func TestSoftDenyConfig_GlobalDefault(t *testing.T) {
	cfg := CurrentSoftDenyConfig()
	if !cfg.Enabled {
		t.Errorf("global default should be enabled")
	}
	if cfg.MaxSame <= 0 || cfg.MaxTotal <= 0 || cfg.MaxLifetime <= 0 {
		t.Errorf("global default thresholds must be positive, got %+v", cfg)
	}
}
