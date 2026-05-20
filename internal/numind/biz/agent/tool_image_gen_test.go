package agent

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/numind/biz/sandbox"
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
	_, err := tool.Execute(context.Background(), []byte(`{"prompt":"a cat"}`))
	if !errors.Is(err, sandbox.ErrImageGenProviderNotConfigured) {
		t.Errorf("err = %v; want ErrImageGenProviderNotConfigured", err)
	}
}
