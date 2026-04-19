package monitor

import (
	"testing"
)

func TestCooldownManager_CanCheck(t *testing.T) {
	cm := NewCooldownManager(5, 10)

	if !cm.CanCheck(1) {
		t.Fatal("expected first check to be allowed")
	}

	cm.RecordCheck(1)

	if cm.CanCheck(1) {
		t.Fatal("expected immediate second check to be blocked")
	}

	if !cm.CanCheck(2) {
		t.Fatal("expected different user check to be allowed")
	}
}

func TestCooldownManager_CanAnalyze(t *testing.T) {
	cm := NewCooldownManager(5, 10)

	if !cm.CanAnalyze(100) {
		t.Fatal("expected first analyze to be allowed")
	}

	cm.RecordAnalyze(100)

	if cm.CanAnalyze(100) {
		t.Fatal("expected immediate second analyze to be blocked")
	}

	if !cm.CanAnalyze(200) {
		t.Fatal("expected different note analyze to be allowed")
	}
}

func TestCooldownManager_ZeroCooldown(t *testing.T) {
	cm := NewCooldownManager(0, 0)

	cm.RecordCheck(1)
	// With 0-minute cooldown, should always be allowed
	// (time.Since > 0 minutes is always true)
	if !cm.CanCheck(1) {
		t.Fatal("expected 0-minute cooldown to always allow")
	}
}
