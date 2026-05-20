package bashvalidator

import (
	"strings"
	"testing"
)

// ===========================================================================
// Top-level Validate() entry tests
// ===========================================================================

func TestValidate_AllowsSafeCommands(t *testing.T) {
	safe := []string{
		"echo hello",
		"ls /tmp",
		"cat /tmp/test.txt",
		"pwd",
		"date",
		"python -c 'print(2+2)'",
		"python3 -V",
	}
	for _, cmd := range safe {
		allow, reason := Validate(cmd)
		if !allow {
			t.Errorf("Validate(%q) should allow; got reason=%q", cmd, reason)
		}
	}
}

func TestValidate_DenyReturnsReason(t *testing.T) {
	allow, reason := Validate("echo $(whoami)")
	if allow {
		t.Fatalf("Validate(echo $(whoami)) should deny")
	}
	if !strings.Contains(reason, "CommandSubstitution") {
		t.Errorf("reason should mention CommandSubstitution: %q", reason)
	}
}

// ===========================================================================
// ControlCharValidator
// ===========================================================================

func TestControlChar_AllowsPrintableASCII(t *testing.T) {
	res := NewControlCharValidator().Validate("echo hello world")
	if res.Decision == Deny {
		t.Errorf("printable ASCII should allow: %v", res)
	}
}

func TestControlChar_DeniesNullByte(t *testing.T) {
	res := NewControlCharValidator().Validate("echo hi\x00ls")
	if res.Decision != Deny {
		t.Errorf("null byte should deny")
	}
}

func TestControlChar_DeniesEsc(t *testing.T) {
	res := NewControlCharValidator().Validate("echo \x1b[31m")
	if res.Decision != Deny {
		t.Errorf("ESC byte should deny")
	}
}

func TestControlChar_AllowsTabNewlineCR(t *testing.T) {
	for _, c := range []string{"\t", "\n"} {
		res := NewControlCharValidator().Validate("echo" + c + "hi")
		if res.Decision == Deny {
			t.Errorf("tab/newline should be allowed: char=%q", c)
		}
	}
}

// ===========================================================================
// UnicodeValidator
// ===========================================================================

func TestUnicode_DeniesRTL(t *testing.T) {
	res := NewUnicodeValidator().Validate("echo " + string(rune(0x202E)) + "bad")
	if res.Decision != Deny {
		t.Errorf("RTL override should deny")
	}
}

func TestUnicode_DeniesZWJ(t *testing.T) {
	res := NewUnicodeValidator().Validate("ec" + string(rune(0x200D)) + "ho")
	if res.Decision != Deny {
		t.Errorf("ZWJ should deny")
	}
}

func TestUnicode_AllowsPlainASCII(t *testing.T) {
	res := NewUnicodeValidator().Validate("echo hello")
	if res.Decision == Deny {
		t.Errorf("plain ASCII should allow")
	}
}

// ===========================================================================
// CarriageReturnValidator
// ===========================================================================

func TestCR_DeniesLoneCRAtEnd(t *testing.T) {
	res := NewCarriageReturnValidator().Validate("echo hi\r")
	if res.Decision != Deny {
		t.Errorf("lone CR at end should deny")
	}
}

func TestCR_DeniesLoneCRMidString(t *testing.T) {
	res := NewCarriageReturnValidator().Validate("echo hi\rls")
	if res.Decision != Deny {
		t.Errorf("lone CR mid-string should deny")
	}
}

func TestCR_AllowsCRLF(t *testing.T) {
	res := NewCarriageReturnValidator().Validate("echo hi\r\nls")
	if res.Decision == Deny {
		t.Errorf("CRLF should be allowed")
	}
}

// ===========================================================================
// CommandSubstitutionValidator
// ===========================================================================

func TestCmdSub_DeniesDollarParen(t *testing.T) {
	res := NewCommandSubstitutionValidator().Validate("echo $(whoami)")
	if res.Decision != Deny {
		t.Errorf("$() should deny")
	}
}

func TestCmdSub_DeniesBackticks(t *testing.T) {
	res := NewCommandSubstitutionValidator().Validate("echo `whoami`")
	if res.Decision != Deny {
		t.Errorf("backticks should deny")
	}
}

func TestCmdSub_DeniesProcessSubstitution(t *testing.T) {
	for _, p := range []string{"<(ls)", ">(cat)"} {
		res := NewCommandSubstitutionValidator().Validate("diff " + p + " " + p)
		if res.Decision != Deny {
			t.Errorf("process substitution %s should deny", p)
		}
	}
}

// ===========================================================================
// IFSValidator
// ===========================================================================

func TestIFS_DeniesDollarIFS(t *testing.T) {
	res := NewIFSValidator().Validate("cat$IFS/etc/passwd")
	if res.Decision != Deny {
		t.Errorf("$IFS should deny")
	}
}

func TestIFS_DeniesBracedIFS(t *testing.T) {
	res := NewIFSValidator().Validate("cat${IFS}/etc/passwd")
	if res.Decision != Deny {
		t.Errorf("${IFS} should deny")
	}
}

func TestIFS_DeniesANSICQuoting(t *testing.T) {
	res := NewIFSValidator().Validate(`echo $'\t' foo`)
	if res.Decision != Deny {
		t.Errorf("ANSI-C quoting should deny")
	}
}

// ===========================================================================
// ProcEnvironValidator
// ===========================================================================

