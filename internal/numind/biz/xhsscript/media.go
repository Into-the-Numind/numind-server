package xhsscript

import (
	"context"
	"net/url"
	"strings"

	"numind-server/internal/pkg/util"
)

const xhsScriptVideoSignExpiry = int64(6 * 3600)

var signXhsScriptVideoURLFn = func(ctx context.Context, objectKey string) (string, error) {
	return util.GenerateSignedURL(ctx, objectKey, xhsScriptVideoSignExpiry)
}

func resignXhsScriptVideoURL(ctx context.Context, rawURL string) string {
	objectKey, ok := xhsScriptVideoObjectKey(rawURL)
	if !ok {
		return rawURL
	}
	signedURL, err := signXhsScriptVideoURLFn(ctx, objectKey)
	if err != nil || strings.TrimSpace(signedURL) == "" {
		return rawURL
	}
	return signedURL
}

func xhsScriptVideoObjectKey(rawURL string) (string, bool) {
	if strings.TrimSpace(rawURL) == "" {
		return "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	objectKey := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if objectKey == "" {
		objectKey = strings.TrimPrefix(parsed.Path, "/")
	}
	if decoded, decErr := url.PathUnescape(objectKey); decErr == nil {
		objectKey = decoded
	}
	idx := strings.Index(objectKey, "xhs-script-media/")
	if idx < 0 {
		return "", false
	}
	return objectKey[idx:], true
}
