package feishu

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	pkgcrypto "numind-server/internal/pkg/crypto"

	"github.com/stretchr/testify/require"
)

func TestDeviceAuthCredentialCipher_RoundTripBindsEveryField(t *testing.T) {
	t.Parallel()

	active := newDeviceAuthCredentialTestCipher(t, 17)
	keyring, err := NewDeviceAuthCredentialCipher(map[string]*pkgcrypto.Cipher{
		"v2":    active,
		"alias": active,
	}, "v2")
	require.NoError(t, err)

	rawExpiry := time.Date(2026, time.July, 17, 10, 11, 12, 345678900, time.FixedZone("CST", 8*60*60))
	binding := deviceAuthCredentialTestBinding()
	binding.ResumeExpiresAt = rawExpiry.UTC().Round(time.Millisecond)
	const deviceCode = "secret-device-code"
	ciphertext, keyVersion, err := keyring.Seal(binding, deviceCode)
	require.NoError(t, err)
	require.Equal(t, "v2", keyVersion)
	require.NotContains(t, string(ciphertext), deviceCode)

	opened, err := keyring.Open(binding, keyVersion, ciphertext)
	require.NoError(t, err)
	require.Equal(t, deviceCode, opened)
	persistedBinding := binding
	persistedBinding.ResumeExpiresAt = time.UnixMilli(binding.ResumeExpiresAt.UnixMilli()).UTC()
	opened, err = keyring.Open(persistedBinding, keyVersion, ciphertext)
	require.NoError(t, err)
	require.Equal(t, deviceCode, opened)
	require.Contains(
		t, string(deviceAuthCredentialAAD(binding, keyVersion)),
		`"resume_expires_at":"2026-07-17T02:11:12.346Z"`,
	)

	tests := []struct {
		name       string
		mutate     func(*DeviceAuthCredentialBinding)
		keyVersion string
	}{
		{name: "user", mutate: func(got *DeviceAuthCredentialBinding) { got.UserID++ }},
		{name: "generation", mutate: func(got *DeviceAuthCredentialBinding) { got.Generation++ }},
		{name: "app", mutate: func(got *DeviceAuthCredentialBinding) { got.AppID += "-other" }},
		{name: "manual operation", mutate: func(got *DeviceAuthCredentialBinding) { got.OperationID = "operation-123" }},
		{name: "session", mutate: func(got *DeviceAuthCredentialBinding) { got.SessionID += "-other" }},
		{name: "scope hash", mutate: func(got *DeviceAuthCredentialBinding) { got.ScopeHash = strings.Repeat("b", 64) }},
		{name: "expiry", mutate: func(got *DeviceAuthCredentialBinding) {
			got.ResumeExpiresAt = got.ResumeExpiresAt.Add(time.Millisecond)
		}},
		{name: "key version", mutate: func(*DeviceAuthCredentialBinding) {}, keyVersion: "alias"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := persistedBinding
			test.mutate(&changed)
			version := keyVersion
			if test.keyVersion != "" {
				version = test.keyVersion
			}

			got, openErr := keyring.Open(changed, version, ciphertext)
			require.Error(t, openErr)
			require.Empty(t, got)
			require.NotContains(t, openErr.Error(), deviceCode)
		})
	}
}

func TestDeviceAuthCredentialCipher_UsesPurposeSeparatedAAD(t *testing.T) {
	t.Parallel()

	cipher := newDeviceAuthCredentialTestCipher(t, 29)
	keyring, err := NewDeviceAuthCredentialCipher(
		map[string]*pkgcrypto.Cipher{"v1": cipher}, "v1",
	)
	require.NoError(t, err)
	binding := deviceAuthCredentialTestBinding()
	const deviceCode = "purpose-secret-device-code"

	wrongAAD := bytes.Replace(
		deviceAuthCredentialAAD(binding, "v1"),
		[]byte("feishu-auth-resume/v1"),
		[]byte("feishu-operation-v1"),
		1,
	)
	malformedPurpose, err := cipher.EncryptWithAAD(
		[]byte(`{"version":1,"device_code":"purpose-secret-device-code"}`), wrongAAD,
	)
	require.NoError(t, err)

	got, err := keyring.Open(binding, "v1", malformedPurpose)
	require.Error(t, err)
	require.Empty(t, got)
	require.NotContains(t, err.Error(), deviceCode)
	require.Contains(t, string(deviceAuthCredentialAAD(binding, "v1")), `"purpose":"feishu-auth-resume/v1"`)
}

