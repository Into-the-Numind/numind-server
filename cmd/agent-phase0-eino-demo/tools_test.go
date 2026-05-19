package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestGetCurrentDateTool_InfoMetadata verifies that the tool's Info() returns
// the expected name and description.
func TestGetCurrentDateTool_InfoMetadata(t *testing.T) {
	ctx := context.Background()
	tl := newGetCurrentDateTool()

	info, err := tl.Info(ctx)
	if err != nil {
		t.Fatalf("Info() error: %v", err)
	}
	if info.Name != "get_current_date" {
		t.Errorf("Name = %q, want get_current_date", info.Name)
	}
	if info.Desc == "" {
		t.Error("Desc must not be empty")
	}
}

// TestGetCurrentDateTool_ReturnsISO8601 verifies the output format YYYY-MM-DD.
func TestGetCurrentDateTool_ReturnsISO8601(t *testing.T) {
	ct := &currentDateTool{}
	got, err := ct.InvokableRun(context.Background(), "{}", nil...)
	if err != nil {
		t.Fatalf("InvokableRun error: %v", err)
	}
	// Expect YYYY-MM-DD — parse with the reference layout.
	parsed, parseErr := time.Parse("2006-01-02", got)
	if parseErr != nil {
		t.Errorf("output %q is not ISO 8601 YYYY-MM-DD: %v", got, parseErr)
	}

	// Sanity: the year should be at least 2024.
	if parsed.Year() < 2024 {
		t.Errorf("year %d looks implausible", parsed.Year())
	}
}

// TestGetCurrentDateTool_MatchesToday checks that the returned date is today's UTC date.
func TestGetCurrentDateTool_MatchesToday(t *testing.T) {
	ct := &currentDateTool{}
	before := time.Now().UTC().Format("2006-01-02")
	got, err := ct.InvokableRun(context.Background(), "", nil...)
	after := time.Now().UTC().Format("2006-01-02")

	if err != nil {
		t.Fatalf("InvokableRun error: %v", err)
	}
	// got must be either 'before' or 'after' (handles rare midnight rollover).
	if got != before && got != after {
		t.Errorf("returned date %q is not today (%s..%s)", got, before, after)
	}
}

// TestGetCurrentDateTool_HasCorrectDateComponents ensures the returned value
// contains hyphens and plausible component lengths.
func TestGetCurrentDateTool_HasCorrectDateComponents(t *testing.T) {
	ct := &currentDateTool{}
	got, err := ct.InvokableRun(context.Background(), "", nil...)
	if err != nil {
		t.Fatalf("InvokableRun error: %v", err)
	}

	parts := strings.Split(got, "-")
	if len(parts) != 3 {
		t.Fatalf("expected 3 hyphen-separated parts, got %d in %q", len(parts), got)
	}
	if len(parts[0]) != 4 {
		t.Errorf("year part %q should be 4 digits", parts[0])
	}
	if len(parts[1]) != 2 {
		t.Errorf("month part %q should be 2 digits", parts[1])
	}
	if len(parts[2]) != 2 {
		t.Errorf("day part %q should be 2 digits", parts[2])
	}
}

// TestGetCurrentDateTool_IgnoresArguments verifies that any argument value
// (including empty string and JSON) is ignored — the tool always returns today.
func TestGetCurrentDateTool_IgnoresArguments(t *testing.T) {
	ct := &currentDateTool{}
	today := time.Now().UTC().Format("2006-01-02")

	for _, arg := range []string{"", "{}", `{"some_key":"some_value"}`} {
		got, err := ct.InvokableRun(context.Background(), arg, nil...)
		if err != nil {
			t.Errorf("InvokableRun(%q) error: %v", arg, err)
			continue
		}
		// Allow for midnight boundary.
		yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
		tomorrow := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02")
		if got != today && got != yesterday && got != tomorrow {
			t.Errorf("InvokableRun(%q) = %q, want today ~%s", arg, got, today)
		}
	}
}
