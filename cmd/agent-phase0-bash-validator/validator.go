package main

// Decision represents whether a command is allowed or denied.
type Decision int

const (
	Allow Decision = iota
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

// allowResult returns a Result indicating the command passed this validator.
func allowResult() Result {
	return Result{Decision: Allow}
}

// denyResult returns a Result indicating the command was denied.
func denyResult(validatorID, reason, pattern string) Result {
	return Result{
		Decision:    Deny,
		ValidatorID: validatorID,
		Reason:      reason,
		Pattern:     pattern,
	}
}
