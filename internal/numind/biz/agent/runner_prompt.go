package agent

import (
	"strings"

	"numind-server/internal/numind/biz/skill"
	"numind-server/internal/pkg/model"
)

// ShouldUseV2Prompt 决定是否走新 4 段 prompt 拼装路径。
// nil-safe：ad 为 nil 时返回 false（fallback Legacy）。
// 仅当机构方在 AgentBuilder 填了非空白行为指引时启用 V2 路径。
func ShouldUseV2Prompt(ad *model.AgentDefinition) bool {
	return ad != nil && strings.TrimSpace(ad.SystemPrompt) != ""
}

// PromptSegment 一段 system prompt，附带语义标签。
// 未来切到 message-blocks + cache_control 时，可按 Name 决定 cache_control 注入位置。
type PromptSegment struct {
	Name string // "platform_head" | "institution" | "end_user_context" | "platform_safety_footer"
	Text string
}

// PromptSegments 多段容器。Render 拼成最终 system prompt 字符串。
type PromptSegments struct {
	Segments []PromptSegment
}

// Append 添加一段。空字符串（含纯空白）会被过滤，不会出现在 Render 输出中。
func (ps *PromptSegments) Append(name, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	ps.Segments = append(ps.Segments, PromptSegment{Name: name, Text: text})
}

// Render 用 "\n\n" 拼段。空段（已被 Append 过滤）不会出现，所以不产生多余空行。
func (ps *PromptSegments) Render() string {
	parts := make([]string, 0, len(ps.Segments))
	for _, s := range ps.Segments {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

// BuildSystemPromptV2 走新 4 段路径，仅在 ad.SystemPrompt 非空时调用。
//
// 段构成：
//
//	§1 platform_head            PlatformBasePrompt
//	§2 institution              ad.SystemPrompt + skillCatalog + toolsHint
//	§3 end_user_context         memoryHeader + agentMd + selector + dialectic + temporal + memoryDisclaimer + memorySystem
//	§4 platform_safety_footer   PlatformSafetyFooter
//
// 调用者（runner.go 主流程）负责把各 source 字段组装成 institutionSection / userContext，
// 此函数只做最终四段拼接 + segment 标签注入。
func BuildSystemPromptV2(institutionSection, userContext string) string {
	ps := &PromptSegments{}
	ps.Append("platform_head", skill.PlatformBasePrompt)
	ps.Append("institution", institutionSection)
	ps.Append("end_user_context", userContext)
	ps.Append("platform_safety_footer", skill.PlatformSafetyFooter)
	return ps.Render()
}

// BuildInstitutionSection 组装 §2 段内容：机构 system_prompt + skill catalog + tools hint。
// 使用 "\n\n" 拼内部子段，与 PromptSegments.Render 同分隔风格。空子段被过滤。
func BuildInstitutionSection(systemPrompt, skillCatalog, toolsHint string) string {
	parts := []string{}
	for _, s := range []string{systemPrompt, skillCatalog, toolsHint} {
		if t := strings.TrimSpace(s); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// BuildUserContextSection 组装 §3 段：memoriesHeader（条件）+ 5 个 memory block 拼接。
// 沿用旧路径行为：5 个 block 任一非空时挂 "## Memories" header；全空则整段为空。
//
// 注意：memoryDisclaimer 与 memorySystem 同进同退（disclaimer 自身不计入 hasAny 判定）；
// 仅当 5 个判定 block（agentMd/selector/dialectic/temporal/memorySystem）任一非空时
// 才挂 header — 保留旧路径语义。
//
// 各 block 内容由调用方保证已含所需换行（旧 inline 拼装里这些字符串都自带前导 \n\n
// 或 \n，本函数不额外插入分隔符，避免双空行 / 空段尾随空行等不一致）。
func BuildUserContextSection(
	agentMd, selector, dialectic, temporal, memoryDisclaimer, memorySystem string,
) string {
	hasAny := agentMd != "" || selector != "" || dialectic != "" ||
		temporal != "" || memorySystem != ""
	if !hasAny {
		return ""
	}
	const memoriesHeader = "## Memories\n"
	return memoriesHeader +
		agentMd +
		selector +
		dialectic +
		temporal +
		memoryDisclaimer +
		memorySystem
}
