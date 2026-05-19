package main

// controlCharValidator detects ASCII control characters (0x00–0x1F except \t \n \r) and 0x7F (DEL).
// These characters can be used to inject hidden commands or confuse parsers.
type controlCharValidator struct{}

// NewControlCharValidator returns a Validator for ASCII control characters.
func NewControlCharValidator() Validator {
	return &controlCharValidator{}
}

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
