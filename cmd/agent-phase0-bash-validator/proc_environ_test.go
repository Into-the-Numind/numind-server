package main

import (
	"testing"
)

func TestProcEnvironValidator(t *testing.T) {
	v := NewProcEnvironValidator()
	if v.ID() != "ProcEnviron" {
		t.Fatalf("expected ID ProcEnviron, got %s", v.ID())
	}

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths
		{name: "normal ls", cmd: "ls /etc", wantDeny: false},
		{name: "proc stat", cmd: "cat /proc/stat", wantDeny: false},
		{name: "proc uptime", cmd: "cat /proc/uptime", wantDeny: false},
		{name: "proc meminfo", cmd: "cat /proc/meminfo", wantDeny: false},
		{name: "proc version", cmd: "cat /proc/version", wantDeny: false},
		{name: "hostname file", cmd: "head -n 5 /etc/hostname", wantDeny: false},

		// Attack cases
		{name: "proc self environ", cmd: "cat /proc/self/environ", wantDeny: true},
		{name: "proc 1 cmdline", cmd: "cat /proc/1/cmdline", wantDeny: true},
		{name: "proc 1234 maps", cmd: "cat /proc/1234/maps", wantDeny: true},
		{name: "proc 42 status", cmd: "cat /proc/42/status", wantDeny: true},
		{name: "proc self fd", cmd: "ls /proc/self/fd", wantDeny: true},
		{name: "proc pid environ", cmd: "strings /proc/12345/environ", wantDeny: true},
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
			if result.Decision == Deny && result.ValidatorID != "ProcEnviron" {
				t.Errorf("expected ValidatorID ProcEnviron, got %s", result.ValidatorID)
			}
		})
	}
}
