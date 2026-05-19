package main

import "regexp"

// braceExpansionValidator detects bash brace expansion that can generate huge argument lists:
//   - {a,b,c}     comma-separated list form
//   - {a..z}      range expansion form
//   - {1..1000}   numeric range (can explode to thousands of args)
//
// Nested brace expansion is also detected via depth analysis.
type braceExpansionValidator struct {
	commaRe *regexp.Regexp
	rangeRe *regexp.Regexp
}

// NewBraceExpansionValidator returns a Validator for brace expansion patterns.
func NewBraceExpansionValidator() Validator {
	return &braceExpansionValidator{
		// Comma form: {anything,anything} — matches even with nested braces
		commaRe: regexp.MustCompile(`\{[^{}]*,[^{}]*\}`),
		// Range form: {a..z} or {1..100}
		rangeRe: regexp.MustCompile(`\{[^{}]*\.\.[^{}]*\}`),
	}
}

func (v *braceExpansionValidator) ID() string { return "BraceExpansion" }

func (v *braceExpansionValidator) Validate(cmd string) Result {
	// Check range form first (e.g. {a..z}, {1..1000})
	if loc := v.rangeRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"command contains brace range expansion which can generate massive argument lists",
			cmd[loc[0]:loc[1]],
		)
	}

	// Check comma form (e.g. {a,b,c})
	if loc := v.commaRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"command contains brace list expansion which may be used to enumerate large sets of paths",
			cmd[loc[0]:loc[1]],
		)
	}

	// Detect nested brace expansion like {a,{b,{c,d}}} by checking brace depth
	if hasNestedBraces(cmd) {
		return denyResult(v.ID(),
			"command contains nested brace expansion which can generate exponentially large argument lists",
			"{...{...}...}",
		)
	}

	return allowResult()
}

// hasNestedBraces returns true if the command contains brace nesting depth > 1.
func hasNestedBraces(cmd string) bool {
	depth := 0
	maxDepth := 0
	for _, ch := range cmd {
		switch ch {
		case '{':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return maxDepth >= 2
}
