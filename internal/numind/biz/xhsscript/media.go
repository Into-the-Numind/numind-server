package xhsscript

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"numind-server/internal/pkg/util"
)

const xhsScriptVideoSignExpiry = int64(6 * 3600)

var (
	signXhsScriptVideoURLFn = func(ctx context.Context, objectKey string) (string, error) {
		return util.GenerateSignedURL(ctx, objectKey, xhsScriptVideoSignExpiry)
	}
	xhsScriptCOSBucketHostFn = util.COSBucketHost
)

func resignXhsScriptVideoURL(ctx context.Context, userID uint, noteID uint64, rawURL string) string {
	objectKey, ok := xhsScriptVideoObjectKey(rawURL, userID, noteID)
	if !ok {
		return rawURL
	}
	signedURL, err := signXhsScriptVideoURLFn(ctx, objectKey)
	if err != nil || strings.TrimSpace(signedURL) == "" {
		return rawURL
	}
	return signedURL
}

func xhsScriptVideoObjectKey(rawURL string, userID uint, noteID uint64) (string, bool) {
	if strings.TrimSpace(rawURL) == "" {
		return "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	bucketHost := strings.TrimSpace(xhsScriptCOSBucketHostFn())
	if bucketHost == "" || !strings.EqualFold(parsed.Hostname(), bucketHost) {
		return "", false
	}
	objectKey := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if objectKey == "" {
		objectKey = strings.TrimPrefix(parsed.Path, "/")
	}
	if decoded, decErr := url.PathUnescape(objectKey); decErr == nil {
		objectKey = decoded
	}
	expectedKey := fmt.Sprintf("xhs-script-media/%d/%d/video.mp4", userID, noteID)
	if objectKey != expectedKey {
		return "", false
	}
	return objectKey, true
}
