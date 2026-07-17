package feishu

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	pkgcrypto "numind-server/internal/pkg/crypto"
)

const (
	deviceAuthCredentialPurpose             = "feishu-auth-resume/v1"
	deviceAuthCredentialEnvelopeVersion     = uint8(1)
	deviceAuthCredentialMaxDeviceCodeBytes  = 4 << 10
	deviceAuthCredentialMaxEnvelopeBytes    = 8 << 10
	deviceAuthCredentialMaxCiphertextBytes  = deviceAuthCredentialMaxEnvelopeBytes + 128
	deviceAuthCredentialMaxAppIDBytes       = 64
	deviceAuthCredentialMaxOperationIDBytes = 64
	deviceAuthCredentialMaxSessionIDBytes   = 36
)

var errDeviceAuthCredentialRejected = errors.New("feishu device auth credential rejected")

// DeviceAuthCredentialBinding binds one resume credential to its exact owner,
// authorization session, requested scopes, and expiry.
type DeviceAuthCredentialBinding struct {
	UserID          uint
	Generation      uint64
	AppID           string
	OperationID     string
	SessionID       string
	ScopeHash       string
	ResumeExpiresAt time.Time
}

type deviceAuthCredentialEnvelope struct {
	Version    uint8  `json:"version"`
	DeviceCode string `json:"device_code"`
}

// DeviceAuthCredentialCipher encrypts device authorization credentials with
// the active key and reads only explicitly configured historical key versions.
type DeviceAuthCredentialCipher struct {
	ciphers    map[string]*pkgcrypto.Cipher
	keyVersion string
}

// NewDeviceAuthCredentialCipher constructs a frozen credential cipher keyring.
func NewDeviceAuthCredentialCipher(
	ciphers map[string]*pkgcrypto.Cipher,
	keyVersion string,
) (*DeviceAuthCredentialCipher, error) {
	if len(ciphers) == 0 || validateCLIHomeKeyVersion(keyVersion) != nil {
		return nil, errDeviceAuthCredentialRejected
	}
	frozen := make(map[string]*pkgcrypto.Cipher, len(ciphers))
	for version, cipher := range ciphers {
		if validateCLIHomeKeyVersion(version) != nil || cipher == nil {
			return nil, errDeviceAuthCredentialRejected
		}
		frozen[version] = cipher
	}
	if frozen[keyVersion] == nil {
		return nil, errDeviceAuthCredentialRejected
	}
	return &DeviceAuthCredentialCipher{ciphers: frozen, keyVersion: keyVersion}, nil
}

// Seal encrypts a device code with the active key and exact binding AAD.
func (c *DeviceAuthCredentialCipher) Seal(
	binding DeviceAuthCredentialBinding,
	deviceCode string,
) ([]byte, string, error) {
	if c == nil || c.ciphers[c.keyVersion] == nil ||
		!validDeviceAuthCredentialBinding(binding) || !validDeviceAuthDeviceCode(deviceCode) {
		return nil, "", errDeviceAuthCredentialRejected
	}
	envelope, err := json.Marshal(deviceAuthCredentialEnvelope{
		Version: deviceAuthCredentialEnvelopeVersion, DeviceCode: deviceCode,
	})
	if err != nil || len(envelope) > deviceAuthCredentialMaxEnvelopeBytes {
		return nil, "", errDeviceAuthCredentialRejected
	}
	ciphertext, err := c.ciphers[c.keyVersion].EncryptWithAAD(
		envelope, deviceAuthCredentialAAD(binding, c.keyVersion),
	)
	if err != nil || len(ciphertext) > deviceAuthCredentialMaxCiphertextBytes {
		return nil, "", errDeviceAuthCredentialRejected
	}
	return ciphertext, c.keyVersion, nil
}

// Open decrypts a device code only with the exact binding and recorded key version.
func (c *DeviceAuthCredentialCipher) Open(
	binding DeviceAuthCredentialBinding,
	keyVersion string,
	ciphertext []byte,
) (string, error) {
	if c == nil || !validDeviceAuthCredentialBinding(binding) ||
		validateCLIHomeKeyVersion(keyVersion) != nil || len(ciphertext) == 0 ||
		len(ciphertext) > deviceAuthCredentialMaxCiphertextBytes {
		return "", errDeviceAuthCredentialRejected
	}
	cipher, ok := c.ciphers[keyVersion]
	if !ok || cipher == nil {
		return "", errDeviceAuthCredentialRejected
	}
	plaintext, err := cipher.DecryptWithAAD(
		ciphertext, deviceAuthCredentialAAD(binding, keyVersion),
	)
	if err != nil || len(plaintext) == 0 || len(plaintext) > deviceAuthCredentialMaxEnvelopeBytes {
		return "", errDeviceAuthCredentialRejected
	}

	var envelope deviceAuthCredentialEnvelope
	if json.Unmarshal(plaintext, &envelope) != nil ||
		envelope.Version != deviceAuthCredentialEnvelopeVersion ||
		!validDeviceAuthDeviceCode(envelope.DeviceCode) {
		return "", errDeviceAuthCredentialRejected
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(plaintext, canonical) {
		return "", errDeviceAuthCredentialRejected
	}
	return envelope.DeviceCode, nil
}

func deviceAuthCredentialAAD(binding DeviceAuthCredentialBinding, keyVersion string) []byte {
	payload := struct {
		Purpose     string `json:"purpose"`
		UserID      uint   `json:"user_id"`
		Generation  uint64 `json:"generation"`
		AppID       string `json:"app_id"`
		OperationID string `json:"operation_id"`
		SessionID   string `json:"session_id"`
		ScopeHash   string `json:"scope_hash"`
		ExpiresAt   string `json:"resume_expires_at"`
		KeyVersion  string `json:"key_version"`
	}{
		Purpose: deviceAuthCredentialPurpose, UserID: binding.UserID,
		Generation: binding.Generation, AppID: binding.AppID,
		OperationID: binding.OperationID, SessionID: binding.SessionID,
		ScopeHash:  binding.ScopeHash,
		ExpiresAt:  binding.ResumeExpiresAt.UTC().Format(time.RFC3339Nano),
		KeyVersion: keyVersion,
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}

func validDeviceAuthCredentialBinding(binding DeviceAuthCredentialBinding) bool {
	// Resume expiry is persisted as DATETIME(3) and participates in AAD. The
	// caller must canonicalize it once before both sealing and persistence.
	return binding.UserID != 0 && binding.Generation != 0 &&
		validStableIdentifier(binding.AppID, deviceAuthCredentialMaxAppIDBytes) &&
		validStableIdentifier(binding.OperationID, deviceAuthCredentialMaxOperationIDBytes) &&
		validStableIdentifier(binding.SessionID, deviceAuthCredentialMaxSessionIDBytes) &&
		validDeviceAuthScopeHash(binding.ScopeHash) && !binding.ResumeExpiresAt.IsZero() &&
		binding.ResumeExpiresAt.Nanosecond()%int(time.Millisecond) == 0
}

func validDeviceAuthScopeHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for index := range value {
		if (value[index] < '0' || value[index] > '9') && (value[index] < 'a' || value[index] > 'f') {
			return false
		}
	}
	return true
}

func validDeviceAuthDeviceCode(value string) bool {
	return value != "" && len(value) <= deviceAuthCredentialMaxDeviceCodeBytes &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
