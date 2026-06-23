package feishu

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeNonceStore is an in-memory NonceStore for unit tests — no live Redis.
// Put records a nonce; Consume returns true exactly once per nonce then deletes
// it (mirrors Redis GETDEL one-time semantics), enabling replay tests offline.
type fakeNonceStore struct {
	seen map[string]struct{}
}

func newFakeNonceStore() *fakeNonceStore { return &fakeNonceStore{seen: map[string]struct{}{}} }

func (f *fakeNonceStore) Put(_ context.Context, nonce string, _ time.Duration) error {
	f.seen[nonce] = struct{}{}
	return nil
}

func (f *fakeNonceStore) Consume(_ context.Context, nonce string) (bool, error) {
	if _, ok := f.seen[nonce]; !ok {
		return false, nil
	}
	delete(f.seen, nonce)
	return true, nil
}

// testKey is a deterministic 32-byte base64 key (content irrelevant to tests).
const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

func newTestSigner(t *testing.T) (*StateSigner, *fakeNonceStore) {
	t.Helper()
	store := newFakeNonceStore()
	s, err := NewStateSigner(testKey, store)
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	return s, store
}

func samplePayload() Payload {
	return Payload{
		UserID:       42,
		RunID:        "run-abc",
		Step:         "authorize",
		QuestionText: "请授权飞书以便我把文档写进你的云空间",
	}
}

func TestSignVerify_RoundTrip(t *testing.T) {
	s, _ := newTestSigner(t)
	ctx := context.Background()

	state, err := s.Sign(ctx, samplePayload(), 10*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if state == "" {
		t.Fatal("Sign returned empty state")
	}

	got, err := s.Verify(ctx, state)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.UserID != 42 || got.RunID != "run-abc" || got.Step != "authorize" {
		t.Fatalf("payload mismatch: %+v", got)
	}
	if got.QuestionText != "请授权飞书以便我把文档写进你的云空间" {
		t.Fatalf("question_text mismatch: %q", got.QuestionText)
	}
	if got.Nonce == "" {
		t.Fatal("nonce should be populated by Sign")
	}
	if got.Exp <= time.Now().Unix() {
		t.Fatalf("exp should be in the future, got %d", got.Exp)
	}
}

func TestVerify_TamperedHMAC(t *testing.T) {
	s, _ := newTestSigner(t)
	ctx := context.Background()

	state, err := s.Sign(ctx, samplePayload(), 10*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Flip a byte in the decoded blob (corrupts payload or MAC) and re-encode.
	raw, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		t.Fatalf("decode state: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	if _, err := s.Verify(ctx, tampered); err == nil {
		t.Fatal("Verify should reject tampered HMAC")
	}
}

func TestVerify_Expired(t *testing.T) {
	s, _ := newTestSigner(t)
	ctx := context.Background()

	// Negative validity → exp already in the past.
	state, err := s.Sign(ctx, samplePayload(), -1*time.Second)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	_, err = s.Verify(ctx, state)
	if err == nil {
		t.Fatal("Verify should reject expired state")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "expir") {
		t.Fatalf("expected expiry error, got: %v", err)
	}
}

// TestVerify_ReplayRejected is the core anti-replay (防重放) acceptance test:
// the same valid state verifies once, then a second verify fails because the
// nonce was consumed (deleted) on first use.
func TestVerify_ReplayRejected(t *testing.T) {
	s, _ := newTestSigner(t)
	ctx := context.Background()

	state, err := s.Sign(ctx, samplePayload(), 10*time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if _, err := s.Verify(ctx, state); err != nil {
		t.Fatalf("first Verify should succeed: %v", err)
	}

	// Second verify of the SAME (still-unexpired, HMAC-valid) state must fail —
	// nonce already consumed → replay defeated.
	if _, err := s.Verify(ctx, state); err == nil {
		t.Fatal("Verify should reject replayed state (nonce already consumed)")
	}
}

func TestVerify_GarbageInput(t *testing.T) {
	s, _ := newTestSigner(t)
	ctx := context.Background()

	for _, in := range []string{"", "not-base64-!!!", "YWJj"} { // "abc" decodes but too short for MAC
		if _, err := s.Verify(ctx, in); err == nil {
			t.Fatalf("Verify should reject garbage input %q", in)
		}
	}
}

func TestNewStateSigner_BadKey(t *testing.T) {
	store := newFakeNonceStore()
	cases := []string{"", "not-base64-!!!", base64.StdEncoding.EncodeToString([]byte("too-short"))}
	for _, k := range cases {
		if _, err := NewStateSigner(k, store); err == nil {
			t.Fatalf("NewStateSigner should reject bad key %q", k)
		}
	}
}

func TestNewStateSigner_NilStore(t *testing.T) {
	if _, err := NewStateSigner(testKey, nil); err == nil {
		t.Fatal("NewStateSigner should reject nil nonce store")
	}
}

// TestDecodeStateKey covers the single source of truth for state-key validation
// shared by NewStateSigner and the startup fail-fast check.
func TestDecodeStateKey(t *testing.T) {
	bad := []string{
		"",               // empty
		"not-base64-!!!", // invalid base64
		base64.StdEncoding.EncodeToString([]byte("too-short")), // 9 bytes < 16
	}
	for _, k := range bad {
		if _, err := DecodeStateKey(k); err == nil {
			t.Fatalf("DecodeStateKey should reject bad key %q", k)
		}
	}

	raw, err := DecodeStateKey(testKey)
	if err != nil {
		t.Fatalf("DecodeStateKey rejected valid key: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("expected 32 decoded bytes, got %d", len(raw))
	}
}

// TestMustValidateStateKey is the startup fail-fast regression guard: a missing
// or malformed key must panic (aborting process startup), while a valid key must
// not — mirroring crypto.MustInit for the AES key.
func TestMustValidateStateKey(t *testing.T) {
	t.Run("panic on empty", func(t *testing.T) {
		assertPanics(t, func() { MustValidateStateKey("") })
	})
	t.Run("panic on invalid base64", func(t *testing.T) {
		assertPanics(t, func() { MustValidateStateKey("not-base64-!!!") })
	})
	t.Run("panic on too short", func(t *testing.T) {
		short := base64.StdEncoding.EncodeToString([]byte("too-short"))
		assertPanics(t, func() { MustValidateStateKey(short) })
	})
	t.Run("no panic on valid", func(t *testing.T) {
		assertNotPanics(t, func() { MustValidateStateKey(testKey) })
	})
}

func assertPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}

func assertNotPanics(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic, got %v", r)
		}
	}()
	fn()
}

// TestVerify_NonceStoreError ensures a transient store error surfaces (not a
// silent pass) — replay protection must fail closed.
func TestVerify_NonceStoreError(t *testing.T) {
	store := &erroringNonceStore{}
	s, err := NewStateSigner(testKey, store)
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	ctx := context.Background()
	// Sign uses Put which errors here; Sign should surface it.
	if _, err := s.Sign(ctx, samplePayload(), 10*time.Minute); err == nil {
		t.Fatal("Sign should surface nonce store Put error")
	}
}

type erroringNonceStore struct{}

func (erroringNonceStore) Put(context.Context, string, time.Duration) error {
	return errors.New("boom")
}
func (erroringNonceStore) Consume(context.Context, string) (bool, error) {
	return false, errors.New("boom")
}
