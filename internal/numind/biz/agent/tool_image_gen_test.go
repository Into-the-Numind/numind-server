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

// When the aiservice gateway is not wired (unit-test process, no SetDefault),
// the image_gen tool's generateImage routes through aiservice.ImageGen which
// returns a "default gateway not initialized" error. Execute must map that to a
// SOFT tool error (nil Go error) — a hard error would kill the whole agent run.
// This replaces the pre-aiservice assertion (old raw-HTTP path returned
// "database store context is not configured" from the DB provider lookup).
func TestImageGenTool_Execute_ReturnsSoftError_WhenGatewayUnwired(t *testing.T) {
	tool := &imageGenTool{}
	res, err := tool.Execute(context.Background(), []byte(`{"prompt":"a cat"}`))
	if err != nil {
		t.Fatalf("expected nil error (soft reject), got: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !strings.HasPrefix(out["error"], "ERROR:") {
		t.Errorf("expected a soft tool error (ERROR: prefix), got: %s", out["error"])
	}
	if !strings.Contains(out["error"], "gateway not initialized") {
		t.Errorf("expected gateway-not-initialized soft error, got: %s", out["error"])
	}
}
