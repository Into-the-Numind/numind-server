package rag

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/retrieval/domain"
)

// FlagAnswerabilityGate 控制是否启用可答性门。关（默认/缺省）→ 门直接放行（fail-open），不拒答。
const FlagAnswerabilityGate = "features.answerability_gate.enabled"

// gateMaxEvidenceChars 喂给门的每条资料截断长度，控 token 与延迟。
const gateMaxEvidenceChars = 400

const gatePrompt = `你是知识库可答性判定器。根据【用户问题】和【检索到的资料片段】，判断：仅凭这些资料能否实质回答这个问题。
输出严格 JSON：{"answerable": true 或 false}。
判 true：资料里确实含有回答该问题所需的信息。
判 false：资料明显是别的主题/别的产品/别的对象（问 A 答的是 B），或只是字面沾边但实际答不了。
只输出 JSON，不要任何解释。`

type gateOut struct {
	Answerable bool `json:"answerable"`
}

// Gate 实现 port.AnswerabilityGate。flag 关时直接放行；开时经 aiservice 统一入口
// （salesrag.intent → deepseek-v4-flash 非思考）做一次可答性判定。任何错误均 fail-open。
type Gate struct{}

// NewGate 构造可答性门。
func NewGate() *Gate { return &Gate{} }

// CanAnswer 实现 port.AnswerabilityGate。
func (g *Gate) CanAnswer(ctx context.Context, query string, chunks []domain.KnowledgeChunk) (bool, string, error) {
	// flag 关 → 放行（不改变现状）。
	if !viper.GetBool(FlagAnswerabilityGate) {
		return true, "gate disabled", nil
	}
	if len(chunks) == 0 {
		return false, "no evidence", nil
	}

	var sb strings.Builder
	for i, c := range chunks {
		content := c.Content
		if len([]rune(content)) > gateMaxEvidenceChars {
			content = string([]rune(content)[:gateMaxEvidenceChars])
		}
		sb.WriteString("- ")
		sb.WriteString(content)
		sb.WriteString("\n")
		if i >= 9 { // 最多看前 10 条，控 token
			break
		}
	}
	userMsg := "【用户问题】\n" + query + "\n\n【检索到的资料片段】\n" + sb.String()

	resp, err := aiservice.Chat(ctx, profile.SalesragIntent, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: gatePrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: userMsg}},
		},
		Temperature:    0,
		MaxTokens:      50,
		ResponseFormat: aiservice.ResponseFormatJSONObject,
	})
	if err != nil {
		// fail-open：门故障绝不阻断检索。
		log.C(ctx).Warnw("answerability gate LLM failed, fail-open (allow)", "error", err)
		return true, "gate error (fail-open)", nil
	}

	var out gateOut
	if jerr := json.Unmarshal([]byte(extractJSON(resp.Content)), &out); jerr != nil {
		log.C(ctx).Warnw("answerability gate JSON parse failed, fail-open (allow)", "error", jerr, "resp", resp.Content)
		return true, "gate parse error (fail-open)", nil
	}
	if !out.Answerable {
		return false, "knowledge base does not contain the answer", nil
	}
	return true, "answerable", nil
}
