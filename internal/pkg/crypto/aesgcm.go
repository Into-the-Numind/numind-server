// Package crypto provides authenticated symmetric encryption (AES-256-GCM)
// for third-party credentials (e.g. 飞书 user_access_token / app_secret) that
// must never be stored in plaintext.
//
// Wire format of a ciphertext blob: nonce || gcm_sealed_ciphertext_with_tag.
// The nonce is randomly generated per Encrypt call and prepended, so callers
// store a single opaque []byte and pass it straight back to Decrypt.
//
// Key management: the 32-byte key is injected via config
// (security.thirdparty_token_key, 32 bytes base64). It must NOT be hardcoded
// nor committed to config_prod.yaml — ops injects it at deploy time. MustInit
// enforces fail-fast: a missing or malformed key aborts process startup so we
// never silently fall back to plaintext.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// KeyLen is the required raw key length for AES-256 (32 bytes).
const KeyLen = 32

// ErrNotInitialized is returned by the package-level Encrypt/Decrypt helpers
// when MustInit has not wired a default cipher yet.
var ErrNotInitialized = errors.New("crypto: default cipher not initialized (MustInit not called)")

// Cipher is an AES-256-GCM authenticated cipher bound to a single key.
// It is safe for concurrent use: cipher.AEAD seal/open are stateless and the
// nonce is generated fresh per call.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from a base64-encoded 32-byte key. It returns an
// error if the key is empty, not valid base64, or not exactly 32 bytes — never
// silently weakening the cipher.
func NewCipher(keyB64 string) (*Cipher, error) {
	if keyB64 == "" {
		return nil, errors.New("crypto: empty key")
	}
	raw, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("crypto: key is not valid base64: %w", err)
	}
	if len(raw) != KeyLen {
		return nil, fmt.Errorf("crypto: key must be %d bytes after base64 decode, got %d", KeyLen, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("crypto: aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: cipher.NewGCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext with a fresh random nonce and returns
// nonce || ciphertext(+tag). A nil/empty plaintext is valid (yields a blob that
// decrypts back to an empty slice).
func (c *Cipher) Encrypt(plain []byte) ([]byte, error) {
	return c.EncryptWithAAD(plain, nil)
}

// EncryptWithAAD seals plaintext and authenticates aad without including it in
// the ciphertext. Decryption must supply the exact same aad byte sequence.
func (c *Cipher) EncryptWithAAD(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: read nonce: %w", err)
	}
	// Seal appends the ciphertext to its first arg (the nonce), giving the
	// nonce-prefixed wire format in a single allocation.
	return c.aead.Seal(nonce, nonce, plain, aad), nil
}

// Decrypt parses nonce || ciphertext and returns the authenticated plaintext.
// It errors (never panics) on blobs shorter than the nonce, on a wrong key, or
// on any tampering (GCM auth-tag mismatch).
func (c *Cipher) Decrypt(blob []byte) ([]byte, error) {
	return c.DecryptWithAAD(blob, nil)
}

// DecryptWithAAD opens a blob only when its authentication tag and the exact
// aad supplied at encryption time both verify.
func (c *Cipher) DecryptWithAAD(blob, aad []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(blob) < ns {
		return nil, fmt.Errorf("crypto: ciphertext too short: %d < nonce size %d", len(blob), ns)
	}
	nonce, ct := blob[:ns], blob[ns:]
	plain, err := c.aead.Open(nil, nonce, ct, aad)
	if err != nil {
		// Do NOT include the blob contents in the error (avoid leaking ciphertext).
		return nil, fmt.Errorf("crypto: decrypt failed (wrong key or tampered): %w", err)
	}
	return plain, nil
}

// defaultCipher is the process-wide cipher wired by MustInit, used by the
// package-level Encrypt/Decrypt convenience helpers.
var defaultCipher *Cipher

// MustInit builds the package-level default Cipher from a base64-encoded 32-byte
// key and panics if the key is missing or malformed. Call it from the server
// startup chain so a bad/absent key aborts the process (fail-fast) rather than
// letting credentials be stored in plaintext.
func MustInit(keyB64 string) {
	c, err := NewCipher(keyB64)
	if err != nil {
		panic(fmt.Sprintf("crypto.MustInit: %v", err))
	}
	defaultCipher = c
}

// Encrypt seals plaintext using the package default cipher. Returns
// ErrNotInitialized if MustInit has not been called.
func Encrypt(plain []byte) ([]byte, error) {
	if defaultCipher == nil {
		return nil, ErrNotInitialized
	}
	return defaultCipher.Encrypt(plain)
}

// Decrypt opens a blob using the package default cipher. Returns
// ErrNotInitialized if MustInit has not been called.
func Decrypt(blob []byte) ([]byte, error) {
	if defaultCipher == nil {
		return nil, ErrNotInitialized
	}
	return defaultCipher.Decrypt(blob)
}
