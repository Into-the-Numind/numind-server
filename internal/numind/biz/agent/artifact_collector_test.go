package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cosImg = "https://cos.example.com/agent-outputs/1/img-20260618.png?q-sign-time=1&sig=a"
const cosDocx = "https://cos.example.com/agent-outputs/1/20260618-报告.docx?q-sign-time=1&sig=b"
const cosHTML = "https://cos.example.com/agent-outputs/1/20260618-页面.html?q-sign-time=1&sig=c"

// ── Image regression (carried over from the old imageCollector, must keep passing) ──

func TestArtifactCollector_NilSafe(t *testing.T) {
	var c *artifactCollector
	c.add("u", "f", "image/png")
	assert.Equal(t, "原文", c.finalizeInto("原文"))
}

// An image the model did NOT embed must still be appended (persistence guarantee).
func TestArtifactCollector_FinalizeInto_KeepsUnreferencedImage(t *testing.T) {
	ctx := withArtifactCollector(context.Background())
	c := artifactCollectorFrom(ctx)
	require.NotNil(t, c)
	c.add(cosImg, "img.png", "image/png")

	got := c.finalizeInto("纯文字答案，没有图片。")
	assert.Contains(t, got, "![img.png]("+cosImg+")", "un-embedded image must be appended")
	assert.Equal(t, "纯文字答案，没有图片。", c.finalizeInto("纯文字答案，没有图片。"), "drained → no-op 2nd call")
}

// An image the model already embedded (same object key, any signature) must NOT
// duplicate.
func TestArtifactCollector_FinalizeInto_SkipsModelEmbeddedImage(t *testing.T) {
	ctx := withArtifactCollector(context.Background())
	c := artifactCollectorFrom(ctx)
	c.add(cosImg, "img.png", "image/png")
	// model embedded the same object key via a different signature
	content := "看图：\n\n![猫](https://cos.example.com/agent-outputs/1/img-20260618.png?q-sign-time=9&sig=Z)"
	got := c.finalizeInto(content)
	assert.Equal(t, content, got, "already-embedded image must not be appended again")
}

// cosIsInlineRenderName: images + html render inline (signImage); docs download.
func TestCosIsInlineRenderName(t *testing.T) {
	assert.True(t, cosIsInlineRenderName("page.html"), "html → inline (iframe preview)")
	assert.True(t, cosIsInlineRenderName("PAGE.HTM"), "htm case-insensitive → inline")
	assert.True(t, cosIsInlineRenderName("img.png"), "image → inline")
	assert.False(t, cosIsInlineRenderName("报告.docx"), "docx → download")
	assert.False(t, cosIsInlineRenderName("data.pdf"), "pdf → download")
	assert.False(t, cosIsInlineRenderName("noext"), "no extension → download")
}

// ── Document / HTML embedding (问题五) ──

// TestArtifactCollector_FinalizeInto_EmbedsDocAsStandaloneLink reproduces 问题五: a
// generated docx must be embedded into the final answer as a STANDALONE [name](url)
// line so the frontend renders it as a file card. Pre-fix finalizeInto drops docs.
func TestArtifactCollector_FinalizeInto_EmbedsDocAsStandaloneLink(t *testing.T) {
	ctx := withArtifactCollector(context.Background())
	c := artifactCollectorFrom(ctx)
	c.add(cosDocx, "报告.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")

	got := c.finalizeInto("我已生成报告。")
	assert.Contains(t, got, "[报告.docx]("+cosDocx+")", "docx must embed as a standalone link → card")
	assert.NotContains(t, got, "![报告.docx]", "a doc must NOT use image syntax")
	// the link must be on its own line (standalone) so the frontend lifts it to a card
	var standalone bool
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "[报告.docx]("+cosDocx+")" {
			standalone = true
		}
	}
	assert.True(t, standalone, "doc link must be on its own line")
}

// HTML is also a document-class artifact → standalone card link.
func TestArtifactCollector_FinalizeInto_EmbedsHTML(t *testing.T) {
	ctx := withArtifactCollector(context.Background())
	c := artifactCollectorFrom(ctx)
	c.add(cosHTML, "页面.html", "text/html; charset=utf-8")
	got := c.finalizeInto("页面做好了。")
	assert.Contains(t, got, "[页面.html]("+cosHTML+")", "html must embed as a standalone link → card")
}

// If the model wrote the doc link INLINE (mixed with prose), the inline node must be
// stripped and the doc appended standalone — exactly one card, no naked inline link.
func TestArtifactCollector_FinalizeInto_StripsInlineDocLink(t *testing.T) {
	ctx := withArtifactCollector(context.Background())
	c := artifactCollectorFrom(ctx)
	c.add(cosDocx, "报告.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")

	// model wrote it inline with trailing prose → would NOT card on the frontend
	content := "文件已生成，点击[下载报告](" + cosDocx + ")，记得查收。"
	got := c.finalizeInto(content)
	// the inline node is removed
	assert.NotContains(t, got, "[下载报告]("+cosDocx+")", "inline doc node must be stripped")
	// and re-appended as a standalone line
	assert.Contains(t, got, "[报告.docx]("+cosDocx+")", "doc must be re-appended standalone")
	// surrounding prose survives
	assert.Contains(t, got, "文件已生成")
	assert.Contains(t, got, "记得查收")
	// exactly one occurrence of the docx URL remains (one card, no duplicate)
	assert.Equal(t, 1, strings.Count(got, cosDocx), "exactly one reference → one card")
}

// Regression (review P1): stripNodesReferencing(content, keyA) must strip ONLY the node
// whose URL IS keyA (+ optional query), NOT a different file whose URL has keyA as a
// string prefix (e.g. "…-data" vs "…-data.csv" generated in the same second). The old
// `[^)]*<key>[^)]*` pattern collaterally stripped the longer one.
func TestStripNodesReferencing_NoPrefixCollision(t *testing.T) {
	const keyA = "https://cos.example.com/agent-outputs/1/20260618-150405-data"
	content := "[A](" + keyA + "?sig=1) 和 [B](" + keyA + ".csv?sig=2)"
	got := stripNodesReferencing(content, keyA)
	assert.NotContains(t, got, "[A]("+keyA+"?sig=1)", "the node whose URL IS keyA must be stripped")
	assert.Contains(t, got, "[B]("+keyA+".csv?sig=2)",
		"a different file sharing keyA as a prefix must NOT be collaterally stripped")
}
