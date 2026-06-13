package agent

import (
	"context"
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
		name := objectKey
		if i := strings.LastIndexByte(objectKey, '/'); i >= 0 {
			name = objectKey[i+1:]
		}
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
