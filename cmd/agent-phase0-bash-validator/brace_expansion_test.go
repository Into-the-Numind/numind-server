package main

import (
	"testing"
)

func TestBraceExpansionValidator(t *testing.T) {
	v := NewBraceExpansionValidator()
	if v.ID() != "BraceExpansion" {
		t.Fatalf("expected ID BraceExpansion, got %s", v.ID())
	}

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths
		{name: "normal command", cmd: "ls -la /home", wantDeny: false},
		{name: "no braces", cmd: "echo $HOME", wantDeny: false},
		{name: "single brace no expansion", cmd: "echo {}", wantDeny: false},
		{name: "cat file", cmd: "cat file.txt | grep foo", wantDeny: false},
		{name: "single item brace", cmd: "echo {a}", wantDeny: false},
		{name: "empty braces in path", cmd: "ls /etc/ssh/", wantDeny: false},

		// Attack cases
		{name: "range expansion az", cmd: "rm /{a..z}/{1..1000}", wantDeny: true},
		{name: "range expansion numeric", cmd: "echo {1..100}", wantDeny: true},
		{name: "range expansion char", cmd: "echo {a..z}", wantDeny: true},
		{name: "comma list", cmd: "echo {a,b,c}", wantDeny: true},
		{name: "nested brace expansion", cmd: "echo {a,{b,{c,d}}}", wantDeny: true},
		{name: "path with comma brace", cmd: "cat /etc/{passwd,shadow}", wantDeny: true},
		{name: "three level nesting", cmd: "echo {{a,b},{c,d}}", wantDeny: true},
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
			if result.Decision == Deny && result.ValidatorID != "BraceExpansion" {
				t.Errorf("expected ValidatorID BraceExpansion, got %s", result.ValidatorID)
			}
		})
	}
}

func TestHasNestedBraces(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"echo {a,b}", false},
		{"echo {{a,b}}", true},
		{"echo {a,{b,c}}", true},
		{"ls /home", false},
		{"echo {a..z}", false},
		{"echo {a,{b,{c}}}", true},
	}
	for _, tt := range tests {
		got := hasNestedBraces(tt.cmd)
		if got != tt.want {
			t.Errorf("hasNestedBraces(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}
