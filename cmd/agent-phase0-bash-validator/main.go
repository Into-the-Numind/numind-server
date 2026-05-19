// Package main implements 8 P0 Bash security validators that detect dangerous
// shell command patterns before execution in a sandboxed agent environment.
//
// Validators:
//   - ControlChar:          ASCII control characters (0x00–0x1F excl. \t\n\r) + DEL (0x7F)
//   - Unicode:              RTL override / NBSP / zero-width codepoints
//   - CR:                   Lone carriage-return injection (\r not followed by \n)
//   - CommandSubstitution:  $(...) / `...` / <(...) / >(...)
//   - IFS:                  $IFS / ${IFS} / ANSI-C quoting $'...'
//   - ProcEnviron:          /proc/*/environ|cmdline|maps|status|fd
//   - BackslashOperator:    \x / \u / \0 escapes inside echo -e / printf
//   - BraceExpansion:       {a,b,c} list or {a..z} / {1..N} range expansion
//
// Usage: go run . <command>
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// AllValidators returns all 8 P0 validators in priority order.
func AllValidators() []Validator {
	return []Validator{
		NewControlCharValidator(),
		NewUnicodeValidator(),
		NewCarriageReturnValidator(),
		NewCommandSubstitutionValidator(),
		NewIFSValidator(),
		NewProcEnvironValidator(),
		NewBackslashOperatorValidator(),
		NewBraceExpansionValidator(),
	}
}

// CheckCommand runs all validators against a command and returns the first Deny result,
// or an Allow result if all validators pass.
func CheckCommand(cmd string, validators []Validator) Result {
	for _, v := range validators {
		result := v.Validate(cmd)
		if result.Decision == Deny {
			return result
		}
	}
	return allowResult()
}

// run executes the CLI logic: accepts args (os.Args[1:]), stdout, stderr writers,
// and returns an exit code. Extracted for testability.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: bash-validator <command>")
		return 1
	}

	cmd := strings.Join(args, " ")
	result := CheckCommand(cmd, AllValidators())

	if result.Decision == Allow {
		fmt.Fprintf(stdout, "ALLOW: %q\n", cmd)
		return 0
	}

	fmt.Fprintf(stderr, "DENY [%s]: %s — matched %q\n", result.ValidatorID, result.Reason, result.Pattern)
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
