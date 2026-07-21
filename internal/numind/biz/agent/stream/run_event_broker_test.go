package stream

import "testing"

func TestRunEventCursorValidationAndOrdering(t *testing.T) {
	valid := []string{"0-0", "1-0", "18446744073709551615-99"}
	for _, cursor := range valid {
		if !validRunEventCursor(cursor) {
			t.Fatalf("expected valid cursor %q", cursor)
		}
	}
	invalid := []string{"", "1", "1-", "-1", "1-2-3", "a-1", "1.0-2"}
	for _, cursor := range invalid {
		if validRunEventCursor(cursor) {
			t.Fatalf("expected invalid cursor %q", cursor)
		}
	}

	cases := []struct {
		a, b string
		want int
	}{
		{"1-0", "1-1", -1},
		{"9-99", "10-0", -1},
		{"18446744073709551615-1", "18446744073709551615-0", 1},
		{"42-7", "42-7", 0},
	}
	for _, tc := range cases {
		got := compareRunEventCursor(tc.a, tc.b)
		if got != tc.want {
			t.Fatalf("compare(%q,%q)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
