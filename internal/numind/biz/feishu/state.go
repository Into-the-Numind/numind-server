// Package feishu holds the biz-layer building blocks for the 飞书 (Lark)
// integration. This file implements the OAuth callback `state` token: a signed,
// short-lived, one-time-use blob that protects the no-JWT callback endpoint
// against CSRF and replay (design.md §"state 设计（防 CSRF/重放）").
//
// Wire format:
//
//	state = base64url( payloadJSON || HMAC_SHA256(payloadJSON, KEY_STATE) )
//
// where payload = {user_id, run_id, step, question_text, nonce, exp}. The HMAC
// key (security.feishu_state_key) is DELIBERATELY separate from the AES key used
// to encrypt stored tokens (security.thirdparty_token_key) so a leak of one does
// not compromise the other.
//
// Anti-replay: at Sign time the random nonce is written to Redis with TTL=exp.
// At Verify time, after the HMAC + exp checks pass, the nonce is atomically
// read-and-deleted (Redis GETDEL). A second verification of the same (still
// unexpired, HMAC-valid) state therefore fails — the nonce is already gone.
package feishu

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"numind-server/internal/pkg/errno"

	"github.com/redis/go-redis/v9"
)

// macLen is the byte length of an HMAC-SHA256 tag.
const macLen = sha256.Size

// nonceBytes is the entropy of the one-time nonce (128-bit, ample for replay
// protection without bloating the state blob).
const nonceBytes = 16

// Payload is the data carried in (and authenticated by) a state token.
//
// QuestionText is the exact pending-question text the agent run is waiting on;
// the callback uses it as the answer key to resume the run (design.md §auth
// resume: biz.Answer(run_id, {question_text: ...})). Nonce and Exp are filled by
// Sign — callers leave them zero.
type Payload struct {
	UserID       uint   `json:"user_id"`
	RunID        string `json:"run_id"`
	Step         string `json:"step"`
	QuestionText string `json:"question_text"`
	Nonce        string `json:"nonce"`
	Exp          int64  `json:"exp"` // unix seconds; absolute expiry
}

// NonceStore is the one-time-nonce backing store. Production uses Redis
// (RedisNonceStore); tests inject an in-memory fake. Consume must be atomic
// (read-and-delete) and return ok=false once the nonce is gone — this is what
// makes replay impossible.
type NonceStore interface {
	// Put records nonce so a later Consume can find it; ttl bounds its lifetime.
	Put(ctx context.Context, nonce string, ttl time.Duration) error
	// Consume atomically checks-and-deletes nonce. ok=true exactly once per
	// nonce (first caller wins); ok=false thereafter or if never present.
	Consume(ctx context.Context, nonce string) (ok bool, err error)
}

// StateSigner signs and verifies state tokens. It is safe for concurrent use:
// the HMAC key and store are immutable after construction and HMAC is stateless.
type StateSigner struct {
	key   []byte // raw HMAC-SHA256 key (decoded from base64)
	store NonceStore
}

// minStateKeyBytes is the minimum raw key length we accept for the HMAC state
// key. HMAC itself accepts any length, but anything shorter than 16 bytes is too
// weak for a CSRF/replay security boundary.
const minStateKeyBytes = 16

// DecodeStateKey decodes and validates a base64-encoded HMAC state key, returning
// the raw key bytes. It is the single source of truth for state-key validation
// rules (non-empty, valid base64, >= minStateKeyBytes) so that startup fail-fast
// (MustValidateStateKey) and StateSigner construction (NewStateSigner) cannot
// drift apart.
func DecodeStateKey(keyB64 string) ([]byte, error) {
	if keyB64 == "" {
		return nil, errors.New("feishu: empty state key")
	}
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("feishu: state key is not valid base64: %w", err)
	}
	if len(raw) < minStateKeyBytes {
		return nil, fmt.Errorf("feishu: state key too short: %d bytes (need >=%d)", len(raw), minStateKeyBytes)
	}
	return raw, nil
}

// MustValidateStateKey checks that security.feishu_state_key is present and valid
// WITHOUT constructing a StateSigner (which would require a live Redis-backed
// NonceStore not yet wired this early in startup). Call it from the server
// startup chain so a missing/malformed key aborts the process (fail-fast),
// mirroring crypto.MustInit for the AES key — the actual StateSigner is built
// later (T7) once Redis is available.
func MustValidateStateKey(keyB64 string) {
	if _, err := DecodeStateKey(keyB64); err != nil {
		panic(fmt.Sprintf("feishu.MustValidateStateKey: %v", err))
	}
}

// NewStateSigner builds a StateSigner from a base64-encoded HMAC key and a
// NonceStore. It fails fast on an empty/malformed/too-short key or a nil store,
// so a misconfigured deploy aborts rather than silently weakening the signature.
//
// The key need not be exactly 32 bytes (HMAC accepts any length), but we reject
// keys shorter than 16 bytes as too weak for a security boundary.
func NewStateSigner(keyB64 string, store NonceStore) (*StateSigner, error) {
	if store == nil {
		return nil, errors.New("feishu: nil nonce store")
	}
	raw, err := DecodeStateKey(keyB64)
	if err != nil {
		return nil, err
	}
	return &StateSigner{key: raw, store: store}, nil
}

