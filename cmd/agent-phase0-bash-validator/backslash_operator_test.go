package main

import (
	"testing"
)

func TestBackslashOperatorValidator(t *testing.T) {
	v := NewBackslashOperatorValidator()
	if v.ID() != "BackslashOperator" {
		t.Fatalf("expected ID BackslashOperator, got %s", v.ID())
	}

	// Build attack strings explicitly to avoid editor/compiler swallowing backslash sequences.
	// The validator checks for literal \x, \u, \0 sequences in echo -e or printf args.
	bsX := string([]byte{0x5C, 'x'})       // literal backslash + x
	bsU := string([]byte{0x5C, 'u'})       // literal backslash + u
	bsOct := string([]byte{0x5C, '0'})     // literal backslash + 0 (octal)
	hexArg := bsX + "72" + bsX + "6d"      // \x72\x6d  (rm)
	uArg := bsU + "0072" + bsU + "006d"    // rm (rm)
	octArg := bsOct + "72" + bsOct + "155" // \072\155

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths — backslash escapes NOT in echo -e / printf context
		{name: "plain echo", cmd: "echo hello", wantDeny: false},
		{name: "echo without -e", cmd: "echo '" + bsX + "72'", wantDeny: false},
		{name: "ls command", cmd: "ls -la /home", wantDeny: false},
		{name: "cat command", cmd: "cat file.txt | grep foo", wantDeny: false},
		{name: "python hello", cmd: `python -c print("hello")`, wantDeny: false},
		{name: "head command", cmd: "head -n 5 /etc/hostname", wantDeny: false},

		// Attack cases — backslash escapes inside echo -e or printf
		{name: "echo -e hex escape", cmd: "echo -e '" + hexArg + "'", wantDeny: true},
		{name: "printf hex escape", cmd: "printf '" + hexArg + " /'", wantDeny: true},
		{name: "echo -e unicode escape", cmd: "echo -e '" + uArg + "'", wantDeny: true},
		{name: "printf octal escape", cmd: "printf '" + octArg + "'", wantDeny: true},
		{name: "echo -ne hex", cmd: "echo -ne '" + bsX + "41" + bsX + "42'", wantDeny: true},
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
			if result.Decision == Deny && result.ValidatorID != "BackslashOperator" {
				t.Errorf("expected ValidatorID BackslashOperator, got %s", result.ValidatorID)
			}
		})
	}
}
