package main

import (
	"testing"
)

func TestCommandSubstitutionValidator(t *testing.T) {
	v := NewCommandSubstitutionValidator()
	if v.ID() != "CommandSubstitution" {
		t.Fatalf("expected ID CommandSubstitution, got %s", v.ID())
	}

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths
		{name: "normal echo", cmd: "echo hello", wantDeny: false},
		{name: "variable expansion", cmd: "echo $HOME", wantDeny: false},
		{name: "brace variable", cmd: "echo ${HOME}", wantDeny: false},
		{name: "pipe", cmd: "cat file.txt | grep foo", wantDeny: false},
		{name: "redirect", cmd: "ls > out.txt", wantDeny: false},
		{name: "python print", cmd: `python -c print("hello")`, wantDeny: false},

		// Attack cases
		{name: "dollar paren $(id)", cmd: "echo $(id)", wantDeny: true},
		{name: "nested dollar paren", cmd: "echo $(cat $(id))", wantDeny: true},
		{name: "backtick", cmd: "echo `id`", wantDeny: true},
		{name: "process sub input", cmd: "cat <(id)", wantDeny: true},
		{name: "process sub output", cmd: "cmd >(id)", wantDeny: true},
		{name: "substitution in string", cmd: "name=$(whoami)", wantDeny: true},
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
			if result.Decision == Deny && result.ValidatorID != "CommandSubstitution" {
				t.Errorf("expected ValidatorID CommandSubstitution, got %s", result.ValidatorID)
			}
		})
	}
}
