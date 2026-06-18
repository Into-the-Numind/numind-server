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
	imgs []imageEntry
}

// imageEntry pairs an image's URL with its rendered markdown snippet, so the
// collector can both emit the markdown and match the image against the model's own
// answer text by its (signature-independent) object key.
type imageEntry struct {
	url string
	md  string
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
	c.imgs = append(c.imgs, imageEntry{url: url, md: fmt.Sprintf("![%s](%s)", alt, url)})
}

// markdown returns the collected image snippets joined by blank lines, or "" when
// none. Safe on a nil receiver.
func (c *imageCollector) markdown() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return joinEntries(c.imgs)
}

// drainMarkdownExcluding returns the collected image markdown EXCLUDING any image
// the model already embedded in `content`, and clears the collector. This fixes the
// duplicate-render bug (dev 2026-06-18): the LLM wrote ![](url) in its own final
// answer AND the finalizer appended the same image, rendering it twice. An image is
// considered "already embedded" when content contains its object key (URL minus the
// signed query string), so a model-rendered re-signed URL still matches. Images the
// model did NOT reference are still returned, preserving the persistence guarantee
// the collector exists for. Safe on a nil receiver.
func (c *imageCollector) drainMarkdownExcluding(content string) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := make([]imageEntry, 0, len(c.imgs))
	for _, e := range c.imgs {
		if e.url != "" && strings.Contains(content, imageObjectKey(e.url)) {
			continue // model already embedded this image in its answer
		}
		kept = append(kept, e)
	}
	c.imgs = nil
	return joinEntries(kept)
}

// joinEntries renders image entries to markdown joined by blank lines, "" when none.
func joinEntries(entries []imageEntry) string {
	if len(entries) == 0 {
		return ""
	}
	mds := make([]string, len(entries))
	for i, e := range entries {
		mds[i] = e.md
	}
	return strings.Join(mds, "\n\n")
}

// imageObjectKey returns the signature-independent part of an image URL — scheme +
// host + path, without the signed query string — so an image embedded via a
// re-signed URL (same COS object, different ?q-sign-time/signature) still matches.
func imageObjectKey(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}
