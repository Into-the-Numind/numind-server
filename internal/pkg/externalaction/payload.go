// Package externalaction defines the durable, restart-safe identity for waits
// owned by an external provider.
package externalaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Payload is the complete allowlist for durable external-action identity.
// Live URLs, device codes, credentials, and unreviewed future fields do not
// belong in this type and therefore cannot appear in canonical persisted JSON.
type Payload struct {
	Provider    string    `json:"provider"`
	OperationID string    `json:"operation_id"`
	SessionID   string    `json:"session_id"`
	ToolCallID  string    `json:"tool_call_id"`
	Phase       string    `json:"phase"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Parse decodes one exact JSON object. Keys are case-sensitive, allowlisted,
// and must each occur exactly once; duplicate, unknown, or trailing input is
// rejected before the payload can cross a persistence or replay boundary.
func Parse(raw []byte) (Payload, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return Payload{}, fmt.Errorf("decode external action: %w", err)
	}
	if first != json.Delim('{') {
		return Payload{}, fmt.Errorf("external action must be a JSON object")
	}

	var payload Payload
	seen := make(map[string]struct{}, 6)
	for decoder.More() {
		token, tokenErr := decoder.Token()
		if tokenErr != nil {
			return Payload{}, fmt.Errorf("decode external action key: %w", tokenErr)
		}
		key, ok := token.(string)
		if !ok {
			return Payload{}, fmt.Errorf("external action key must be a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return Payload{}, fmt.Errorf("duplicate external action field %q", key)
		}

		var value any
		switch key {
		case "provider":
			value = &payload.Provider
		case "operation_id":
			value = &payload.OperationID
		case "session_id":
			value = &payload.SessionID
		case "tool_call_id":
			value = &payload.ToolCallID
		case "phase":
			value = &payload.Phase
		case "expires_at":
			value = &payload.ExpiresAt
		default:
			return Payload{}, fmt.Errorf("unknown external action field %q", key)
		}
		seen[key] = struct{}{}
		if decodeErr := decoder.Decode(value); decodeErr != nil {
			return Payload{}, fmt.Errorf("decode external action field %q: %w", key, decodeErr)
		}
	}

	end, err := decoder.Token()
	if err != nil {
		return Payload{}, fmt.Errorf("decode external action object end: %w", err)
	}
	if end != json.Delim('}') {
		return Payload{}, fmt.Errorf("external action object is not closed")
	}
	if trailing, trailingErr := decoder.Token(); trailingErr != io.EOF {
		if trailingErr == nil {
			return Payload{}, fmt.Errorf("multiple JSON values after external action: %v", trailing)
		}
		return Payload{}, fmt.Errorf("decode trailing external action data: %w", trailingErr)
	}

	if len(seen) != 6 || payload.Provider == "" || payload.OperationID == "" ||
		payload.SessionID == "" || payload.ToolCallID == "" || payload.Phase == "" ||
		payload.ExpiresAt.IsZero() {
		return Payload{}, fmt.Errorf("provider, operation_id, session_id, tool_call_id, phase, and expires_at are required exactly once")
	}
	return payload, nil
}

// CanonicalJSON validates raw with Parse and serializes the durable DTO in one
// stable field order. Callers must persist this result, never the original raw.
func CanonicalJSON(raw []byte) ([]byte, error) {
	payload, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical external action: %w", err)
	}
	return canonical, nil
}
