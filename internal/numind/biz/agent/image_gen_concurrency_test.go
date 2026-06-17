package agent

import "testing"

func TestImageGenConcurrencyCap_Is6(t *testing.T) {
	if imageGenMaxConcurrentPerUser != 6 {
		t.Fatalf("product rule: per-user image_gen concurrency cap must be 6, got %d", imageGenMaxConcurrentPerUser)
	}
}

func TestUserConcurrencyLimiter_CapsPerUser(t *testing.T) {
	l := newUserConcurrencyLimiter(6)
	uid := uint(42)

	for i := 0; i < 6; i++ {
		if !l.acquire(uid) {
			t.Fatalf("acquire #%d should succeed (under cap)", i+1)
		}
	}
	if l.acquire(uid) {
		t.Error("7th concurrent acquire must fail (over cap)")
	}

	// Releasing one frees a slot.
	l.release(uid)
	if !l.acquire(uid) {
		t.Error("after release, acquire should succeed again")
	}

	// A different user has an independent cap.
	if !l.acquire(uint(99)) {
		t.Error("a different user must have its own independent cap")
	}
}

func TestUserConcurrencyLimiter_MapBoundedAfterRelease(t *testing.T) {
	l := newUserConcurrencyLimiter(2)
	uid := uint(7)
	l.acquire(uid)
	l.acquire(uid)
	l.release(uid)
	l.release(uid)

	l.mu.Lock()
	_, present := l.inUse[uid]
	l.mu.Unlock()
	if present {
		t.Error("entry should be deleted once the user's in-flight count returns to 0")
	}
}
