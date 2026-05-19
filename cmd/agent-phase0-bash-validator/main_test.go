package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRun covers the run() CLI logic with allow and deny paths.
func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "no args prints usage",
			args:       []string{},
			wantCode:   1,
			wantStderr: "usage:",
		},
		{
			name:       "allow normal command",
			args:       []string{"ls", "-la"},
			wantCode:   0,
			wantStdout: "ALLOW",
		},
		{
			name:       "deny attack command",
			args:       []string{"echo $(id)"},
			wantCode:   1,
			wantStderr: "DENY",
		},
		{
			name:       "multi-word allow",
			args:       []string{"cat", "file.txt"},
			wantCode:   0,
			wantStdout: "ALLOW",
		},
		{
			name:       "deny ControlChar",
			args:       []string{"cmd\x00rm"},
			wantCode:   1,
			wantStderr: "DENY [ControlChar]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)

			if code != tt.wantCode {
				t.Errorf("exit code: want %d got %d (stdout=%q stderr=%q)",
					tt.wantCode, code, stdout.String(), stderr.String())
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout: want %q to contain %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr: want %q to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

// TestCheckCommandDeny verifies CheckCommand returns Deny on known attacks.
func TestCheckCommandDeny(t *testing.T) {
	bsX := string([]byte{0x5C, 'x'})

	cases := []struct {
		cmd           string
		wantValidator string
	}{
		{"cmd\x00bad", "ControlChar"},
		{"ls" + string(rune(0x202E)) + ".exe", "Unicode"},
		{"echo\rrm", "CR"},
		{"echo $(id)", "CommandSubstitution"},
		{"cat${IFS}/etc/passwd", "IFS"},
		{"cat /proc/self/environ", "ProcEnviron"},
		{"echo -e '" + bsX + "41'", "BackslashOperator"},
		{"rm /{a..z}", "BraceExpansion"},
	}

	validators := AllValidators()
	for _, tc := range cases {
		result := CheckCommand(tc.cmd, validators)
		if result.Decision != Deny {
			t.Errorf("CheckCommand(%q): want Deny, got Allow", tc.cmd)
		}
		if result.ValidatorID != tc.wantValidator {
			t.Errorf("CheckCommand(%q): want validator %s, got %s", tc.cmd, tc.wantValidator, result.ValidatorID)
		}
	}
}

// TestBraceExpansionNestedFallback exercises the nested-brace depth fallback
// path in BraceExpansion.Validate that is not reachable via regex alone.
// Pattern: deeply nested braces with no commas or ranges at any level.
func TestBraceExpansionNestedFallback(t *testing.T) {
	v := NewBraceExpansionValidator()

	// "{{a}}" has depth 2, no comma, no range — triggers hasNestedBraces fallback
	result := v.Validate("echo {{a}}")
	if result.Decision != Deny {
		t.Errorf("expected Deny for nested braces without comma/range, got Allow")
	}
	if result.ValidatorID != "BraceExpansion" {
		t.Errorf("expected BraceExpansion validator, got %s", result.ValidatorID)
	}
}
