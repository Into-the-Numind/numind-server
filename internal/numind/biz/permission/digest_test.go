package permission

import (
	"strings"
	"testing"
)

func TestDigest_Length64Hex(t *testing.T) {
	out := Digest("hello")
	if len(out) != 64 {
		t.Errorf("Digest length = %d, want 64", len(out))
	}
	if !isHex(out) {
		t.Errorf("Digest output not all hex: %q", out)
	}
}

func TestDigest_DeterministicSameInput(t *testing.T) {
	a := Digest("the same input")
	b := Digest("the same input")
	if a != b {
		t.Errorf("Digest not deterministic: %s vs %s", a, b)
	}
}

func TestDigest_DifferentInput(t *testing.T) {
	a := Digest("input one")
	b := Digest("input two")
	if a == b {
		t.Errorf("Digest collision on distinct inputs: %s", a)
	}
}

func TestDigest_EmptyInput(t *testing.T) {
	// SHA-256("") = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	got := Digest("")
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want {
		t.Errorf("Digest(\"\") = %q, want %q", got, want)
	}
}

func TestDigest_UnicodeInput(t *testing.T) {
	out := Digest("中文测试 with emoji 🎉")
	if len(out) != 64 || !isHex(out) {
		t.Errorf("Digest unicode failed: %q", out)
	}
}

func isHex(s string) bool {
	const hexChars = "0123456789abcdef"
	for _, c := range s {
		if !strings.ContainsRune(hexChars, c) {
			return false
		}
	}
	return true
}
