package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// imageCollector accumulates tool-generated images (e.g. image_gen) during a run
// so the run finalizer can embed them as markdown in the PERSISTED final answer.
// A generated image delivered only as a transient SSE artifact event is lost on
// reload (loadSessionSnapshot rebuilds the conversation from agent_run.messages,
// which never stored the artifact) — User-reported, dev 2026-06-08.
//
// It lives on the run context so BOTH the streaming (RunStream/consumeEinoStream)
// and non-streaming (Run, e.g. ask_user_question resume) paths share one
// mechanism, independent of StreamSessionState (whose presence also drives
// streaming-only yield handling).
type imageCollector struct {
	mu   sync.Mutex
	seen map[string]struct{}
	imgs []string
}

type imageCollectorCtxKey struct{}

// withImageCollector attaches a fresh image collector to ctx.
func withImageCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, imageCollectorCtxKey{}, &imageCollector{seen: make(map[string]struct{})})
}

// imageCollectorFrom returns the collector attached to ctx, or nil. All methods
// are nil-safe so callers need not check.
func imageCollectorFrom(ctx context.Context) *imageCollector {
	c, _ := ctx.Value(imageCollectorCtxKey{}).(*imageCollector)
	return c
}

// add records a tool-generated image as a markdown snippet, deduped by URL.
// Safe for concurrent calls and on a nil receiver.
func (c *imageCollector) add(url, filename string) {
	if c == nil || url == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[url]; ok {
		return
	}
	c.seen[url] = struct{}{}
	alt := filename
	if alt == "" {
		alt = "生成的图片"
	}
	c.imgs = append(c.imgs, fmt.Sprintf("![%s](%s)", alt, url))
}

// markdown returns the collected image snippets joined by blank lines, or "" when
// none. Safe on a nil receiver.
func (c *imageCollector) markdown() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.imgs) == 0 {
		return ""
	}
	return strings.Join(c.imgs, "\n\n")
}

// drainMarkdown returns the collected image snippets AND clears them, so a
// subsequent call returns "". Use this when embedding images into the persisted
// final answer: the streaming path embeds in consumeEinoStream and then
// finalizeRun runs too — without draining, both append the same markdown and the
// image is persisted twice. Safe on a nil receiver.
func (c *imageCollector) drainMarkdown() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.imgs) == 0 {
		return ""
	}
	md := strings.Join(c.imgs, "\n\n")
	c.imgs = nil
	return md
}
