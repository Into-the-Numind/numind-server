// Package bashvalidator wraps the 8 P0 Phase 0 V3 bash security validators
// into an importable subpackage. The validator implementations are
// extracted verbatim from cmd/agent-phase0-bash-validator/ (which is a
// main package and therefore cannot be imported by business code).
//
// Used by internal/numind/biz/agent/tool_bash_exec.go to gate user-provided
// shell commands before they reach sandbox.ExecCommand.
//
// #6 permission-pipeline will extend this set to 23 validators per the
// agent-mode permission roadmap.
package bashvalidator

import "regexp"

// Decision represents whether a command is allowed or denied.
type Decision int

const (
	// Allow indicates the command passed all checks.
	Allow Decision = iota
	// Deny indicates a validator matched a dangerous pattern.
	Deny
)

// Result holds the outcome of a validator check.
type Result struct {
	Decision    Decision
	ValidatorID string // which validator triggered the deny
	Reason      string // human-readable explanation
	Pattern     string // the matched dangerous pattern
}

// Validator is the interface implemented by each P0 bash security check.
type Validator interface {
	ID() string
	Validate(cmd string) Result
}

func allowResult() Result {
	return Result{Decision: Allow}
}

func denyResult(validatorID, reason, pattern string) Result {
	return Result{
		Decision:    Deny,
		ValidatorID: validatorID,
		Reason:      reason,
		Pattern:     pattern,
	}
}

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

// CheckCommand runs all validators against a command and returns the first
// Deny result, or an Allow result if all validators pass.
func CheckCommand(cmd string, validators []Validator) Result {
	for _, v := range validators {
		result := v.Validate(cmd)
		if result.Decision == Deny {
			return result
		}
	}
	return allowResult()
}

// Validate is the top-level entry the tool_bash_exec gate calls.
// Returns (allow=true, "") if all 8 P0 validators pass, otherwise
// (allow=false, "<ValidatorID>: <Reason> — pattern=<Pattern>").
func Validate(command string) (bool, string) {
	res := CheckCommand(command, AllValidators())
	if res.Decision == Allow {
		return true, ""
	}
	return false, res.ValidatorID + ": " + res.Reason + " — pattern=" + res.Pattern
}

// ===========================================================================
// 1) ControlCharValidator
// ===========================================================================

type controlCharValidator struct{}

// NewControlCharValidator returns a Validator for ASCII control characters.
func NewControlCharValidator() Validator { return &controlCharValidator{} }

func (v *controlCharValidator) ID() string { return "ControlChar" }

func (v *controlCharValidator) Validate(cmd string) Result {
	for i, r := range cmd {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if (r >= 0x00 && r <= 0x1F) || r == 0x7F {
			return denyResult(v.ID(),
				"command contains ASCII control character which may be used to inject hidden commands",
				cmd[i:i+1],
			)
		}
	}
	return allowResult()
}

// ===========================================================================
// 2) UnicodeValidator
// ===========================================================================

type unicodeValidator struct{}

// NewUnicodeValidator returns a Validator for dangerous Unicode codepoints.
func NewUnicodeValidator() Validator { return &unicodeValidator{} }

var dangerousRunes = map[rune]string{
	0x202E: "U+202E RIGHT-TO-LEFT OVERRIDE",
	0x00A0: "U+00A0 NO-BREAK SPACE",
	0x200B: "U+200B ZERO WIDTH SPACE",
	0x200C: "U+200C ZERO WIDTH NON-JOINER",
	0x200D: "U+200D ZERO WIDTH JOINER",
	0xFEFF: "U+FEFF ZERO WIDTH NO-BREAK SPACE (BOM)",
}

func (v *unicodeValidator) ID() string { return "Unicode" }

func (v *unicodeValidator) Validate(cmd string) Result {
	for _, r := range cmd {
		if desc, found := dangerousRunes[r]; found {
			return denyResult(v.ID(),
				"command contains dangerous Unicode codepoint: "+desc,
				string(r),
			)
		}
	}
	return allowResult()
}

// ===========================================================================
// 3) CarriageReturnValidator
// ===========================================================================

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

// ===========================================================================
// 4) CommandSubstitutionValidator
// ===========================================================================

type commandSubstitutionValidator struct {
	re *regexp.Regexp
}

// NewCommandSubstitutionValidator returns a Validator for command substitution patterns.
func NewCommandSubstitutionValidator() Validator {
	return &commandSubstitutionValidator{
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

// ===========================================================================
// 5) IFSValidator
// ===========================================================================

type ifsValidator struct {
	re *regexp.Regexp
}

// NewIFSValidator returns a Validator for IFS manipulation patterns.
func NewIFSValidator() Validator {
	return &ifsValidator{
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

// ===========================================================================
// 6) ProcEnvironValidator
// ===========================================================================

type procEnvironValidator struct {
	re *regexp.Regexp
}

// NewProcEnvironValidator returns a Validator for sensitive /proc path access.
func NewProcEnvironValidator() Validator {
	return &procEnvironValidator{
		re: regexp.MustCompile(`/proc/[^/\s]+/(environ|cmdline|maps|status|fd)`),
	}
}

func (v *procEnvironValidator) ID() string { return "ProcEnviron" }

func (v *procEnvironValidator) Validate(cmd string) Result {
	loc := v.re.FindStringIndex(cmd)
	if loc == nil {
		return allowResult()
	}
	return denyResult(v.ID(),
		"command accesses sensitive /proc filesystem path which may leak process secrets or memory",
		cmd[loc[0]:loc[1]],
	)
}

// ===========================================================================
// 7) BackslashOperatorValidator
// ===========================================================================

type backslashOperatorValidator struct {
	contextRe *regexp.Regexp
}

// NewBackslashOperatorValidator returns a Validator for backslash-encoded bypass attempts.
func NewBackslashOperatorValidator() Validator {
	return &backslashOperatorValidator{
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

// ===========================================================================
// 8) BraceExpansionValidator
// ===========================================================================

type braceExpansionValidator struct {
	commaRe *regexp.Regexp
	rangeRe *regexp.Regexp
}

// NewBraceExpansionValidator returns a Validator for brace expansion patterns.
func NewBraceExpansionValidator() Validator {
	return &braceExpansionValidator{
		commaRe: regexp.MustCompile(`\{[^{}]*,[^{}]*\}`),
		rangeRe: regexp.MustCompile(`\{[^{}]*\.\.[^{}]*\}`),
	}
}

func (v *braceExpansionValidator) ID() string { return "BraceExpansion" }

func (v *braceExpansionValidator) Validate(cmd string) Result {
	if loc := v.rangeRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"command contains brace range expansion which can generate massive argument lists",
			cmd[loc[0]:loc[1]],
		)
	}
	if loc := v.commaRe.FindStringIndex(cmd); loc != nil {
		return denyResult(v.ID(),
			"command contains brace list expansion which may be used to enumerate large sets of paths",
			cmd[loc[0]:loc[1]],
		)
	}
	if hasNestedBraces(cmd) {
		return denyResult(v.ID(),
			"command contains nested brace expansion which can generate exponentially large argument lists",
			"{...{...}...}",
		)
	}
	return allowResult()
}

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