func TestProcEnviron_DeniesEnviron(t *testing.T) {
	res := NewProcEnvironValidator().Validate("cat /proc/1/environ")
	if res.Decision != Deny {
		t.Errorf("/proc/N/environ should deny")
	}
}

func TestProcEnviron_DeniesMaps(t *testing.T) {
	res := NewProcEnvironValidator().Validate("cat /proc/self/maps")
	if res.Decision != Deny {
		t.Errorf("/proc/self/maps should deny")
	}
}

func TestProcEnviron_AllowsProcVersion(t *testing.T) {
	res := NewProcEnvironValidator().Validate("cat /proc/version")
	if res.Decision == Deny {
		t.Errorf("/proc/version should allow")
	}
}

// ===========================================================================
// BackslashOperatorValidator
// ===========================================================================

func TestBackslash_DeniesEchoEHex(t *testing.T) {
	res := NewBackslashOperatorValidator().Validate(`echo -e "\x6c\x73"`)
	if res.Decision != Deny {
		t.Errorf("echo -e \\xNN should deny")
	}
}

func TestBackslash_DeniesPrintfHex(t *testing.T) {
	res := NewBackslashOperatorValidator().Validate(`printf "\x6c\x73"`)
	if res.Decision != Deny {
		t.Errorf("printf \\xNN should deny")
	}
}

func TestBackslash_AllowsPlainEcho(t *testing.T) {
	res := NewBackslashOperatorValidator().Validate("echo hello")
	if res.Decision == Deny {
		t.Errorf("plain echo should allow")
	}
}

// ===========================================================================
// BraceExpansionValidator
// ===========================================================================

func TestBrace_DeniesRangeExpansion(t *testing.T) {
	res := NewBraceExpansionValidator().Validate("echo {1..100}")
	if res.Decision != Deny {
		t.Errorf("{1..100} should deny")
	}
}

func TestBrace_DeniesListExpansion(t *testing.T) {
	res := NewBraceExpansionValidator().Validate("ls {a,b,c}.txt")
	if res.Decision != Deny {
		t.Errorf("{a,b,c} should deny")
	}
}

func TestBrace_DeniesNestedBraces(t *testing.T) {
	// Use raw braces without comma/range so commaRe/rangeRe don't preempt;
	// hasNestedBraces should still catch depth >= 2.
	res := NewBraceExpansionValidator().Validate("echo {{nested}}")
	if res.Decision != Deny {
		t.Errorf("nested braces should deny")
	}
}

func TestBrace_AllowsSingleBraces(t *testing.T) {
	res := NewBraceExpansionValidator().Validate("python -c 'print({1:2})'")
	// {1:2} is dict literal, not brace expansion; but our regex might
	// still hit on {1..} pattern — verify it doesn't
	if res.Decision == Deny {
		t.Errorf("python dict literal should allow; got reason=%s pattern=%s", res.Reason, res.Pattern)
	}
}

// ===========================================================================
// Cross-validator attack vectors matrix (Phase 0 V3 inheritance)
// ===========================================================================

func TestAttackVectors_AllRejected(t *testing.T) {
	// 20 attack vectors from Phase 0 V3 — all should be denied by Validate().
	vectors := []struct {
		name string
		cmd  string
	}{
		// ControlChar
		{"null-byte", "echo a\x00b"},
		{"esc-byte", "echo \x1b[31mred"},
		// Unicode
		{"rtl-override", "echo " + string(rune(0x202E)) + "bad"},
		{"nbsp", "echo a" + string(rune(0x00A0)) + "b"},
		{"zwsp", "echo a" + string(rune(0x200B)) + "b"},
		// CR
		{"trailing-cr", "echo hi\r"},
		{"midstring-cr", "echo hi\rls"},
		// CommandSubstitution
		{"dollar-paren", "$(whoami)"},
		{"backtick", "`whoami`"},
		{"proc-sub-in", "diff <(ls) <(ls -a)"},
		{"proc-sub-out", "ls | tee >(cat)"},
		// IFS
		{"dollar-ifs", "cat$IFS/etc/passwd"},
		{"braced-ifs", "cat${IFS}/etc/passwd"},
		{"ansi-c-quote", `echo $'\t' foo`},
		// ProcEnviron
		{"proc-environ", "cat /proc/1/environ"},
		{"proc-cmdline", "cat /proc/self/cmdline"},
		{"proc-maps", "cat /proc/self/maps"},
		// BackslashOperator
		{"echo-e-hex", `echo -e "\x6c\x73"`},
		// BraceExpansion
		{"brace-range", "echo {1..1000}"},
		{"brace-comma", "rm {a,b,c}.txt"},
	}

	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			allow, reason := Validate(v.cmd)
			if allow {
				t.Errorf("attack vector %q should be denied; allowed (reason=%q)", v.cmd, reason)
			}
		})
	}
}

func TestCheckCommand_FirstDenyWins(t *testing.T) {
	// Build a command that triggers both ControlChar (1st) and CommandSubstitution (4th).
	// Expect ControlChar.ID to be the returned ValidatorID.
	cmd := "echo \x00 $(whoami)"
	res := CheckCommand(cmd, AllValidators())
	if res.Decision != Deny {
		t.Fatalf("expected Deny")
	}
	if res.ValidatorID != "ControlChar" {
		t.Errorf("first-match should be ControlChar, got %s", res.ValidatorID)
	}
}
