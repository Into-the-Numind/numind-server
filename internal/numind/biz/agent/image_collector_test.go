package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression: the streaming path embeds images in consumeEinoStream AND then
// finalizeRun runs — both must not append the same image markdown. markdown() is
// idempotent (safe to read twice); drainMarkdownExcluding clears so the second
// embed is a no-op, preventing the persisted final answer from showing it twice.
func TestImageCollector_DrainClearsMarkdown(t *testing.T) {
	ctx := withImageCollector(context.Background())
	c := imageCollectorFrom(ctx)
	require.NotNil(t, c)
	c.add("https://cos/x.png", "x.png")

	md := c.markdown()
	assert.Contains(t, md, "![x.png](https://cos/x.png)")
	assert.Equal(t, md, c.markdown(), "markdown() must be idempotent (no clear)")

	// Empty content excludes nothing → returns all, then clears.
	assert.Equal(t, md, c.drainMarkdownExcluding(""), "drain returns the markdown")
	assert.Equal(t, "", c.drainMarkdownExcluding(""), "drain clears → empty on 2nd call")
	assert.Equal(t, "", c.markdown(), "markdown is empty after drain")
}

// TestImageCollector_DrainMarkdownExcluding_SkipsModelEmbedded reproduces the
// customer-reported duplicate (dev 2026-06-18): when the LLM writes the image
// markdown in its OWN final answer AND the finalizer appends the collected image,
// the persisted answer renders the same image twice. drainMarkdownExcluding(content)
// must skip any collected image whose object key is already present in content.
func TestImageCollector_DrainMarkdownExcluding_SkipsModelEmbedded(t *testing.T) {
	const url = "https://cos.example.com/agent-outputs/1/img-20260618.png?q-sign-time=123&sig=abc"
	ctx := withImageCollector(context.Background())
	c := imageCollectorFrom(ctx)
	require.NotNil(t, c)
	c.add(url, "img.png")

	// The model already embedded this exact image in its answer.
	content := "给，这是你要的图：\n\n![橘猫](" + url + ")"
	assert.Equal(t, "", c.drainMarkdownExcluding(content),
		"an image the model already embedded must NOT be appended again (would render twice)")
}

// TestImageCollector_DrainMarkdownExcluding_SameKeyDifferentSignature: the model
// may reference the same image via a re-signed URL (same COS object key, different
// query/signature). That is still the same image and must NOT duplicate.
func TestImageCollector_DrainMarkdownExcluding_SameKeyDifferentSignature(t *testing.T) {
	const collected = "https://cos.example.com/agent-outputs/1/img-20260618.png?q-sign-time=123&sig=AAA"
	const embedded = "https://cos.example.com/agent-outputs/1/img-20260618.png?q-sign-time=999&sig=ZZZ"
	ctx := withImageCollector(context.Background())
	c := imageCollectorFrom(ctx)
	c.add(collected, "img.png")

	assert.Equal(t, "", c.drainMarkdownExcluding("![x]("+embedded+")"),
		"same object key with a different signature is the same image — must not duplicate")
}

// TestImageCollector_DrainMarkdownExcluding_KeepsUnreferenced verifies an image the
// model did NOT embed is still appended (the persistence guarantee the collector
// exists for — dev 2026-06-08).
func TestImageCollector_DrainMarkdownExcluding_KeepsUnreferenced(t *testing.T) {
	const url = "https://cos.example.com/agent-outputs/1/img-20260618.png?sig=abc"
	ctx := withImageCollector(context.Background())
	c := imageCollectorFrom(ctx)
	c.add(url, "img.png")

	got := c.drainMarkdownExcluding("纯文字答案，没有图片。")
	assert.Contains(t, got, "![img.png]("+url+")",
		"an image the model did NOT embed must still be appended so it persists on reload")
	assert.Equal(t, "", c.drainMarkdownExcluding(""), "drains → empty on 2nd call")
}

// TestImageCollector_DrainMarkdownExcluding_MixedBatch: with two distinct images,
// one already embedded by the model and one not, only the un-embedded one is kept.
func TestImageCollector_DrainMarkdownExcluding_MixedBatch(t *testing.T) {
	const embedded = "https://cos.example.com/agent-outputs/1/a.png?sig=1"
	const fresh = "https://cos.example.com/agent-outputs/1/b.png?sig=2"
	ctx := withImageCollector(context.Background())
	c := imageCollectorFrom(ctx)
	c.add(embedded, "a.png")
	c.add(fresh, "b.png")

	got := c.drainMarkdownExcluding("model wrote ![a](" + embedded + ") and nothing else")
	assert.NotContains(t, got, "a.png", "the model-embedded image must be excluded")
	assert.Contains(t, got, "![b.png]("+fresh+")", "the un-embedded image must be kept")
}

func TestImageCollector_NilSafe(t *testing.T) {
	var c *imageCollector
	c.add("u", "f")
	assert.Equal(t, "", c.markdown())
	assert.Equal(t, "", c.drainMarkdownExcluding("anything"))
}
