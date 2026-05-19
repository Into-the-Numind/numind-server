package main

import (
	"testing"
)

func TestControlCharValidator(t *testing.T) {
	v := NewControlCharValidator()
	if v.ID() != "ControlChar" {
		t.Fatalf("expected ID ControlChar, got %s", v.ID())
	}

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths — should Allow
		{name: "normal command", cmd: "ls -la /home", wantDeny: false},
		{name: "tab allowed", cmd: "echo\there", wantDeny: false},
		{name: "newline allowed", cmd: "echo line1\necho line2", wantDeny: false},
		{name: "CR LF allowed", cmd: "echo hi\r\n", wantDeny: false},
		{name: "printable ASCII", cmd: "cat file.txt | grep foo", wantDeny: false},
		{name: "empty string", cmd: "", wantDeny: false},

		// Attack cases — should Deny
		{name: "null byte", cmd: "cmd\x00rm -rf /", wantDeny: true},
		{name: "bell char 0x07", cmd: "\x07exec", wantDeny: true},
		{name: "0x01 SOH", cmd: "\x01ls", wantDeny: true},
		{name: "0x1F US", cmd: "ls\x1f/etc", wantDeny: true},
		{name: "DEL 0x7F", cmd: "ls\x7f/etc", wantDeny: true},
		{name: "form feed", cmd: "echo\x0c", wantDeny: true},
		{name: "vertical tab", cmd: "echo\x0b", wantDeny: true},
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
			if result.Decision == Deny && result.ValidatorID != "ControlChar" {
				t.Errorf("expected ValidatorID ControlChar, got %s", result.ValidatorID)
			}
		})
	}
}
