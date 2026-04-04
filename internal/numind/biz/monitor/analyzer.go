package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"numind-server/internal/pkg/llm"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"gorm.io/datatypes"
)

// analysisResult AI 分析返回的 JSON 结构
type analysisResult struct {
	Summary  string   `json:"summary"`
	Topics   []string `json:"topics"`
	Category string   `json:"category"`
}

const analyzeSystemPrompt = `你是一个小红书内容分析专家。分析以下笔记内容，返回 JSON 格式：
{"summary": "一句话摘要", "topics": ["选题标签1", "选题标签2"], "category": "内容分类"}
分类选项：美妆护肤、穿搭时尚、美食探店、旅行攻略、母婴育儿、家居生活、运动健身、科技数码、教育学习、商业财经、情感心理、其他`

// AnalyzeSingleNote 使用 AI 分析单条笔记的内容，提取摘要、选题标签和分类
func (mb *MonitorBiz) AnalyzeSingleNote(ctx context.Context, note *model.MonitorNote) error {
	// 构建用户消息
	var userMsg strings.Builder
	userMsg.WriteString(fmt.Sprintf("标题：%s\n", note.Title))
	if note.Content != "" {
		userMsg.WriteString(fmt.Sprintf("正文：%s\n", note.Content))
	}
	if note.Transcript != "" {
		userMsg.WriteString(fmt.Sprintf("视频转录文字：%s\n", note.Transcript))
	}

	messages := []llm.ChatMessage{
		{Role: "system", Content: analyzeSystemPrompt},
		{Role: "user", Content: userMsg.String()},
	}

	// 调用 LLM
	resp, _, err := mb.llm.ChatCompletion(ctx, "deepseek-v3-2-251201", messages, 0.3, 1000)
	if err != nil {
		return fmt.Errorf("AnalyzeSingleNote: llm call: %w", err)
	}

	// 尝试解析 JSON 响应
	// 处理可能被 markdown 代码块包裹的情况
	cleanResp := strings.TrimSpace(resp)
	cleanResp = strings.TrimPrefix(cleanResp, "```json")
	cleanResp = strings.TrimPrefix(cleanResp, "```")
	cleanResp = strings.TrimSuffix(cleanResp, "```")
	cleanResp = strings.TrimSpace(cleanResp)

	var result analysisResult
	if err := json.Unmarshal([]byte(cleanResp), &result); err != nil {
		// JSON 解析失败，保存原始响应作为摘要
		log.Warnw("AnalyzeSingleNote: failed to parse JSON response, saving raw",
			"noteID", note.ID, "error", err, "response", resp)
		note.AISummary = resp
	} else {
		note.AISummary = result.Summary
		note.AICategory = result.Category
		if len(result.Topics) > 0 {
			topicsJSON, _ := json.Marshal(result.Topics)
			note.AITopics = datatypes.JSON(topicsJSON)
		}
	}

	// 保存到数据库
	if err := mb.store.Monitor().UpdateNote(ctx, note); err != nil {
		return fmt.Errorf("AnalyzeSingleNote: update note: %w", err)
	}

	log.Infow("AnalyzeSingleNote: completed", "noteID", note.ID, "category", note.AICategory)
	return nil
}
