package agent

import (
	"context"
	"strings"
	"testing"
)

func TestGetCurrentDateTool_Execute(t *testing.T) {
	tool := &getCurrentDateTool{}

	result, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	s := string(result)
	// result should be a JSON string like "2026-01-20"
	if len(s) != 12 { // 10 chars date + 2 quotes
		t.Errorf("expected 12-char result (quoted date), got %q (len=%d)", s, len(s))
	}
	if !strings.HasPrefix(s, `"`) || !strings.HasSuffix(s, `"`) {
		t.Errorf("result should be a JSON string (quoted), got %q", s)
	}
	inner := s[1 : len(s)-1]
	parts := strings.Split(inner, "-")
	if len(parts) != 3 {
		t.Errorf("expected YYYY-MM-DD format, got %q", inner)
	}
}

func TestGetCurrentDateTool_IsReadOnly(t *testing.T) {
	tool := &getCurrentDateTool{}
	if !tool.IsReadOnly() {
		t.Error("get_current_date should be read-only")
	}
}

func TestGetCurrentDateTool_Name(t *testing.T) {
	tool := &getCurrentDateTool{}
	if tool.Name() != "get_current_date" {
		t.Errorf("unexpected name: %s", tool.Name())
	}
}
