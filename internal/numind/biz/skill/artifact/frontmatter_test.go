package artifact

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"numind-server/internal/pkg/errno"
)

// =====================================================================
// Parse — 基础场景
// =====================================================================

func TestParse_EmptyString(t *testing.T) {
	fm, body, err := Parse("")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm = %+v, want zero", fm)
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestParse_NoFrontmatter_PlainText(t *testing.T) {
	in := "Hello world\nNo frontmatter here.\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero, got %+v", fm)
	}
	if body != in {
		t.Errorf("body = %q, want full input", body)
	}
}

func TestParse_NoFrontmatter_StartsWithHash(t *testing.T) {
	in := "# Heading\n\nSome content."
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero, got %+v", fm)
	}
	if body != in {
		t.Errorf("body mismatch")
	}
}

func TestParse_EmptyFrontmatter(t *testing.T) {
	in := "---\n---\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm = %+v, want zero", fm)
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestParse_EmptyFrontmatter_WithBody(t *testing.T) {
	in := "---\n---\n# Body\nText here."
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm = %+v, want zero", fm)
	}
	want := "# Body\nText here."
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
}

func TestParse_NameOnly(t *testing.T) {
	in := "---\nname: 销售助手\n---\n# Body\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "销售助手" {
		t.Errorf("Name = %q", fm.Name)
	}
	if body != "# Body\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParse_AllFields(t *testing.T) {
	in := `---
name: 销售数据分析师
description: 分析销售数据并生成日报
when_to_use: 用户上传 CSV/Excel 文件并要求"分析"或"日报"时
allowed_tools:
  - web_search
  - bash_exec
---

# 销售数据分析师

你是一名擅长 ...
`
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "销售数据分析师" {
		t.Errorf("Name = %q", fm.Name)
	}
	if fm.Description != "分析销售数据并生成日报" {
		t.Errorf("Description = %q", fm.Description)
	}
	if fm.WhenToUse != `用户上传 CSV/Excel 文件并要求"分析"或"日报"时` {
		t.Errorf("WhenToUse = %q", fm.WhenToUse)
	}
	if !reflect.DeepEqual(fm.AllowedTools, []string{"web_search", "bash_exec"}) {
		t.Errorf("AllowedTools = %v", fm.AllowedTools)
	}
	if !strings.HasPrefix(body, "\n# 销售数据分析师") {
		t.Errorf("body prefix wrong: %q", body[:min(40, len(body))])
	}
}

func TestParse_DescriptionOnly(t *testing.T) {
	in := "---\nname: X\ndescription: Y\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" || fm.Description != "Y" {
		t.Errorf("got %+v", fm)
	}
}

func TestParse_WhenToUseOnly(t *testing.T) {
	in := "---\nname: X\nwhen_to_use: trigger here\n---\nbody"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.WhenToUse != "trigger here" {
		t.Errorf("WhenToUse = %q", fm.WhenToUse)
	}
	if body != "body" {
		t.Errorf("body = %q", body)
	}
}

func TestParse_AllowedTools_Single(t *testing.T) {
	in := "---\nname: X\nallowed_tools:\n  - web_search\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm.AllowedTools, []string{"web_search"}) {
		t.Errorf("AllowedTools = %v", fm.AllowedTools)
	}
}

func TestParse_AllowedTools_FlowStyle(t *testing.T) {
	in := "---\nname: X\nallowed_tools: [a, b, c]\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm.AllowedTools, []string{"a", "b", "c"}) {
		t.Errorf("AllowedTools = %v", fm.AllowedTools)
	}
}

func TestParse_AllowedTools_Many(t *testing.T) {
	in := "---\nname: X\nallowed_tools:\n  - t1\n  - t2\n  - t3\n  - t4\n  - t5\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(fm.AllowedTools) != 5 {
		t.Errorf("got %d tools, want 5", len(fm.AllowedTools))
	}
}

func TestParse_AllowedTools_Empty(t *testing.T) {
	in := "---\nname: X\nallowed_tools: []\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(fm.AllowedTools) != 0 {
		t.Errorf("AllowedTools = %v, want empty", fm.AllowedTools)
	}
}

// =====================================================================
// Parse — body 含 `---`（关键边界 case：不应被误识别为 frontmatter 终止）
// =====================================================================

