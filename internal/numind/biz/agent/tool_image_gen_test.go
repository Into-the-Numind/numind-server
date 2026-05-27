package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

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
