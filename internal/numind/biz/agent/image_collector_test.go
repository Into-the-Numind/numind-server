package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression: the streaming path embeds images in consumeEinoStream AND then
// finalizeRun runs — both must not append the same image markdown. markdown() is
// idempotent (safe to read twice); drainMarkdown() clears so the second embed is
// a no-op, preventing the persisted final answer from showing the image twice.
func TestImageCollector_DrainClearsMarkdown(t *testing.T) {
	ctx := withImageCollector(context.Background())
	c := imageCollectorFrom(ctx)
	require.NotNil(t, c)
	c.add("https://cos/x.png", "x.png")

	md := c.markdown()
	assert.Contains(t, md, "![x.png](https://cos/x.png)")
	assert.Equal(t, md, c.markdown(), "markdown() must be idempotent (no clear)")

	assert.Equal(t, md, c.drainMarkdown(), "drainMarkdown returns the markdown")
	assert.Equal(t, "", c.drainMarkdown(), "drainMarkdown clears → empty on 2nd call")
	assert.Equal(t, "", c.markdown(), "markdown is empty after drain")
}

func TestImageCollector_NilSafe(t *testing.T) {
	var c *imageCollector
	c.add("u", "f")
	assert.Equal(t, "", c.markdown())
	assert.Equal(t, "", c.drainMarkdown())
}
