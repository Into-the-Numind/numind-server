package main

import "regexp"

// ifsValidator detects IFS (Internal Field Separator) manipulation:
//   - $IFS     bare variable reference
//   - ${IFS}   braced variable reference
//   - $'\t'    ANSI-C quoting with tab (used to inject whitespace that bypasses naive filters)
//   - $'\n'    ANSI-C quoting with newline
//   - $' '     ANSI-C quoting with space
type ifsValidator struct {
	re *regexp.Regexp
}

// NewIFSValidator returns a Validator for IFS manipulation patterns.
func NewIFSValidator() Validator {
	return &ifsValidator{
		// $IFS or ${IFS} or $'...' (ANSI-C quoting)
		re: regexp.MustCompile(`\$\{?IFS\}?|\$'[^']*'`),
	}
}

func (v *ifsValidator) ID() string { return "IFS" }

func (v *ifsValidator) Validate(cmd string) Result {
	loc := v.re.FindStringIndex(cmd)
	if loc == nil {
		return allowResult()
	}
	return denyResult(v.ID(),
		"command contains IFS manipulation or ANSI-C quoting which can be used to bypass word-splitting protections",
		cmd[loc[0]:loc[1]],
	)
}
