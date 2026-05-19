package main

import (
	"testing"
)

func TestCarriageReturnValidator(t *testing.T) {
	v := NewCarriageReturnValidator()
	if v.ID() != "CR" {
		t.Fatalf("expected ID CR, got %s", v.ID())
	}

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths
		{name: "normal command", cmd: "echo hello", wantDeny: false},
		{name: "CRLF sequence allowed", cmd: "echo hi\r\n", wantDeny: false},
		{name: "no CR at all", cmd: "ls -la", wantDeny: false},
		{name: "newline only", cmd: "echo\nls", wantDeny: false},

		// Attack cases
		{name: "bare CR followed by cmd", cmd: "echo hi\rrm /", wantDeny: true},
		{name: "CR at end of string", cmd: "echo hi\r", wantDeny: true},
		{name: "CR followed by space", cmd: "echo\r ls", wantDeny: true},
		{name: "multiple bare CRs", cmd: "ls\recho\rcat", wantDeny: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := v.Validate(tt.cmd)
			if tt.wantDeny && result.Decision != Deny {
				t.Errorf("expected Deny but got Allow for %q", tt.cmd)
			}
			if !tt.wantDeny && result.Decision != Allow {
				t.Errorf("expected Allow but got Deny (validator=%s, reason=%s) for %q",
					result.ValidatorID, result.Reason, tt.cmd)
			}
			if result.Decision == Deny && result.ValidatorID != "CR" {
				t.Errorf("expected ValidatorID CR, got %s", result.ValidatorID)
			}
		})
	}
}
