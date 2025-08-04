package ali

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestPromptManager_GetTextProcessingPrompt(t *testing.T) {
	pm := NewPromptManager()

	// 测试获取文本处理提示词
	prompt := pm.GetTextProcessingPrompt()

	// 验证提示词不为空
	if prompt == "" {
		t.Error("Text processing prompt should not be empty")
	}

	// 验证提示词包含关键内容
	if !strings.Contains(prompt, "角色") {
		t.Error("Text processing prompt should contain '角色'")
	}

	if !strings.Contains(prompt, "核心任务") {
		t.Error("Text processing prompt should contain '核心任务'")
	}
}

func TestPromptManager_GetImageGenerationPrompt(t *testing.T) {
	pm := NewPromptManager()

	// 测试获取图片生成提示词模板
	prompt := pm.GetImageGenerationPrompt()

	// 验证提示词不为空
	if prompt == "" {
		t.Error("Image generation prompt should not be empty")
	}

	// 验证提示词包含占位符
	if !strings.Contains(prompt, "{content}") {
		t.Error("Image generation prompt should contain '{content}' placeholder")
	}
}

func TestPromptManager_FormatImagePrompt(t *testing.T) {
	pm := NewPromptManager()

	// 测试格式化图片提示词
	content := "这是一段测试文本"
	formattedPrompt := pm.FormatImagePrompt(content)

	// 验证格式化后的提示词包含原始内容
	if !strings.Contains(formattedPrompt, content) {
		t.Errorf("Formatted prompt should contain original content: %s", content)
	}

	// 验证占位符被替换
	if strings.Contains(formattedPrompt, "{content}") {
		t.Error("Placeholder should be replaced with actual content")
	}
}

func TestPromptManager_WithCustomConfig(t *testing.T) {
	// 设置测试配置
	viper.Set("ai_prompts.text_processing", "自定义文本处理提示词")
	viper.Set("ai_prompts.image_generation", "自定义图片生成模板：{content}")

	pm := NewPromptManager()

	// 测试自定义文本处理提示词
	textPrompt := pm.GetTextProcessingPrompt()
	if textPrompt != "自定义文本处理提示词" {
		t.Errorf("Expected custom text prompt, got: %s", textPrompt)
	}

	// 测试自定义图片生成提示词
	imagePrompt := pm.GetImageGenerationPrompt()
	if imagePrompt != "自定义图片生成模板：{content}" {
		t.Errorf("Expected custom image prompt, got: %s", imagePrompt)
	}

	// 测试格式化
	formatted := pm.FormatImagePrompt("测试内容")
	expected := "自定义图片生成模板：测试内容"
	if formatted != expected {
		t.Errorf("Expected formatted prompt: %s, got: %s", expected, formatted)
	}

	// 清理测试配置
	viper.Reset()
}
