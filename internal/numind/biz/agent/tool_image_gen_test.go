package agent

import (
	"context"
	"testing"
)

func TestImageGenTool_Execute_ReturnsError(t *testing.T) {
	tool := &imageGenTool{}
	_, err := tool.Execute(context.Background(), nil)
	if err == nil {
		t.Error("image_gen stub should return an error")
	}
}

func TestImageGenTool_IsEnabled_FalseWhenDisabled(t *testing.T) {
	tool := &imageGenTool{}
	if tool.IsEnabled(ToolConfig{EnableImageGen: false}) {
		t.Error("image_gen should be disabled when EnableImageGen=false")
	}
}

func TestImageGenTool_IsEnabled_TrueWhenEnabled(t *testing.T) {
	tool := &imageGenTool{}
	if !tool.IsEnabled(ToolConfig{EnableImageGen: true}) {
		t.Error("image_gen should be enabled when EnableImageGen=true")
	}
}

func TestImageGenTool_Name(t *testing.T) {
	tool := &imageGenTool{}
	if tool.Name() != "image_gen" {
		t.Errorf("unexpected name: %s", tool.Name())
	}
}
