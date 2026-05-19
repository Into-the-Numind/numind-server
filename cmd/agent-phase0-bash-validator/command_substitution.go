package main

import "regexp"

// commandSubstitutionValidator detects bash command substitution forms:
//   - $(...)   modern form
//   - `...`    backtick form
//   - <(...)   process substitution (input)
//   - >(...)   process substitution (output)
type commandSubstitutionValidator struct {
	re *regexp.Regexp
}

// NewCommandSubstitutionValidator returns a Validator for command substitution patterns.
func NewCommandSubstitutionValidator() Validator {
	return &commandSubstitutionValidator{
		// Match $( or ` or <( or >(
		re: regexp.MustCompile("\\$\\(|`|<\\(|>\\("),
	}
}

func (v *commandSubstitutionValidator) ID() string { return "CommandSubstitution" }

func (v *commandSubstitutionValidator) Validate(cmd string) Result {
	loc := v.re.FindStringIndex(cmd)
	if loc == nil {
		return allowResult()
	}
	return denyResult(v.ID(),
		"command contains command substitution which would execute embedded commands",
		cmd[loc[0]:loc[1]],
	)
}
