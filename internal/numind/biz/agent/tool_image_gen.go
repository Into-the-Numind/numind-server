package agent

import (
	"context"

	"numind-server/internal/numind/biz/sandbox"
)

// imageGenTool is the image_gen FullTool. In #4 the underlying provider
// (wanx2.1-t2i-turbo / wan2.2-t2i-flash via aiservice) is not yet
// registered, so Execute returns a friendly sentinel error.
//
// The full provider wiring is a follow-up feature (likely paired with #12
// billing-integration so token / image cost accounting is in place).
type imageGenTool struct {
	BaseTool
}

var _ FullTool = (*imageGenTool)(nil)

func (t *imageGenTool) Name() string { return "image_gen" }
func (t *imageGenTool) Description() string {
	return "Generate an image from a text prompt. (Provider not yet configured — contact admin.)"
}
func (t *imageGenTool) UserFacingName() string        { return "图像生成" }
func (t *imageGenTool) NarrationVerb() string         { return "生成" }
func (t *imageGenTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableImageGen }

func (t *imageGenTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	// v1: no image-generation provider is registered yet. Return the
	// shared sentinel sandbox.ErrImageGenProviderNotConfigured so the
	// adapter / hooks can detect the case (e.g., suppress audit row).
	return nil, sandbox.ErrImageGenProviderNotConfigured
}
