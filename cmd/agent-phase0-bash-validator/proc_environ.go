package main

import "regexp"

// procEnvironValidator detects access to sensitive /proc filesystem paths:
//   - /proc/*/environ  — process environment variables (may contain secrets)
//   - /proc/*/cmdline  — process command line arguments
//   - /proc/*/maps     — process memory maps
//   - /proc/*/status   — process status info
//   - /proc/*/fd       — process file descriptors
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
