package main

// unicodeValidator detects dangerous Unicode codepoints:
//   - U+202E RIGHT-TO-LEFT OVERRIDE (RTL spoofing)
//   - U+00A0 NO-BREAK SPACE (NBSP, can bypass word-splitting checks)
//   - U+200B ZERO WIDTH SPACE
//   - U+200C ZERO WIDTH NON-JOINER
//   - U+200D ZERO WIDTH JOINER
//   - U+FEFF ZERO WIDTH NO-BREAK SPACE / BOM
type unicodeValidator struct{}

// NewUnicodeValidator returns a Validator for dangerous Unicode codepoints.
func NewUnicodeValidator() Validator {
	return &unicodeValidator{}
}

// dangerousRunes maps dangerous codepoints to human-readable descriptions.
// Using explicit hex rune literals avoids invisible-character encoding issues in source.
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
