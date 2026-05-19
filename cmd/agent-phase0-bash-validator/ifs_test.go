package main

import (
	"testing"
)

func TestIFSValidator(t *testing.T) {
	v := NewIFSValidator()
	if v.ID() != "IFS" {
		t.Fatalf("expected ID IFS, got %s", v.ID())
	}

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths
		{name: "normal command", cmd: "ls -la", wantDeny: false},
		{name: "echo HOME", cmd: "echo $HOME", wantDeny: false},
		{name: "regular braces", cmd: "echo ${HOME}", wantDeny: false},
		{name: "PATH variable", cmd: "echo $PATH", wantDeny: false},

		// Attack cases
		{name: "bare $IFS", cmd: "cat${IFS}/etc/passwd", wantDeny: true},
		{name: "$IFS standalone", cmd: "echo $IFS", wantDeny: true},
		{name: "${IFS} braced", cmd: "ls${IFS}-la", wantDeny: true},
		{name: "ANSI-C tab quoting", cmd: "$'\\trm'", wantDeny: true},
		{name: "ANSI-C newline quoting", cmd: "echo $'\\n'", wantDeny: true},
		{name: "ANSI-C space quoting", cmd: "cat$' '/etc/passwd", wantDeny: true},
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
			if result.Decision == Deny && result.ValidatorID != "IFS" {
				t.Errorf("expected ValidatorID IFS, got %s", result.ValidatorID)
			}
		})
	}
}
