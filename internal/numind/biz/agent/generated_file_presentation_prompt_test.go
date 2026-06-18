package agent

import (
	"strings"
	"testing"

	"numind-server/internal/pkg/model"
)

// TestGeneratedFilePresentationAddendum_Content asserts the 问题五 guidance
// constant carries the key instructions (system renders cards; do not write own
// download links/tables; mention each file once) in both English and Chinese.
func TestGeneratedFilePresentationAddendum_Content(t *testing.T) {
	a := GeneratedFilePresentationAddendum
	for _, want := range []string{
		"How Generated Files Are Presented",
		"Do NOT write your own download link",
		"renders each generated file as a card",
		"生成文件如何呈现",
		"不要自己写下载链接、下载表格或文件列表",
		"渲染成卡片",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("GeneratedFilePresentationAddendum missing %q", want)
		}
	}
}

// TestAssembleSystemPrompt_IncludesPresentationGuidance verifies the new guidance
// appears in the assembled system prompt when injected into the tools section
// exactly as runner.go / runner_runstream.go do (OutputToolsPriorityAddendum +
// GeneratedFilePresentationAddendum).
func TestAssembleSystemPrompt_IncludesPresentationGuidance(t *testing.T) {
	r := &agentRunner{}
	ad := &model.AgentDefinition{SystemPrompt: "你是助手"}
	toolsSection := OutputToolsPriorityAddendum + GeneratedFilePresentationAddendum

	got := r.assembleSystemPrompt(
		ad,
		"",           // tenantHardRulesPlaceholder
		"",           // body
		nil,          // skills
		"",           // agentMd
		"",           // selector
		"",           // dialectic
		"",           // temporal
		"",           // memoryDisclaimer
		"",           // memorySystem
		"",           // memoriesSectionHeader
		toolsSection, // toolsSection
	)

	if !strings.Contains(got, "How Generated Files Are Presented") {
		t.Errorf("assembled system prompt missing generated-file presentation guidance; got=%q", got)
	}
	if !strings.Contains(got, "不要自己写下载链接、下载表格或文件列表") {
		t.Errorf("assembled system prompt missing Chinese presentation guidance; got=%q", got)
	}
}