func TestParse_BodyContainsRuler(t *testing.T) {
	// body 中的 `---` 是 markdown horizontal ruler。在 frontmatter 结束之后，所有
	// `---` 都应保留为 body 一部分。
	in := "---\nname: X\n---\n# Body\n\n---\n\nMore content."
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" {
		t.Errorf("Name = %q", fm.Name)
	}
	if !strings.Contains(body, "---") {
		t.Errorf("body should preserve ruler ---, got %q", body)
	}
	if !strings.Contains(body, "More content") {
		t.Errorf("body should contain More content")
	}
}

func TestParse_BodyContainsMultipleRulers(t *testing.T) {
	in := "---\nname: X\n---\nA\n---\nB\n---\nC\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" {
		t.Errorf("Name = %q", fm.Name)
	}
	expectedBody := "A\n---\nB\n---\nC\n"
	if body != expectedBody {
		t.Errorf("body = %q, want %q", body, expectedBody)
	}
}

func TestParse_NoFrontmatter_BodyStartsWithRuler_Mid(t *testing.T) {
	// 首行非 `---`（即使第二行是），整篇当 body。
	in := "hello\n---\nname: X\n---\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero, got %+v", fm)
	}
	if body != in {
		t.Errorf("body should equal input")
	}
}

// =====================================================================
// Parse — body 各种 markdown 内容
// =====================================================================

