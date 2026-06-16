package agent

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"numind-server/internal/pkg/util"
)

// cosResignExpirySeconds is the validity window for a re-signed read-path URL.
const cosResignExpirySeconds = 24 * 60 * 60

// cosSigner abstracts the two COS presign flavours so resignCOSLinksWithHost is
// unit-testable without a live COS client.
type cosSigner struct {
	// signImage presigns an inline GET (no attachment) so <img> still renders.
	signImage func(ctx context.Context, objectKey string, expirySeconds int64) (string, error)
	// signDownload presigns a GET with Content-Disposition: attachment.
	signDownload func(ctx context.Context, objectKey, filename string, expirySeconds int64) (string, error)
}

var cosImageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".svg": true, ".bmp": true,
}

// keyTimestampPrefixRE matches the "<yyyymmdd>-<HHMMSS>-" prefix (plus an optional
// run_python "py-" marker) that uploadGeneratedFile / run_python prepend to every
// object-key tail for uniqueness (e.g. "20260616-101010-本周工作小结.docx" or
// "20260616-101010-py-本周工作小结.docx"). Stripped from the re-signed download
// filename so a reopened session shows the clean content name in the
// Content-Disposition (and thus the artifact card), matching the first-time
// download which already passes the clean filename. Only ONE leading timestamp is
// removed, so a user file literally named "20260101-计划.docx" keeps its own date.
// The optional "py-" is the system run_python marker; the rare cost is a user file
// literally named "py-foo.docx" losing its "py-" on reopen (cosmetic only).
var keyTimestampPrefixRE = regexp.MustCompile(`^\d{8}-\d{6}-(?:py-)?`)

var cosLinkReCache sync.Map // host(string) -> *regexp.Regexp

// cosLinkRE returns a cached regexp matching any URL on the given COS bucket
// host up to the first markdown/whitespace boundary. The character class stops
// at ')' so it stays inside a markdown link's parentheses, and greedily
// consumes the object path PLUS whatever query string was persisted (complete
// or truncated) so the whole stale URL gets replaced in one shot.
func cosLinkRE(host string) *regexp.Regexp {
	if v, ok := cosLinkReCache.Load(host); ok {
		return v.(*regexp.Regexp)
	}
	// '(' is intentionally NOT a boundary: COS object keys are sanitized via
	// sanitizeOutputFilename / sanitizeObjectKeyName (both map '(' ')' → '_', the
	// latter because parens are not \p{L}/\p{N}), so a key can never contain '('
	// or ')'. The ')' boundary therefore only ever marks the end of a markdown
	// link. Readable keys may now carry percent-encoded UTF-8 (e.g. %E6%9C%AC) in
	// the URL text — still ASCII, no parens — so the boundary is unaffected; the
	// extracted key is url.PathUnescape'd in resignCOSLinksWithHost before signing.
	re := regexp.MustCompile(`https://` + regexp.QuoteMeta(host) + `/[^\s)"'<>]+`)
	cosLinkReCache.Store(host, re)
	return re
}

// resignCOSLinksWithHost rewrites every COS bucket URL in markdown with a fresh
// presigned URL derived SOLELY from the URL path (object key), discarding the
// persisted query string.
//
// This heals two failure modes of embedding a full signed URL in persisted
// markdown:
//
//  1. The model truncated the ~600-char signed URL when transcribing it into
//     its final answer, dropping the q-sign-* params (dev run 150 — the
//     download returned COS "InvalidRequest: Request specific response headers
//     cannot be used for anonymous GET"). The path survived the truncation, so
//     re-signing from the path alone produces a working URL.
//  2. The 24h presign expired since the run finished, so a reopened session
//     would otherwise show broken links/images.
//
// Image object keys re-sign inline (no attachment) so chat <img> still renders;
// everything else re-signs as an attachment download. On any signing error the
// original text is left untouched (best-effort, never worse than before).
func resignCOSLinksWithHost(ctx context.Context, markdown, host string, s cosSigner) string {
	if markdown == "" || host == "" {
		return markdown
	}
	prefix := "https://" + host + "/"
	return cosLinkRE(host).ReplaceAllStringFunc(markdown, func(match string) string {
		objectKey := strings.TrimPrefix(match, prefix)
		if i := strings.IndexByte(objectKey, '?'); i >= 0 {
			objectKey = objectKey[:i]
		}
		if objectKey == "" {
			return match
		}
		// The URL text carries the key percent-encoded (readable keys may hold
		// %E6%9C%AC… UTF-8). Decode back to the raw key the COS SDK expects — it
		// re-encodes internally when signing, so passing the encoded form would
		// double-encode → a 404 key. PathUnescape on a pure-ASCII key (legacy
		// sanitizeOutputFilename output, no '%') is an identity, so legacy links
		// are unaffected. On a malformed escape, keep the original (best-effort).
		if decoded, err := url.PathUnescape(objectKey); err == nil {
			objectKey = decoded
		}
		name := objectKey
		if i := strings.LastIndexByte(objectKey, '/'); i >= 0 {
			name = objectKey[i+1:]
		}
		// Strip the system "<ts>-" prefix so the reflected download filename (and the
		// reopened-session artifact card, which reads it) shows the clean name.
		name = keyTimestampPrefixRE.ReplaceAllString(name, "")
		var (
			signed string
			err    error
		)
		if cosIsImageName(name) {
			signed, err = s.signImage(ctx, objectKey, cosResignExpirySeconds)
		} else {
			signed, err = s.signDownload(ctx, objectKey, name, cosResignExpirySeconds)
		}
		if err != nil || signed == "" {
			return match
		}
		return signed
	})
}

// cosIsImageName reports whether the object's filename extension is an inline
// image type (rendered with <img> rather than downloaded as an attachment).
func cosIsImageName(name string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return false
	}
	return cosImageExts[strings.ToLower(name[dot:])]
}

// resignCOSLinks is the production wrapper: it resolves the live bucket host and
// the real util signers. A no-op when COS is unconfigured (host == "").
func resignCOSLinks(ctx context.Context, markdown string) string {
	return resignCOSLinksWithHost(ctx, markdown, util.COSBucketHost(), cosSigner{
		signImage:    util.GenerateSignedURL,
		signDownload: util.GenerateSignedDownloadURL,
	})
}