func TestDeviceAuthCredentialCipher_KeyRotationWritesActiveAndReadsPrevious(t *testing.T) {
	t.Parallel()

	v1 := newDeviceAuthCredentialTestCipher(t, 41)
	v2 := newDeviceAuthCredentialTestCipher(t, 73)
	binding := deviceAuthCredentialTestBinding()

	previousWriter, err := NewDeviceAuthCredentialCipher(
		map[string]*pkgcrypto.Cipher{"v1": v1}, "v1",
	)
	require.NoError(t, err)
	oldCiphertext, oldVersion, err := previousWriter.Seal(binding, "old-device-code")
	require.NoError(t, err)
	require.Equal(t, "v1", oldVersion)

	rotated, err := NewDeviceAuthCredentialCipher(
		map[string]*pkgcrypto.Cipher{"v1": v1, "v2": v2}, "v2",
	)
	require.NoError(t, err)
	openedOld, err := rotated.Open(binding, oldVersion, oldCiphertext)
	require.NoError(t, err)
	require.Equal(t, "old-device-code", openedOld)

	newCiphertext, newVersion, err := rotated.Seal(binding, "new-device-code")
	require.NoError(t, err)
	require.Equal(t, "v2", newVersion)
	openedNew, err := rotated.Open(binding, newVersion, newCiphertext)
	require.NoError(t, err)
	require.Equal(t, "new-device-code", openedNew)

	_, err = previousWriter.Open(binding, newVersion, newCiphertext)
	require.Error(t, err)
}