func TestParse_BodyWithHeading(t *testing.T) {
	in := "---\nname: X\n---\n# H1\n## H2\n### H3\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if body != "# H1\n## H2\n### H3\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParse_BodyWithCodeBlock(t *testing.T) {
	in := "---\nname: X\n---\n```go\nfunc main() {}\n```\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(body, "```go") || !strings.Contains(body, "func main()") {
		t.Errorf("body = %q", body)
	}
}

func TestParse_BodyWithList(t *testing.T) {
	in := "---\nname: X\n---\n- item1\n- item2\n- item3\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(body, "- item1") {
		t.Errorf("body = %q", body)
	}
}

func TestParse_BodyWithNumberedList(t *testing.T) {
	in := "---\nname: X\n---\n1. first\n2. second\n3. third\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(body, "1. first") {
		t.Errorf("body = %q", body)
	}
}

func TestParse_BodyWithLink(t *testing.T) {
	in := "---\nname: X\n---\nSee [docs](https://example.com).\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(body, "[docs](https://example.com)") {
		t.Errorf("body = %q", body)
	}
}

func TestParse_BodyWithTable(t *testing.T) {
	in := "---\nname: X\n---\n| A | B |\n|---|---|\n| 1 | 2 |\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// markdown table 包含 `|---|---|` —— 不影响 frontmatter（首行不是单独 `---`）
	if !strings.Contains(body, "|---|---|") {
		t.Errorf("body should preserve table: %q", body)
	}
}

func TestParse_BodyEmpty(t *testing.T) {
	in := "---\nname: X\n---\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" {
		t.Errorf("Name = %q", fm.Name)
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestParse_BodyOnlyWhitespace(t *testing.T) {
	in := "---\nname: X\n---\n\n\n\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if body != "\n\n\n" {
		t.Errorf("body = %q", body)
	}
}

// =====================================================================
// Parse — UTF-8 / 中文 / emoji
// =====================================================================

func TestParse_ChineseName(t *testing.T) {
	in := "---\nname: 测试助手中文名字\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "测试助手中文名字" {
		t.Errorf("Name = %q", fm.Name)
	}
}

func TestParse_EmojiInDescription(t *testing.T) {
	in := "---\nname: X\ndescription: \"hello 🎉 world 🚀\"\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(fm.Description, "🎉") {
		t.Errorf("Description = %q", fm.Description)
	}
}

func TestParse_MixedScripts(t *testing.T) {
	in := "---\nname: \"日本語 한국어 العربية\"\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(fm.Name, "日本語") {
		t.Errorf("Name = %q", fm.Name)
	}
}

func TestParse_ChineseBody(t *testing.T) {
	in := "---\nname: X\n---\n你是一名助手，请回答问题。"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if body != "你是一名助手，请回答问题。" {
		t.Errorf("body = %q", body)
	}
}

// =====================================================================
// Parse — 边界格式：缩进 / quote / 特殊字符
// =====================================================================

func TestParse_QuotedStrings(t *testing.T) {
	in := "---\nname: \"X: with colon\"\ndescription: 'Y: with colon'\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X: with colon" {
		t.Errorf("Name = %q", fm.Name)
	}
	if fm.Description != "Y: with colon" {
		t.Errorf("Description = %q", fm.Description)
	}
}

func TestParse_StringWithSpecialChars(t *testing.T) {
	in := "---\nname: \"a&b<c>d\"\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "a&b<c>d" {
		t.Errorf("Name = %q", fm.Name)
	}
}

func TestParse_StringWithBackslash(t *testing.T) {
	in := "---\nname: 'a\\nb'\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// single-quoted YAML 不解释 \n
	if fm.Name != "a\\nb" {
		t.Errorf("Name = %q", fm.Name)
	}
}

func TestParse_LongDescription(t *testing.T) {
	desc := strings.Repeat("very long description ", 50)
	in := "---\nname: X\ndescription: \"" + desc + "\"\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Description != desc {
		t.Errorf("description length mismatch")
	}
}

// =====================================================================
// Parse — CRLF line endings
// =====================================================================

func TestParse_CRLF_Frontmatter(t *testing.T) {
	in := "---\r\nname: X\r\ndescription: Y\r\n---\r\n# body\r\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" || fm.Description != "Y" {
		t.Errorf("fm = %+v", fm)
	}
	if !strings.Contains(body, "# body") {
		t.Errorf("body = %q", body)
	}
}

func TestParse_CRLF_Body(t *testing.T) {
	in := "---\nname: X\n---\nLine1\r\nLine2\r\n"
	_, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(body, "Line1\r\n") || !strings.Contains(body, "Line2") {
		t.Errorf("body = %q", body)
	}
}

func TestParse_MixedCRLF(t *testing.T) {
	in := "---\nname: X\r\ndescription: Y\n---\r\nBody"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" || fm.Description != "Y" {
		t.Errorf("fm = %+v", fm)
	}
}

// =====================================================================
// Parse — Trailing whitespace / leading whitespace 边界
// =====================================================================

func TestParse_DelimiterWithTrailingSpaces(t *testing.T) {
	in := "---   \nname: X\n---  \n# body"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" {
		t.Errorf("Name = %q", fm.Name)
	}
	if body != "# body" {
		t.Errorf("body = %q", body)
	}
}

func TestParse_FirstLineHasLeadingSpace(t *testing.T) {
	// 首行 " ---"（前导空格）应被识别——trim 后等于 `---`
	in := "  ---\nname: X\n---\nbody"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" {
		t.Errorf("Name = %q", fm.Name)
	}
}

func TestParse_FirstLineDashes_NotEqualDelimiter(t *testing.T) {
	// `----`（4 个 dash）不应被识别为 frontmatter 起始
	in := "----\nname: X\n----\nbody"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero, got %+v", fm)
	}
	if body != in {
		t.Errorf("body should equal input")
	}
}

func TestParse_FirstLineHasInlineContent(t *testing.T) {
	// `--- content` 不应被识别（trim 后不等于 `---`）
	in := "--- content\nname: X\n---\nbody"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero, got %+v", fm)
	}
	if body != in {
		t.Errorf("body should equal input")
	}
}

// =====================================================================
// Parse — 极长 body
// =====================================================================

func TestParse_VeryLongBody_50KB(t *testing.T) {
	body := strings.Repeat("This is a long body line.\n", 2048) // ~50KB
	in := "---\nname: X\n---\n" + body
	fm, gotBody, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" {
		t.Errorf("Name = %q", fm.Name)
	}
	if gotBody != body {
		t.Errorf("body length mismatch: got %d, want %d", len(gotBody), len(body))
	}
}

func TestParse_VeryLongBody_200KB(t *testing.T) {
	body := strings.Repeat("L\n", 100_000) // 200KB
	in := "---\nname: X\n---\n" + body
	_, gotBody, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(gotBody) != len(body) {
		t.Errorf("body length: got %d, want %d", len(gotBody), len(body))
	}
}

// =====================================================================
// Parse — YAML 解析失败
// =====================================================================

func TestParse_InvalidYAML_BadIndent(t *testing.T) {
	in := "---\nname: X\n  : invalid\n---\nbody"
	_, _, err := Parse(in)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errno.ErrSkillArtifactFrontmatterInvalid) {
		t.Errorf("err should be ErrSkillArtifactFrontmatterInvalid, got %v", err)
	}
}

func TestParse_InvalidYAML_TabIndent(t *testing.T) {
	// YAML 不允许 tab 缩进
	in := "---\nname: X\n\tdescription: Y\n---\nbody"
	_, _, err := Parse(in)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errno.ErrSkillArtifactFrontmatterInvalid) {
		t.Errorf("err should be ErrSkillArtifactFrontmatterInvalid")
	}
}

func TestParse_InvalidYAML_UnclosedQuote(t *testing.T) {
	in := "---\nname: \"unclosed\n---\nbody"
	_, _, err := Parse(in)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParse_InvalidYAML_ListNotList(t *testing.T) {
	// allowed_tools 应是 []string，给 string → unmarshal error
	in := "---\nname: X\nallowed_tools: not_a_list\n---\nbody"
	_, _, err := Parse(in)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// =====================================================================
// Parse — 缺少结束 delimiter
// =====================================================================

func TestParse_MissingClosingDelimiter(t *testing.T) {
	// 首行 `---` 但找不到结束 `---` — 整篇当 body
	in := "---\nname: X\nNo closing delimiter\nMore lines"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero, got %+v", fm)
	}
	if body != in {
		t.Errorf("body should equal input")
	}
}

func TestParse_OnlyOpeningDelimiter(t *testing.T) {
	in := "---\n"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero")
	}
	if body != in {
		t.Errorf("body = %q, want input", body)
	}
}

func TestParse_OnlyDelimiter_NoNewline(t *testing.T) {
	in := "---"
	fm, body, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(fm, Frontmatter{}) {
		t.Errorf("fm should be zero")
	}
	if body != in {
		t.Errorf("body = %q, want input", body)
	}
}

// =====================================================================
// Parse — 各种字段缺失场景
// =====================================================================

func TestParse_OnlyAllowedTools(t *testing.T) {
	in := "---\nallowed_tools:\n  - a\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "" {
		t.Errorf("Name should be empty, got %q", fm.Name)
	}
	if !reflect.DeepEqual(fm.AllowedTools, []string{"a"}) {
		t.Errorf("AllowedTools = %v", fm.AllowedTools)
	}
}

func TestParse_OnlyWhenToUse(t *testing.T) {
	in := "---\nwhen_to_use: trigger\n---\n"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.WhenToUse != "trigger" {
		t.Errorf("WhenToUse = %q", fm.WhenToUse)
	}
}

func TestParse_UnknownField_Ignored(t *testing.T) {
	// yaml.v3 默认忽略未定义字段
	in := "---\nname: X\nunknown_field: abc\n---\nbody"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" {
		t.Errorf("Name = %q", fm.Name)
	}
}

// =====================================================================
// Parse — 多行字符串（YAML 块 scalar）
// =====================================================================

func TestParse_MultilineDescription_Literal(t *testing.T) {
	in := "---\nname: X\ndescription: |\n  line1\n  line2\n---\nbody"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Description != "line1\nline2\n" {
		t.Errorf("Description = %q", fm.Description)
	}
}

func TestParse_MultilineDescription_Folded(t *testing.T) {
	in := "---\nname: X\ndescription: >\n  line1\n  line2\n---\nbody"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// folded：换行变空格
	if !strings.Contains(fm.Description, "line1 line2") {
		t.Errorf("Description = %q", fm.Description)
	}
}

// =====================================================================
// Parse — frontmatter 内含空行
// =====================================================================

func TestParse_FrontmatterWithEmptyLines(t *testing.T) {
	in := "---\nname: X\n\ndescription: Y\n\n---\nbody"
	fm, _, err := Parse(in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fm.Name != "X" || fm.Description != "Y" {
		t.Errorf("fm = %+v", fm)
	}
}

// =====================================================================
// Serialize — 基础
// =====================================================================

func TestSerialize_Empty(t *testing.T) {
	out, err := Serialize(Frontmatter{}, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// 至少应有 `---\n` + yaml 输出 + `---\n`
	if !strings.HasPrefix(out, "---\n") {
		t.Errorf("output should start with ---\\n, got %q", out)
	}
	if !strings.Contains(out, "\n---\n") {
		t.Errorf("output should contain closing ---\\n, got %q", out)
	}
}

func TestSerialize_NameOnly(t *testing.T) {
	out, err := Serialize(Frontmatter{Name: "X"}, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "name: X") {
		t.Errorf("output should contain name: X, got %q", out)
	}
}

func TestSerialize_AllFields(t *testing.T) {
	fm := Frontmatter{
		Name:         "测试助手",
		Description:  "描述",
		WhenToUse:    "触发条件",
		AllowedTools: []string{"a", "b"},
	}
	body := "# Body\nContent"
	out, err := Serialize(fm, body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "name: 测试助手") {
		t.Errorf("missing name: %q", out)
	}
	if !strings.Contains(out, "description: 描述") {
		t.Errorf("missing description: %q", out)
	}
	if !strings.Contains(out, "when_to_use:") {
		t.Errorf("missing when_to_use: %q", out)
	}
	if !strings.Contains(out, "allowed_tools:") {
		t.Errorf("missing allowed_tools: %q", out)
	}
	if !strings.HasSuffix(out, body) {
		t.Errorf("output should end with body, got %q", out[len(out)-50:])
	}
}

func TestSerialize_OmitEmpty(t *testing.T) {
	// Description / WhenToUse / AllowedTools 为空时应省略（omitempty）
	out, err := Serialize(Frontmatter{Name: "X"}, "body")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, "description:") {
		t.Errorf("description should be omitted, got %q", out)
	}
	if strings.Contains(out, "when_to_use:") {
		t.Errorf("when_to_use should be omitted")
	}
	if strings.Contains(out, "allowed_tools:") {
		t.Errorf("allowed_tools should be omitted")
	}
}

func TestSerialize_NameAlwaysPresent(t *testing.T) {
	// Name 无 omitempty，空 name 也应保留 key
	out, err := Serialize(Frontmatter{}, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "name:") {
		t.Errorf("name key should always present, got %q", out)
	}
}

func TestSerialize_BodyEmpty(t *testing.T) {
	fm := Frontmatter{Name: "X"}
	out, err := Serialize(fm, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// 应有 frontmatter 终止符
	if !strings.Contains(out, "---\nname: X\n---\n") {
		t.Errorf("output structure wrong: %q", out)
	}
}

// =====================================================================
// Serialize — round-trip 不变性
// =====================================================================

func TestRoundTrip_NameOnly(t *testing.T) {
	fm := Frontmatter{Name: "X"}
	out, err := Serialize(fm, "body")
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	gotFm, gotBody, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !reflect.DeepEqual(gotFm, fm) {
		t.Errorf("fm round-trip: got %+v, want %+v", gotFm, fm)
	}
	if gotBody != "body" {
		t.Errorf("body round-trip: got %q", gotBody)
	}
}

func TestRoundTrip_AllFields(t *testing.T) {
	fm := Frontmatter{
		Name:         "销售助手",
		Description:  "分析销售数据",
		WhenToUse:    "上传 CSV 时",
		AllowedTools: []string{"web_search", "bash_exec"},
	}
	body := "# Body\n你是一名分析师。"
	out, err := Serialize(fm, body)
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	gotFm, gotBody, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !reflect.DeepEqual(gotFm, fm) {
		t.Errorf("fm: got %+v, want %+v", gotFm, fm)
	}
	if gotBody != body {
		t.Errorf("body: got %q, want %q", gotBody, body)
	}
}

func TestRoundTrip_EmptyBody(t *testing.T) {
	fm := Frontmatter{Name: "X", Description: "Y"}
	out, err := Serialize(fm, "")
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	gotFm, gotBody, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !reflect.DeepEqual(gotFm, fm) {
		t.Errorf("fm: got %+v, want %+v", gotFm, fm)
	}
	if gotBody != "" {
		t.Errorf("body should be empty, got %q", gotBody)
	}
}

func TestRoundTrip_BodyWithRuler(t *testing.T) {
	// 关键 case：body 含 `---`，round-trip 不应丢失
	fm := Frontmatter{Name: "X"}
	body := "# Heading\n\n---\n\nAfter ruler.\n"
	out, err := Serialize(fm, body)
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	_, gotBody, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if gotBody != body {
		t.Errorf("body round-trip with ruler: got %q, want %q", gotBody, body)
	}
}

func TestRoundTrip_BodyOnlyRulers(t *testing.T) {
	fm := Frontmatter{Name: "X"}
	body := "---\n---\n---\n"
	out, err := Serialize(fm, body)
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	_, gotBody, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if gotBody != body {
		t.Errorf("body: got %q, want %q", gotBody, body)
	}
}

func TestRoundTrip_Unicode(t *testing.T) {
	fm := Frontmatter{
		Name:        "助手 🤖",
		Description: "中文 + emoji 🎉",
	}
	body := "你好 World 👋"
	out, err := Serialize(fm, body)
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	gotFm, gotBody, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !reflect.DeepEqual(gotFm, fm) {
		t.Errorf("fm: got %+v, want %+v", gotFm, fm)
	}
	if gotBody != body {
		t.Errorf("body: got %q, want %q", gotBody, body)
	}
}

func TestRoundTrip_LongBody(t *testing.T) {
	fm := Frontmatter{Name: "X"}
	body := strings.Repeat("Lorem ipsum dolor sit amet.\n", 1000) // ~28KB
	out, err := Serialize(fm, body)
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	_, gotBody, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if gotBody != body {
		t.Errorf("long body round-trip failed: lengths %d vs %d", len(gotBody), len(body))
	}
}

func TestRoundTrip_SpecialCharsInFields(t *testing.T) {
	fm := Frontmatter{
		Name:        "name: with colon",
		Description: "desc with \"quote\" and 'apostrophe'",
		WhenToUse:   "use\nwith\nnewlines",
	}
	out, err := Serialize(fm, "body")
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	gotFm, _, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v: out=%q", err, out)
	}
	if !reflect.DeepEqual(gotFm, fm) {
		t.Errorf("fm: got %+v, want %+v", gotFm, fm)
	}
}

func TestRoundTrip_ManyAllowedTools(t *testing.T) {
	fm := Frontmatter{
		Name:         "X",
		AllowedTools: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"},
	}
	out, err := Serialize(fm, "")
	if err != nil {
		t.Fatalf("Serialize err = %v", err)
	}
	gotFm, _, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse err = %v", err)
	}
	if !reflect.DeepEqual(gotFm.AllowedTools, fm.AllowedTools) {
		t.Errorf("AllowedTools: got %v, want %v", gotFm.AllowedTools, fm.AllowedTools)
	}
}

// =====================================================================
// Table-driven: 大批简单 case 集中跑
// =====================================================================

func TestParse_TableDriven_NoFrontmatter(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"plain", "hello world"},
		{"with newline", "hello\nworld"},
		{"heading", "# H"},
		{"list", "- item"},
		{"numbered", "1. item"},
		{"link", "[a](b)"},
		{"code inline", "`code`"},
		{"table", "| a | b |"},
		{"blockquote", "> quote"},
		{"two dashes", "--\nname: X\n--"},
		{"four dashes", "----\nname: X\n----"},
		{"dashes with content", "--- inline\nname: X\n---"},
		{"hash before", "#---\n"},
		{"space then dash", " - item"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fm, body, err := Parse(c.in)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(fm, Frontmatter{}) {
				t.Errorf("fm should be zero, got %+v", fm)
			}
			if body != c.in {
				t.Errorf("body should equal input")
			}
		})
	}
}

func TestParse_TableDriven_ValidFrontmatter(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantName string
	}{
		{"simple", "---\nname: A\n---\n", "A"},
		{"with body", "---\nname: B\n---\nbody", "B"},
		{"no closing newline", "---\nname: C\n---", "C"},
		{"with trailing spaces", "---\nname: D\n---  \n", "D"},
		{"with leading spaces", "  ---\nname: E\n---\n", "E"},
		{"quoted name", "---\nname: \"F\"\n---\n", "F"},
		{"single quoted", "---\nname: 'G'\n---\n", "G"},
		{"empty body line", "---\nname: H\n---\n\n", "H"},
		{"crlf", "---\r\nname: I\r\n---\r\n", "I"},
		{"chinese", "---\nname: 助手J\n---\n", "助手J"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fm, _, err := Parse(c.in)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if fm.Name != c.wantName {
				t.Errorf("Name = %q, want %q", fm.Name, c.wantName)
			}
		})
	}
}

func TestParse_TableDriven_InvalidYAML(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"tab indent", "---\nname: X\n\tbad: Y\n---\n"},
		{"unclosed quote", "---\nname: \"unclosed\n---\n"},
		{"key empty body weird", "---\n  : v\n---\n"},
		{"bad type for list", "---\nallowed_tools: bare-string\n---\n"},
		{"bad type for list nested", "---\nallowed_tools:\n  key: value\n---\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Parse(c.in)
			if err == nil {
				t.Fatalf("expected error for %q", c.in)
			}
			if !errors.Is(err, errno.ErrSkillArtifactFrontmatterInvalid) {
				t.Errorf("err should be ErrSkillArtifactFrontmatterInvalid, got %v", err)
			}
		})
	}
}

func TestParse_TableDriven_BodyPreservation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"simple", "hello"},
		{"newline", "line1\nline2"},
		{"trailing newline", "x\n"},
		{"multiple newlines", "\n\n\n"},
		{"with ruler", "before\n---\nafter"},
		{"only ruler", "---"},
		{"only ruler with newlines", "---\n"},
		{"heading", "# h\n## h2"},
		{"code block", "```\ncode\n```"},
		{"chinese", "你好世界"},
		{"emoji", "🎉🚀🤖"},
		{"long line", strings.Repeat("a", 1000)},
		{"many newlines", strings.Repeat("\n", 100)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := "---\nname: X\n---\n" + c.body
			_, body, err := Parse(in)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if body != c.body {
				t.Errorf("body: got %q, want %q", body, c.body)
			}
		})
	}
}

// =====================================================================
// Fuzz target — Parse(Serialize(Parse(x))) consistency
// =====================================================================

// FuzzRoundTrip 验证 Parse → Serialize → Parse 的不变性。
//
// 不变量：对任意输入 raw，若 Parse(raw) 成功得到 (fm, body)，则 Serialize(fm, body) 应该再次
// 可被 Parse 解析回相同的 (fm, body)。即 Parse 是 Serialize 的左逆。
//
// 不直接验证 Serialize(Parse(raw)) == raw（因为合法输入可有多种等价表示，如 quoted vs
// unquoted），只验证语义等价。
func FuzzRoundTrip(f *testing.F) {
	// Seed corpus
	f.Add("")
	f.Add("---\nname: X\n---\nbody")
	f.Add("---\nname: 助手\ndescription: 测试\n---\n# Heading\n")
	f.Add("plain text no frontmatter")
	f.Add("---\nname: X\nallowed_tools:\n  - a\n  - b\n---\n")
	f.Add("---\n---\n")
	f.Add("# heading\n\nbody only\n")
	f.Add("---\nname: X\n---\nbody with --- ruler\nmore")

	f.Fuzz(func(t *testing.T, raw string) {
		fm, body, err := Parse(raw)
		if err != nil {
			// Parse 失败不进一步验证（输入 YAML 不合法）
			return
		}

		// 第一次 round-trip
		out, err := Serialize(fm, body)
		if err != nil {
			t.Fatalf("Serialize failed on valid Parse output: fm=%+v body=%q err=%v", fm, body, err)
		}

		// 第二次 Parse
		fm2, body2, err := Parse(out)
		if err != nil {
			t.Fatalf("Re-Parse of Serialize output failed: out=%q err=%v", out, err)
		}

		// 语义等价：Frontmatter 内容应严格相等
		if !reflect.DeepEqual(normalizeFm(fm), normalizeFm(fm2)) {
			t.Errorf("fm mismatch after round-trip:\n  raw=%q\n  fm1=%+v\n  out=%q\n  fm2=%+v",
				raw, fm, out, fm2)
		}

		// Body 内容应严格相等
		if body != body2 {
			t.Errorf("body mismatch after round-trip:\n  raw=%q\n  body1=%q\n  out=%q\n  body2=%q",
				raw, body, out, body2)
		}
	})
}

// normalizeFm 把 AllowedTools 的 nil 和 []string{} 视为等价（YAML omitempty 行为可能两种都出）。
func normalizeFm(fm Frontmatter) Frontmatter {
	if fm.AllowedTools == nil {
		fm.AllowedTools = []string{}
	}
	return fm
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
