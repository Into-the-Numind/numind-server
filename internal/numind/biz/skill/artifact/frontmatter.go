// Package artifact 是 agent-mode v2 Skill-as-Artifact 子包，封装独立 Skill 资产的
// CRUD / Binding / frontmatter parser / 版本快照逻辑。
//
// ADR-13（spec 2026-05-24-agent-mode-v2-skill-as-artifact-design.md §0）：
// v2 所有代码放本子包，与 v1 `internal/numind/biz/skill/*.go`（agent_definition 业务编排）
// 完全隔离，避免命名冲突和语义混淆。
package artifact

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"numind-server/internal/pkg/errno"
)

// frontmatterDelimiter 是 YAML frontmatter 起止行的标记（trim 后必须严格等于 `---`）。
const frontmatterDelimiter = "---"

// Frontmatter 是 Skill artifact 的 YAML 头部（spec §3.2）。
//
// YAML 字段名固定（与前端 CodeMirror 编辑器和外部 SKILL.md 文件格式对齐）：
//
//	---
//	name: 销售数据分析师
//	description: 分析销售数据并生成日报
//	when_to_use: 用户上传 CSV/Excel 文件时
//	allowed_tools:
//	  - web_search
//	  - bash_exec
//	---
//
// 空值字段（description / when_to_use / allowed_tools）使用 omitempty，序列化时省略以保持
// frontmatter 简洁。Name 字段无 omitempty——业务规则要求 name 必填，序列化空 name 也保留 key
// 以便上层校验。
type Frontmatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description,omitempty"`
	WhenToUse    string   `yaml:"when_to_use,omitempty"`
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
}

// Parse 解析 markdown 内容，分离 YAML frontmatter 与 body（spec §3.3 算法）。
//
// 算法：
//  1. 仅识别**首行** `---`（trim 后等于 `---`）作为 frontmatter 起始；后续 `---` 一律视作
//     markdown 水平线（horizontal ruler），不会被误识别为 frontmatter。
//  2. 找到首行 `---` 后，向下逐行扫描；下一个 trim 后等于 `---` 的独立行视为 frontmatter
//     结束。
//  3. 起止行之间的内容用 `yaml.Unmarshal` 解析为 Frontmatter。
//  4. 若首行不是 `---`，整篇都是 body，Frontmatter 返回零值，err 为 nil。
//  5. YAML 解析失败时返回 `errno.ErrSkillArtifactFrontmatterInvalid`（带原始 err 信息）；
//     service 层可决定 fallback 策略（如保留 raw content 入 body）。
//
// 边界 case：
//   - 空字符串 → 零值 Frontmatter + 空 body + nil err
//   - 仅 `---\n---` → 零值 Frontmatter + 空 body + nil err
//   - 首行 `---` 但找不到结束 `---` → 视为无 frontmatter（整篇当 body）
//   - CRLF 换行 → 正确处理（统一按行 split，trim 时一并清掉 \r）
func Parse(content string) (Frontmatter, string, error) {
	var fm Frontmatter

	if content == "" {
		return fm, "", nil
	}

	// 按行扫描——不预先 ReplaceAll \r\n→\n，避免破坏 body 内部 CRLF（极少见但保证语义）。
	// 仅在判定 `---` 时 trim 空白（含 \r）。
	lines := strings.SplitAfter(content, "\n")

	// 规则 1：首行必须 trim 后等于 `---`，否则整篇当 body。
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != frontmatterDelimiter {
		return fm, content, nil
	}

	// 规则 2：从第 2 行起找下一个独立成行的 `---`。
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontmatterDelimiter {
			endIdx = i
			break
		}
	}

	// 找不到结束行：算法保护——整篇当 body，零值 fm，无错误。
	// 理由：首行 `---` 在 markdown 中合法（虽不常见），不应因此报错。
	if endIdx == -1 {
		return fm, content, nil
	}

	// 规则 3：YAML 解析中间段。
	// lines[1:endIdx] 是 frontmatter 内容行（每行末尾仍保留换行）。
	var fmBuf bytes.Buffer
	for i := 1; i < endIdx; i++ {
		fmBuf.WriteString(lines[i])
	}

	if fmBuf.Len() > 0 {
		if err := yaml.Unmarshal(fmBuf.Bytes(), &fm); err != nil {
			return Frontmatter{}, "", errno.ErrSkillArtifactFrontmatterInvalid.SetMessage(
				"frontmatter yaml parse failed: %s", err.Error(),
			)
		}
	}

	// 规则 4 & body 提取：结束行之后是 body_md。
	var bodyBuf bytes.Buffer
	for i := endIdx + 1; i < len(lines); i++ {
		bodyBuf.WriteString(lines[i])
	}

	return fm, bodyBuf.String(), nil
}

// Serialize 将 Frontmatter struct 与 body 反向组装为完整 markdown 字符串。
//
// 输出格式：
//
//	---
//	<yaml.Marshal(fm)>
//	---
//	<body>
//
// 规则：
//  1. frontmatter 总是用 `yaml.v3` Marshal（自动 quote / escape）。
//  2. frontmatter 与 body 之间用单个换行分隔（yaml.Marshal 输出已含尾部 `\n`，再加 `---\n`）。
//  3. body 为空时仍保留 frontmatter + 一个空行尾巴（保持 `Parse(Serialize(fm, "")) == fm`
//     幂等性）。
//  4. yaml.Marshal 几乎不可能失败（fm 是已定义 struct，无 cyclic ref / unsupported type），
//     但保留 err 返回签名以匹配 spec。
func Serialize(fm Frontmatter, body string) (string, error) {
	fmBytes, err := yaml.Marshal(&fm)
	if err != nil {
		return "", fmt.Errorf("Serialize: marshal frontmatter: %w", err)
	}

	var sb strings.Builder
	// 预估容量：3+1 (---\n) + fm + 3+1 (---\n) + body
	sb.Grow(8 + len(fmBytes) + len(body))

	sb.WriteString(frontmatterDelimiter)
	sb.WriteByte('\n')
	sb.Write(fmBytes) // yaml.Marshal 已保证以 \n 结尾
	sb.WriteString(frontmatterDelimiter)
	sb.WriteByte('\n')
	sb.WriteString(body)

	return sb.String(), nil
}
