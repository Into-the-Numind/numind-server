package main

import (
	"testing"
)

func TestUnicodeValidator(t *testing.T) {
	v := NewUnicodeValidator()
	if v.ID() != "Unicode" {
		t.Fatalf("expected ID Unicode, got %s", v.ID())
	}

	// Construct attack strings using explicit rune literals to avoid file encoding issues.
	rtlOverride := "ls" + string(rune(0x202E)) + ".exe"     // U+202E RIGHT-TO-LEFT OVERRIDE
	nbspCmd := "cat" + string(rune(0x00A0)) + "/etc/passwd" // U+00A0 NO-BREAK SPACE
	zwSpace := "r" + string(rune(0x200B)) + "m /"           // U+200B ZERO WIDTH SPACE
	zwNonJoiner := "r" + string(rune(0x200C)) + "m /"       // U+200C ZERO WIDTH NON-JOINER
	zwJoiner := "r" + string(rune(0x200D)) + "m /"          // U+200D ZERO WIDTH JOINER
	bomCmd := string(rune(0xFEFF)) + "rm /"                 // U+FEFF BOM

	tests := []struct {
		name     string
		cmd      string
		wantDeny bool
	}{
		// Happy paths
		{name: "normal ASCII", cmd: "ls -la /home", wantDeny: false},
		{name: "regular Chinese", cmd: "echo \xe4\xbd\xa0\xe5\xa5\xbd", wantDeny: false},
		{name: "standard space", cmd: "echo hello world", wantDeny: false},

		// Attack cases — using rune-constructed strings
		{name: "RTL override U+202E", cmd: rtlOverride, wantDeny: true},
		{name: "NBSP U+00A0", cmd: nbspCmd, wantDeny: true},
		{name: "zero-width space U+200B", cmd: zwSpace, wantDeny: true},
		{name: "zero-width non-joiner U+200C", cmd: zwNonJoiner, wantDeny: true},
		{name: "zero-width joiner U+200D", cmd: zwJoiner, wantDeny: true},
		{name: "BOM U+FEFF", cmd: bomCmd, wantDeny: true},
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
			if result.Decision == Deny && result.ValidatorID != "Unicode" {
				t.Errorf("expected ValidatorID Unicode, got %s", result.ValidatorID)
			}
		})
	}
}
