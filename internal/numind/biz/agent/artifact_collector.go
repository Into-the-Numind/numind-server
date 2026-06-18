package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// artifactCollector accumulates tool-generated files during a run so the run
// finalizer can embed them in the PERSISTED final answer markdown. A generated file
// delivered only as a transient SSE artifact event is lost on reload
// (loadSessionSnapshot rebuilds the conversation from agent_run.messages, which never
// stored the artifact). User-reported: images dev 2026-06-08; documents/HTML
// (agent-output-ux-fixes 问题五) dev 2026-06-18.
//
// Rendering by kind:
//   - image/*       → ![alt](url)            (renders inline; placement preserved)
//   - everything else → [filename](url) ON ITS OWN LINE, so the frontend
//     (splitIntoSegments / standaloneArtifactOf) lifts it into a file card
//     (download + preview) instead of a buried naked link.
//
// It lives on the run context so BOTH the streaming (RunStream/consumeEinoStream)
// and non-streaming (Run, e.g. ask_user_question resume) paths share one mechanism.
type artifactCollector struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	entries []artifactEntry
}

// artifactKind distinguishes how an entry embeds into the final answer.
type artifactKind int

const (
	artifactImage artifactKind = iota
	artifactDoc
)

// artifactEntry pairs an artifact's URL with its rendered markdown snippet and a
// signature-independent object key (URL minus query) used to dedup against / strip
// from the model's own answer text.
type artifactEntry struct {
	url       string
	objectKey string
	kind      artifactKind
	md        string
}

type artifactCollectorCtxKey struct{}

// withArtifactCollector attaches a fresh artifact collector to ctx.
func withArtifactCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, artifactCollectorCtxKey{}, &artifactCollector{seen: make(map[string]struct{})})
}

// artifactCollectorFrom returns the collector attached to ctx, or nil. All methods
// are nil-safe so callers need not check.
func artifactCollectorFrom(ctx context.Context) *artifactCollector {
	c, _ := ctx.Value(artifactCollectorCtxKey{}).(*artifactCollector)
	return c
}

// add records a tool-generated artifact, deduped by URL. mime decides rendering:
// image/* → inline image markdown; anything else → a standalone download link the
// frontend turns into a file card. Safe for concurrent calls and on a nil receiver.
func (c *artifactCollector) add(url, filename, mime string) {
	if c == nil || url == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[url]; ok {
		return
	}
	c.seen[url] = struct{}{}
	if strings.HasPrefix(mime, "image/") {
		alt := filename
		if alt == "" {
			alt = "生成的图片"
		}
		c.entries = append(c.entries, artifactEntry{
			url: url, objectKey: artifactObjectKey(url), kind: artifactImage,
			md: fmt.Sprintf("![%s](%s)", alt, url),
		})
		return
	}
	name := filename
	if name == "" {
		name = "生成的文件"
	}
	c.entries = append(c.entries, artifactEntry{
		url: url, objectKey: artifactObjectKey(url), kind: artifactDoc,
		md: fmt.Sprintf("[%s](%s)", name, url),
	})
}

// finalizeInto returns `content` with all collected artifacts embedded, and clears
// the collector. Single finalizer used by BOTH embed sites (streaming runner_stream
// + non-streaming finalizeRun) — drain-once semantics (whichever runs first embeds;
// the second sees an empty collector and is a no-op). Safe on a nil receiver.
//
//   - Images: appended as ![alt](url) UNLESS the model already embedded the same
//     object key in content (no double render — dev 2026-06-18). Placement preserved.
//   - Documents/HTML: any inline markdown node the model wrote referencing the doc's
//     object key is STRIPPED from content, then the doc is appended as a STANDALONE
//     [name](url) line so the frontend lifts it into a file card — exactly one card,
//     no buried naked link, no duplicate (问题五).
func (c *artifactCollector) finalizeInto(content string) string {
	if c == nil {
		return content
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var imgMD, docMD []string
	for _, e := range c.entries {
		switch e.kind {
		case artifactImage:
			if e.objectKey != "" && strings.Contains(content, e.objectKey) {
				continue // model already embedded this image in its answer
			}
			imgMD = append(imgMD, e.md)
		case artifactDoc:
			// Drop any inline node the model wrote for this doc (it would otherwise
			// render as a naked link beside the card); re-append standalone below.
			content = stripNodesReferencing(content, e.objectKey)
			docMD = append(docMD, e.md)
		}
	}
	c.entries = nil
	c.seen = make(map[string]struct{})

	content = appendBlock(content, imgMD)
	content = appendBlock(content, docMD)
	return content
}

// appendBlock joins block by blank lines and appends it to content (blank-line
// separated), or returns content unchanged when block is empty.
func appendBlock(content string, block []string) string {
	if len(block) == 0 {
		return content
	}
	joined := strings.Join(block, "\n\n")
	if strings.TrimSpace(content) == "" {
		return joined
	}
	return content + "\n\n" + joined
}

// artifactObjectKey returns the signature-independent part of a URL — everything
// before the query string — so an artifact embedded via a re-signed URL (same COS
// object, different ?q-sign-time/signature) still matches.
func artifactObjectKey(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// stripNodesReferencing removes every markdown image/link node whose URL contains
// objectKey from content (used so a model-written inline doc link does not survive
// as a naked link beside the standalone card). Best-effort: empty objectKey → no-op.
func stripNodesReferencing(content, objectKey string) string {
	if objectKey == "" || content == "" {
		return content
	}
	re := regexp.MustCompile(`!?\[[^\]]*\]\([^)]*` + regexp.QuoteMeta(objectKey) + `[^)]*\)`)
	return re.ReplaceAllString(content, "")
}
