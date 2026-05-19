package main

import (
	"testing"
)

// TestAttackMatrix validates all 20 attack vectors from ATTACK_VECTORS.md.
// Expected: 6 Allow (cases 1,16,17,18,19,20) + 14 Deny, each Deny hitting the correct validator.
func TestAttackMatrix(t *testing.T) {
	validators := AllValidators()

	// Construct strings with dangerous Unicode using explicit rune values
	// to avoid invisible characters in source that confuse editors/compilers.
	case3RTL := "ls" + string(rune(0x202E)) + ".exe" // U+202E RIGHT-TO-LEFT OVERRIDE

	cases := []struct {
		num           int
		cmd           string
		wantDecision  Decision
		wantValidator string // empty string for Allow cases
	}{
		// --- Deny cases (14 explicit attack vectors) ---
		{2, "cmd\x00rm -rf /", Deny, "ControlChar"},
		{3, case3RTL, Deny, "Unicode"},
		{4, "echo hi\rrm /", Deny, "CR"},
		{5, "echo $(id)", Deny, "CommandSubstitution"},
		{6, "echo `id`", Deny, "CommandSubstitution"},
		{7, "cat <(id)", Deny, "CommandSubstitution"},
		{8, "cat${IFS}/etc/passwd", Deny, "IFS"},
		{9, "$'\\trm'", Deny, "IFS"},
		{10, "cat /proc/self/environ", Deny, "ProcEnviron"},
		{11, "cat /proc/1/cmdline", Deny, "ProcEnviron"},
		{12, `echo -e '\x72\x6d'`, Deny, "BackslashOperator"},
		{13, `printf '\x72\x6d /'`, Deny, "BackslashOperator"},
		{14, "rm /{a..z}/{1..1000}", Deny, "BraceExpansion"},
		{15, "echo {a,{b,{c,d}}}", Deny, "BraceExpansion"},

		// --- Allow cases (6) ---
		{1, "rm -rf /", Allow, ""},
		{16, `python -c print("hello")`, Allow, ""},
		{17, "ls -la /home", Allow, ""},
		{18, "echo $HOME", Allow, ""},
		{19, "cat file.txt | grep foo", Allow, ""},
		{20, "head -n 5 /etc/hostname", Allow, ""},
	}

	allowCount := 0
	denyCount := 0

	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			t.Logf("case #%d: %q", tc.num, tc.cmd)
			result := CheckCommand(tc.cmd, validators)

			if result.Decision != tc.wantDecision {
				t.Errorf("case #%d %q: want %v got %v (validatorID=%s, reason=%s)",
					tc.num, tc.cmd, tc.wantDecision, result.Decision,
					result.ValidatorID, result.Reason)
				return
			}

			if tc.wantDecision == Deny {
				if result.ValidatorID != tc.wantValidator {
					t.Errorf("case #%d %q: want validator %s, got %s",
						tc.num, tc.cmd, tc.wantValidator, result.ValidatorID)
				}
			}
		})

		if tc.wantDecision == Allow {
			allowCount++
		} else {
			denyCount++
		}
	}

	t.Logf("Attack matrix summary: %d Allow + %d Deny = %d total", allowCount, denyCount, len(cases))

	if allowCount != 6 {
		t.Errorf("expected 6 Allow cases, got %d", allowCount)
	}
	if denyCount != 14 {
		t.Errorf("expected 14 Deny cases, got %d", denyCount)
	}
}

// TestCheckCommandAllAllow ensures AllValidators pass normal commands.
func TestCheckCommandAllAllow(t *testing.T) {
	validators := AllValidators()
	normalCmds := []string{
		"ls -la",
		"git status",
		"echo hello world",
		"cat /etc/hostname",
		"python3 script.py",
		"go test ./...",
		"npm run build",
	}
	for _, cmd := range normalCmds {
		result := CheckCommand(cmd, validators)
		if result.Decision != Allow {
			t.Errorf("normal command %q was denied by %s: %s", cmd, result.ValidatorID, result.Reason)
		}
	}
}

// TestAllValidatorsHaveUniqueIDs ensures no two validators share the same ID.
func TestAllValidatorsHaveUniqueIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range AllValidators() {
		id := v.ID()
		if seen[id] {
			t.Errorf("duplicate validator ID: %s", id)
		}
		seen[id] = true
	}
}

// TestAllValidatorsPresent ensures all 8 expected validators are registered.
func TestAllValidatorsPresent(t *testing.T) {
	expected := []string{
		"ControlChar", "Unicode", "CR", "CommandSubstitution",
		"IFS", "ProcEnviron", "BackslashOperator", "BraceExpansion",
	}
	validators := AllValidators()
	if len(validators) != len(expected) {
		t.Errorf("expected %d validators, got %d", len(expected), len(validators))
	}
	ids := map[string]bool{}
	for _, v := range validators {
		ids[v.ID()] = true
	}
	for _, e := range expected {
		if !ids[e] {
			t.Errorf("missing validator: %s", e)
		}
	}
}
