package main

import "regexp"

// carriageReturnValidator detects lone \r (CR) characters not followed by \n (LF).
// Bash treats a lone \r as a line terminator, enabling command injection.
type carriageReturnValidator struct {
	re *regexp.Regexp
}

// NewCarriageReturnValidator returns a Validator for lone carriage-return injection.
func NewCarriageReturnValidator() Validator {
	return &carriageReturnValidator{
		re: regexp.MustCompile(`\r[^\n]`),
	}
}

func (v *carriageReturnValidator) ID() string { return "CR" }

func (v *carriageReturnValidator) Validate(cmd string) Result {
	// Also handle \r at end of string (not followed by any character)
	if len(cmd) > 0 && cmd[len(cmd)-1] == '\r' {
		return denyResult(v.ID(),
			"command ends with bare \\r which bash interprets as a line terminator enabling hidden command injection",
			"\\r",
		)
	}
	loc := v.re.FindStringIndex(cmd)
	if loc == nil {
		return allowResult()
	}
	return denyResult(v.ID(),
		"command contains bare \\r (not \\r\\n) which bash interprets as a line terminator enabling hidden command injection",
		cmd[loc[0]:loc[1]],
	)
}
