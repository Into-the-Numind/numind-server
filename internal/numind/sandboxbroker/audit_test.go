package sandboxbroker

import "testing"

func TestAuditRedactsSensitiveValues(t *testing.T) {
	event := SanitizeAuditEvent(AuditEvent{
		RequestID:         "request-1",
		LeaseID:           "lease-1",
		OwnerID:           "api-blue",
		AgentRunID:        7,
		SandboxSessionID:  8,
		StateFrom:         LeaseActive,
		StateTo:           LeaseOutputPersisting,
		WaitMS:            123,
		TerminationReason: TerminationCompleted,
		PressureState:     "nominal",
		Fields: map[string]any{
			"env":            "OPENAI_API_KEY=sk-secret",
			"prompt":         "customer private prompt",
			"stdout":         "customer file body",
			"safe_component": "sandboxd",
			"authorization":  "Bearer token-secret",
			"argv_sanitized": []string{"/bin/true", "token=secret"},
		},
	})

	for _, key := range []string{
		"env",
		"prompt",
		"stdout",
		"authorization",
	} {
		if event.Fields[key] != redactedAuditValue {
			t.Fatalf("%s was not redacted: %#v", key, event.Fields[key])
		}
	}
	if event.Fields["safe_component"] != "sandboxd" {
		t.Fatalf("safe field changed: %#v", event.Fields["safe_component"])
	}
	argv := event.Fields["argv_sanitized"].([]string)
	if argv[0] != "/bin/true" || argv[1] != redactedAuditValue {
		t.Fatalf("argv redaction = %#v", argv)
	}
}

func TestAuditTruncatesLongStrings(t *testing.T) {
	value := make([]byte, maxAuditStringLen+20)
	for index := range value {
		value[index] = 'a'
	}
	redacted := RedactAuditValue("safe", string(value)).(string)
	if len(redacted) != maxAuditStringLen {
		t.Fatalf("redacted length = %d", len(redacted))
	}
}
