package sandboxbroker

import (
	"fmt"
	"strings"

	"numind-server/internal/pkg/log"
)

const (
	redactedAuditValue = "[redacted]"
	maxAuditStringLen  = 256
)

// AuditEvent is a bounded structured event. It deliberately omits user file
// content, prompts, environment values, and command output.
type AuditEvent struct {
	RequestID         string
	LeaseID           string
	OwnerID           string
	AgentRunID        uint64
	SandboxSessionID  uint64
	StateFrom         LeaseState
	StateTo           LeaseState
	WaitMS            int64
	TerminationReason TerminationReason
	PressureState     string
	Fields            map[string]any
}

func logSandboxAudit(message string, event AuditEvent) {
	clean := SanitizeAuditEvent(event)
	log.Infow(
		message,
		"request_id", clean.RequestID,
		"lease_id", clean.LeaseID,
		"owner_id", clean.OwnerID,
		"agent_run_id", clean.AgentRunID,
		"sandbox_session_id", clean.SandboxSessionID,
		"state_from", clean.StateFrom,
		"state_to", clean.StateTo,
		"wait_ms", clean.WaitMS,
		"termination_reason", clean.TerminationReason,
		"pressure_state", clean.PressureState,
	)
}

// SanitizeAuditEvent returns a copy safe for structured logs.
func SanitizeAuditEvent(event AuditEvent) AuditEvent {
	clean := event
	clean.RequestID = redactAuditString("request_id", event.RequestID)
	clean.LeaseID = redactAuditString("lease_id", event.LeaseID)
	clean.OwnerID = redactAuditString("owner_id", event.OwnerID)
	clean.PressureState = redactAuditString(
		"pressure_state",
		event.PressureState,
	)
	if len(event.Fields) > 0 {
		clean.Fields = make(map[string]any, len(event.Fields))
		for key, value := range event.Fields {
			clean.Fields[key] = RedactAuditValue(key, value)
		}
	}
	return clean
}

// RedactAuditValue removes values that could contain customer content, secrets,
// file payloads, prompts, environment values, or command output.
func RedactAuditValue(key string, value any) any {
	if auditKeySensitive(key) {
		return redactedAuditValue
	}
	switch typed := value.(type) {
	case string:
		return redactAuditString(key, typed)
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactAuditString(key, item))
		}
		return out
	case fmt.Stringer:
		return redactAuditString(key, typed.String())
	default:
		return value
	}
}

func redactAuditString(key string, value string) string {
	if value == "" {
		return ""
	}
	if auditKeySensitive(key) || auditValueSensitive(value) {
		return redactedAuditValue
	}
	if len(value) <= maxAuditStringLen {
		return value
	}
	return value[:maxAuditStringLen]
}

func auditKeySensitive(key string) bool {
	normalized := strings.ToLower(key)
	for _, marker := range []string{
		"authorization",
		"command_output",
		"env",
		"file",
		"key",
		"output",
		"password",
		"prompt",
		"secret",
		"stderr",
		"stdout",
		"token",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func auditValueSensitive(value string) bool {
	normalized := strings.ToLower(value)
	for _, marker := range []string{
		"api_key=",
		"authorization:",
		"bearer ",
		"password=",
		"prompt=",
		"secret=",
		"sk-",
		"stderr=",
		"stdout=",
		"token=",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