func TestDeviceAuthCredentialCipher_RejectsInvalidKeyringsAndUnknownVersion(t *testing.T) {
	t.Parallel()

	cipher := newDeviceAuthCredentialTestCipher(t, 91)
	tests := []struct {
		name    string
		ciphers map[string]*pkgcrypto.Cipher
		active  string
	}{
		{name: "nil keyring", ciphers: nil, active: "v1"},
		{name: "empty keyring", ciphers: map[string]*pkgcrypto.Cipher{}, active: "v1"},
		{name: "empty active version", ciphers: map[string]*pkgcrypto.Cipher{"v1": cipher}},
		{name: "active version absent", ciphers: map[string]*pkgcrypto.Cipher{"v1": cipher}, active: "v2"},
		{name: "nil cipher", ciphers: map[string]*pkgcrypto.Cipher{"v1": nil}, active: "v1"},
		{name: "invalid key version", ciphers: map[string]*pkgcrypto.Cipher{"bad/version": cipher}, active: "bad/version"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewDeviceAuthCredentialCipher(test.ciphers, test.active)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}

	keyring, err := NewDeviceAuthCredentialCipher(
		map[string]*pkgcrypto.Cipher{"v1": cipher}, "v1",
	)
	require.NoError(t, err)
	ciphertext, _, err := keyring.Seal(deviceAuthCredentialTestBinding(), "secret-device-code")
	require.NoError(t, err)

	got, err := keyring.Open(deviceAuthCredentialTestBinding(), "retired", ciphertext)
	require.Error(t, err)
	require.Empty(t, got)
	require.NotContains(t, err.Error(), "secret-device-code")
}

func TestDeviceAuthCredentialCipher_RejectsInvalidBindingAndOversizedPlaintext(t *testing.T) {
	t.Parallel()

	cipher := newDeviceAuthCredentialTestCipher(t, 113)
	keyring, err := NewDeviceAuthCredentialCipher(
		map[string]*pkgcrypto.Cipher{"v1": cipher}, "v1",
	)
	require.NoError(t, err)

	invalid := deviceAuthCredentialTestBinding()
	invalid.OperationID = ""
	sealed, version, err := keyring.Seal(invalid, "secret-device-code")
	require.Error(t, err)
	require.Nil(t, sealed)
	require.Empty(t, version)
	require.NotContains(t, err.Error(), "secret-device-code")
	nonCanonicalExpiry := deviceAuthCredentialTestBinding()
	nonCanonicalExpiry.ResumeExpiresAt = nonCanonicalExpiry.ResumeExpiresAt.Add(time.Nanosecond)
	sealed, version, err = keyring.Seal(nonCanonicalExpiry, "noncanonical-secret-device-code")
	require.Error(t, err)
	require.Nil(t, sealed)
	require.Empty(t, version)
	require.NotContains(t, err.Error(), "noncanonical-secret-device-code")

	oversized := strings.Repeat("sensitive-", deviceAuthCredentialMaxDeviceCodeBytes/len("sensitive-")+2)
	sealed, version, err = keyring.Seal(deviceAuthCredentialTestBinding(), oversized)
	require.Error(t, err)
	require.Nil(t, sealed)
	require.Empty(t, version)
	require.NotContains(t, err.Error(), oversized)
}

func TestDeviceAuthCredentialCipher_RejectsMalformedEnvelopeWithoutLeakingPlaintext(t *testing.T) {
	t.Parallel()

	cipher := newDeviceAuthCredentialTestCipher(t, 151)
	keyring, err := NewDeviceAuthCredentialCipher(
		map[string]*pkgcrypto.Cipher{"v1": cipher}, "v1",
	)
	require.NoError(t, err)
	binding := deviceAuthCredentialTestBinding()
	aad := deviceAuthCredentialAAD(binding, "v1")

	tests := []struct {
		name      string
		plaintext string
	}{
		{name: "invalid JSON", plaintext: `not-json-sensitive-marker`},
		{name: "wrong schema version", plaintext: `{"version":2,"device_code":"sensitive-marker"}`},
		{name: "missing device code", plaintext: `{"version":1}`},
		{name: "empty device code", plaintext: `{"version":1,"device_code":""}`},
		{name: "unknown field", plaintext: `{"version":1,"device_code":"sensitive-marker","token":"must-not-pass"}`},
		{name: "duplicate field", plaintext: `{"version":1,"device_code":"sensitive-marker","device_code":"other"}`},
		{name: "trailing JSON", plaintext: `{"version":1,"device_code":"sensitive-marker"}{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ciphertext, encryptErr := cipher.EncryptWithAAD([]byte(test.plaintext), aad)
			require.NoError(t, encryptErr)

			got, openErr := keyring.Open(binding, "v1", ciphertext)
			require.Error(t, openErr)
			require.Empty(t, got)
			for _, secret := range []string{"not-json-sensitive-marker", "sensitive-marker", "must-not-pass", "other"} {
				require.NotContains(t, openErr.Error(), secret)
			}
		})
	}

	oversizedEnvelope := []byte(`{"version":1,"device_code":"` +
		strings.Repeat("private", deviceAuthCredentialMaxEnvelopeBytes/len("private")+1) + `"}`)
	oversizedCiphertext, err := cipher.EncryptWithAAD(oversizedEnvelope, aad)
	require.NoError(t, err)
	got, err := keyring.Open(binding, "v1", oversizedCiphertext)
	require.Error(t, err)
	require.Empty(t, got)
	require.NotContains(t, err.Error(), "private")

	got, err = keyring.Open(binding, "v1", []byte("too-short-sensitive-marker"))
	require.Error(t, err)
	require.Empty(t, got)
	require.NotContains(t, err.Error(), "sensitive-marker")
}

func deviceAuthCredentialTestBinding() DeviceAuthCredentialBinding {
	return DeviceAuthCredentialBinding{
		UserID:          7,
		Generation:      3,
		AppID:           "cli_a1234567890",
		OperationID:     "manual",
		SessionID:       "auth-session-123",
		ScopeHash:       strings.Repeat("a", 64),
		ResumeExpiresAt: time.Date(2026, time.July, 17, 10, 11, 12, 346000000, time.FixedZone("CST", 8*60*60)),
	}
}

func newDeviceAuthCredentialTestCipher(t *testing.T, seed byte) *pkgcrypto.Cipher {
	t.Helper()
	raw := make([]byte, pkgcrypto.KeyLen)
	for index := range raw {
		raw[index] = seed + byte(index)
	}
	cipher, err := pkgcrypto.NewCipher(base64.StdEncoding.EncodeToString(raw))
	require.NoError(t, err)
	return cipher
}
