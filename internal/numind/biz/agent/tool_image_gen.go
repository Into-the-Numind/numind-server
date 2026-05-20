package agent

import (
	"context"
	"errors"
)

type imageGenTool struct {
	BaseTool
}

var _ FullTool = (*imageGenTool)(nil)

func (t *imageGenTool) Name() string { return "image_gen" }
func (t *imageGenTool) Description() string {
	return "[stub] Generate an image. Activated when aiservice.ImageGenerate is added (follow-up feature)."
}
func (t *imageGenTool) UserFacingName() string        { return "图像生成" }
func (t *imageGenTool) NarrationVerb() string         { return "生成" }
func (t *imageGenTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableImageGen }

func (t *imageGenTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	return nil, errors.New("image_gen requires aiservice.ImageGenerate entry (planned for follow-up feature)")
}