// Sign produces a state token for payload, valid for `validity`. It generates a
// random nonce and absolute exp, records the nonce in the store (TTL=validity so
// the nonce auto-expires alongside the token), then returns
// base64url(payloadJSON || HMAC).
//
// Caller-supplied Nonce/Exp on payload are ignored (always overwritten).
func (s *StateSigner) Sign(ctx context.Context, payload Payload, validity time.Duration) (string, error) {
	nonce, err := randNonce()
	if err != nil {
		return "", fmt.Errorf("feishu: generate nonce: %w", err)
	}
	payload.Nonce = nonce
	payload.Exp = time.Now().Add(validity).Unix()

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("feishu: marshal payload: %w", err)
	}

	// Record the nonce BEFORE returning the state. If validity is non-positive
	// (e.g. test for the expired path), use a tiny positive TTL so Put is still a
	// valid Redis call; Verify will reject on exp anyway.
	ttl := validity
	if ttl <= 0 {
		ttl = time.Second
	}
	if err := s.store.Put(ctx, nonce, ttl); err != nil {
		return "", fmt.Errorf("feishu: record nonce: %w", err)
	}

	mac := s.sign(body)
	blob := append(body, mac...)
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

// Verify parses and authenticates a state token, returning the carried payload.
// Order matters: HMAC (constant-time) → exp → nonce one-time consume. Any
// failure yields errno.ErrLarkStateInvalid (wrapped with a non-sensitive
// reason) so the callback can uniformly 302 to ?feishu=error. It never leaks the
// HMAC key or the raw blob in errors.
func (s *StateSigner) Verify(ctx context.Context, state string) (*Payload, error) {
	blob, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed state encoding", errno.ErrLarkStateInvalid)
	}
	if len(blob) <= macLen {
		return nil, fmt.Errorf("%w: state too short", errno.ErrLarkStateInvalid)
	}
	body, mac := blob[:len(blob)-macLen], blob[len(blob)-macLen:]

	// Constant-time HMAC comparison: reject any tampering before parsing JSON.
	if !hmac.Equal(mac, s.sign(body)) {
		return nil, fmt.Errorf("%w: signature mismatch", errno.ErrLarkStateInvalid)
	}

	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("%w: malformed payload", errno.ErrLarkStateInvalid)
	}

	if p.Exp <= time.Now().Unix() {
		return nil, fmt.Errorf("%w: state expired", errno.ErrLarkStateInvalid)
	}

	// One-time consume: must succeed exactly once. A store error fails closed
	// (we do NOT treat an error as "consumed") so replay protection holds even
	// when Redis is flaky.
	ok, err := s.store.Consume(ctx, p.Nonce)
	if err != nil {
		return nil, fmt.Errorf("%w: nonce check failed: %v", errno.ErrLarkStateInvalid, err)
	}
	if !ok {
		return nil, fmt.Errorf("%w: state already used (replay)", errno.ErrLarkStateInvalid)
	}

	return &p, nil
}

// sign computes HMAC-SHA256(body, key).
func (s *StateSigner) sign(body []byte) []byte {
	h := hmac.New(sha256.New, s.key)
	h.Write(body)
	return h.Sum(nil)
}

// randNonce returns a base64url-encoded 128-bit random nonce.
func randNonce() (string, error) {
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// --- Redis-backed NonceStore -------------------------------------------------

// nonceKeyPrefix namespaces state nonces in Redis (design.md: feishu:state:<nonce>).
const nonceKeyPrefix = "feishu:state:"

// RedisNonceStore is the production NonceStore over a go-redis client. Put uses
// SETNX (refuse to clobber an existing nonce); Consume uses GETDEL for an atomic
// read-and-delete (the single round-trip that guarantees a nonce is honored at
// most once across instances).
type RedisNonceStore struct {
	rdb *redis.Client
}

// NewRedisNonceStore wraps a go-redis client. Returns an error on a nil client
// rather than producing a store that silently no-ops nonce checks (which would
// disable replay protection).
func NewRedisNonceStore(rdb *redis.Client) (*RedisNonceStore, error) {
	if rdb == nil {
		return nil, errors.New("feishu: nil redis client for nonce store")
	}
	return &RedisNonceStore{rdb: rdb}, nil
}

// Put records the nonce with the given TTL. SETNX guards against an
// (astronomically unlikely) nonce collision overwriting a live entry.
func (r *RedisNonceStore) Put(ctx context.Context, nonce string, ttl time.Duration) error {
	ok, err := r.rdb.SetNX(ctx, nonceKeyPrefix+nonce, "1", ttl).Result()
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("feishu: nonce already exists (collision)")
	}
	return nil
}

// Consume atomically reads-and-deletes the nonce. A redis.Nil result means the
// nonce was never present or already consumed → ok=false (replay/unknown).
func (r *RedisNonceStore) Consume(ctx context.Context, nonce string) (bool, error) {
	err := r.rdb.GetDel(ctx, nonceKeyPrefix+nonce).Err()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// compile-time guard: RedisNonceStore satisfies NonceStore.
var _ NonceStore = (*RedisNonceStore)(nil)
