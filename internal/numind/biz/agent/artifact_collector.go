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
//   - Documents/HTML: if the model already presented the doc as a STANDALONE-cardable
//     line the frontend cards it → skip. Otherwise append a standalone [name](url) line
//     so every generated file is reliably a card. The model's links are NEVER stripped:
//     stripping a markdown table cell left it empty and detached the card from its row
//     (问题五 + dev 2026-06-18 followup).
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
			// Never strip the model's links — stripping a table cell empties it and the
			// card floats away from its row (dev 2026-06-18 followup). If the doc is
			// already a standalone-cardable line the frontend cards it → skip; else append
			// a standalone card line. Any in-table/inline link stays as readable context.
			if hasStandaloneCardableLine(content, e.objectKey) {
				continue
			}
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

// listOrQuoteLineRE mirrors the frontend agentArtifacts LIST_OR_QUOTE_RE: a line that
// starts a list item / ordered item / blockquote. The frontend never lifts a COS link
// inside one (or inside a table row) into a card, so neither do we.
var listOrQuoteLineRE = regexp.MustCompile(`^\s*(?:[-*+]\s|\d+[.)]\s|>\s?)`)

// hasStandaloneCardableLine reports whether content already has a line the frontend's
// standaloneArtifactOf would lift into a file card for objectKey: a markdown link/image
// node referencing objectKey, alone on its line (nothing meaningful after the node),
// and NOT inside a table row (pipe), list, or blockquote. Mirrors the frontend so the
// backend appends a card ONLY when the frontend wouldn't already render one — avoiding a
// duplicate card and never stripping/breaking the model's tables (dev 2026-06-18). The
// URL must END at objectKey (next char `?`/`#`/whitespace/`)`) so a prefix like "…/data"
// does not match a different file "…/data.csv".
func hasStandaloneCardableLine(content, objectKey string) bool {
	if objectKey == "" || content == "" {
		return false
	}
	nodeRe := regexp.MustCompile(`!?\[[^\]]*\]\(\s*` + regexp.QuoteMeta(objectKey) + `(?:[?#][^)]*)?\s*\)`)
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "|") || listOrQuoteLineRE.MatchString(line) {
			continue
		}
		loc := nodeRe.FindStringIndex(line)
		if loc == nil {
			continue
		}
		if strings.TrimSpace(line[loc[1]:]) == "" { // nothing meaningful after the node
			return true
		}
	}
	return false
}
