package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newKeyB64 returns a fresh random 32-byte key encoded as standard base64.
func newKeyB64(t *testing.T) string {
	t.Helper()
	raw := make([]byte, KeyLen)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}

func TestNewCipher_RoundTrip(t *testing.T) {
	c, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)

	cases := [][]byte{
		nil,
		{},
		[]byte("user_access_token_abc123"),
		[]byte("飞书 app secret 含中文与 emoji 🚀"),
		bytes.Repeat([]byte("x"), 4096),
	}
	for _, plain := range cases {
		ct, err := c.Encrypt(plain)
		require.NoError(t, err)

		got, err := c.Decrypt(ct)
		require.NoError(t, err)

		// Encrypt(nil) and Encrypt([]byte{}) both decrypt back to an empty slice.
		if len(plain) == 0 {
			assert.Len(t, got, 0)
		} else {
			assert.Equal(t, plain, got)
		}
	}
}

func TestCipherAADRejectsDifferentBinding(t *testing.T) {
	c, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)

	plain := []byte("encrypted lark-cli home")
	originalAAD := []byte("lark|7|1|v1")
	sealed, err := c.EncryptWithAAD(plain, originalAAD)
	require.NoError(t, err)

	got, err := c.DecryptWithAAD(sealed, originalAAD)
	require.NoError(t, err)
	require.Equal(t, plain, got)

	for name, aad := range map[string][]byte{
		"different user":        []byte("lark|8|1|v1"),
		"different generation":  []byte("lark|7|2|v1"),
		"different key version": []byte("lark|7|1|v2"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.DecryptWithAAD(sealed, aad)
			require.Error(t, err)
		})
	}
}

func TestCipherAADLegacyEncryptDecryptCompatibility(t *testing.T) {
	c, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)

	legacyCiphertext, err := c.Encrypt([]byte("legacy payload"))
	require.NoError(t, err)
	got, err := c.DecryptWithAAD(legacyCiphertext, nil)
	require.NoError(t, err)
	require.Equal(t, []byte("legacy payload"), got)

	newCiphertext, err := c.EncryptWithAAD([]byte("new payload without AAD"), nil)
	require.NoError(t, err)
	got, err = c.Decrypt(newCiphertext)
	require.NoError(t, err)
	require.Equal(t, []byte("new payload without AAD"), got)
}

// TestEncrypt_NonceUnique ensures every Encrypt call uses a fresh random nonce,
// so encrypting identical plaintext twice yields distinct ciphertext.
func TestEncrypt_NonceUnique(t *testing.T) {
	c, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)

	plain := []byte("same plaintext")
	a, err := c.Encrypt(plain)
	require.NoError(t, err)
	b, err := c.Encrypt(plain)
	require.NoError(t, err)

	assert.NotEqual(t, a, b, "ciphertext must differ due to random nonce")

	// Both still decrypt to the same plaintext.
	gotA, err := c.Decrypt(a)
	require.NoError(t, err)
	gotB, err := c.Decrypt(b)
	require.NoError(t, err)
	assert.Equal(t, plain, gotA)
	assert.Equal(t, plain, gotB)
}

// TestDecrypt_WrongKey ensures a ciphertext produced with one key cannot be
// decrypted with a different key (GCM auth tag mismatch -> error).
func TestDecrypt_WrongKey(t *testing.T) {
	enc, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)
	dec, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)

	ct, err := enc.Encrypt([]byte("secret"))
	require.NoError(t, err)

	_, err = dec.Decrypt(ct)
	assert.Error(t, err, "decrypting with the wrong key must fail")
}

// TestDecrypt_Tampered ensures any mutation of the ciphertext (incl. nonce) is
// rejected by the GCM authentication tag.
func TestDecrypt_Tampered(t *testing.T) {
	c, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)

	ct, err := c.Encrypt([]byte("integrity matters"))
	require.NoError(t, err)
	require.Greater(t, len(ct), 0)

	tampered := append([]byte(nil), ct...)
	tampered[len(tampered)-1] ^= 0xFF // flip last byte of the auth tag/ciphertext

	_, err = c.Decrypt(tampered)
	assert.Error(t, err, "tampered ciphertext must fail authentication")
}

// TestDecrypt_TooShort ensures ciphertext shorter than the nonce is rejected
// without panicking.
func TestDecrypt_TooShort(t *testing.T) {
	c, err := NewCipher(newKeyB64(t))
	require.NoError(t, err)

	_, err = c.Decrypt([]byte{0x00, 0x01})
	assert.Error(t, err, "ciphertext shorter than nonce must error, not panic")
}

func TestNewCipher_InvalidKey(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		_, err := NewCipher("")
		assert.Error(t, err)
	})
	t.Run("not base64", func(t *testing.T) {
		_, err := NewCipher("!!!not base64!!!")
		assert.Error(t, err)
	})
	t.Run("wrong length", func(t *testing.T) {
		short := base64.StdEncoding.EncodeToString(make([]byte, 16)) // 16 bytes != 32
		_, err := NewCipher(short)
		assert.Error(t, err)
	})
}

// TestMustInit_FailFast verifies MustInit panics on a missing/invalid key
// (the startup chain converts a panic/fatal into process abort = fail-fast),
// and succeeds with a valid key, wiring the package-level default cipher.
func TestMustInit_FailFast(t *testing.T) {
	// Reset the package default after this test so other tests/state are clean.
	t.Cleanup(func() { defaultCipher = nil })

	t.Run("empty key panics", func(t *testing.T) {
		assert.Panics(t, func() { MustInit("") })
	})
	t.Run("wrong length panics", func(t *testing.T) {
		bad := base64.StdEncoding.EncodeToString(make([]byte, 10))
		assert.Panics(t, func() { MustInit(bad) })
	})
	t.Run("valid key wires default cipher", func(t *testing.T) {
		key := newKeyB64(t)
		assert.NotPanics(t, func() { MustInit(key) })

		ct, err := Encrypt([]byte("hello"))
		require.NoError(t, err)
		got, err := Decrypt(ct)
		require.NoError(t, err)
		assert.Equal(t, []byte("hello"), got)
	})
}

// TestPackageFuncs_NotInitialized verifies that calling the package-level
// Encrypt/Decrypt before MustInit returns an error (not a nil-pointer panic).
func TestPackageFuncs_NotInitialized(t *testing.T) {
	prev := defaultCipher
	defaultCipher = nil
	t.Cleanup(func() { defaultCipher = prev })

	_, err := Encrypt([]byte("x"))
	assert.ErrorIs(t, err, ErrNotInitialized)
	_, err = Decrypt([]byte("x"))
	assert.ErrorIs(t, err, ErrNotInitialized)
}
