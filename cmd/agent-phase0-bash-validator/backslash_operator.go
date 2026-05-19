package main

import "regexp"

// backslashOperatorValidator detects hex/octal/unicode escape sequences in echo -e or printf
// contexts that can be used to encode dangerous commands and bypass naive string filters.
//   - \xNN  hex escape
//   - \uNNNN unicode escape
//   - \0NNN  octal escape
//
// Only flagged when appearing in echo -e or printf argument context.
type backslashOperatorValidator struct {
	// contextRe matches echo -e or printf followed by content containing backslash escapes
	contextRe *regexp.Regexp
}

// NewBackslashOperatorValidator returns a Validator for backslash-encoded bypass attempts.
func NewBackslashOperatorValidator() Validator {
	return &backslashOperatorValidator{
		// Match echo -e or printf commands that contain \x, \u, or \0 escape sequences
		contextRe: regexp.MustCompile(`(?i)(echo\s+-[a-zA-Z]*e[a-zA-Z]*|printf)\s+['"` + "`" + `]?[^;|&]*\\[xu0-7]`),
	}
}

func (v *backslashOperatorValidator) ID() string { return "BackslashOperator" }

func (v *backslashOperatorValidator) Validate(cmd string) Result {
	loc := v.contextRe.FindStringIndex(cmd)
	if loc == nil {
		return allowResult()
	}
	return denyResult(v.ID(),
		"command uses backslash hex/octal/unicode escapes in echo -e or printf which can encode hidden commands",
		cmd[loc[0]:loc[1]],
	)
}
