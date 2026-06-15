package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// defaultImageFilename must yield a clean ASCII date-form name (image-YYYYMMDD-HHMMSS.png),
// not the old gemini-image-{unix} (giant timestamp + leaked model name). The frontend
// falls back to this name when the LLM writes no markdown alt, so it must read cleanly.
func TestDefaultImageFilename(t *testing.T) {
	fixed := time.Date(2026, 6, 16, 18, 36, 47, 0, time.UTC)
	got := defaultImageFilename(fixed)
	if got != "image-20260616-183647.png" {
		t.Errorf("defaultImageFilename = %q; want image-20260616-183647.png", got)
	}
	if strings.Contains(got, "gemini") {
		t.Errorf("filename must not leak model name: %q", got)
	}
	if !regexp.MustCompile(`^image-\d{8}-\d{6}\.png$`).MatchString(got) {
		t.Errorf("filename %q does not match image-YYYYMMDD-HHMMSS.png", got)
	}
}

func TestImageGenTool_Name(t *testing.T) {
	if got := (&imageGenTool{}).Name(); got != "image_gen" {
		t.Errorf("Name = %q; want image_gen", got)
	}
}

func TestImageGenTool_UserFacingName(t *testing.T) {
	if got := (&imageGenTool{}).UserFacingName(); got != "图像生成" {
		t.Errorf("UserFacingName = %q; want 图像生成", got)
	}
}

func TestImageGenTool_IsEnabled(t *testing.T) {
	tool := &imageGenTool{}
	if tool.IsEnabled(ToolConfig{EnableImageGen: false}) {
		t.Error("disabled when EnableImageGen=false")
	}
	if !tool.IsEnabled(ToolConfig{EnableImageGen: true}) {
		t.Error("enabled when EnableImageGen=true")
	}
}

func TestImageGenTool_Execute_ReturnsProviderNotConfigured(t *testing.T) {
	tool := &imageGenTool{}
	res, err := tool.Execute(context.Background(), []byte(`{"prompt":"a cat"}`))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.Contains(out["error"], "database store context is not configured") {
		t.Errorf("expected soft error message, got: %s", out["error"])
	}
}
