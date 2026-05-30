package sandbox

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/util"
)

// COSUploadConfig holds the configuration for uploading sandbox output files
// to Tencent COS. Values are read from viper (cos.* + sandbox.*) by the
// caller (pool_skill.go); never hard-coded here.
type COSUploadConfig struct {
	// Prefix is the COS object-key prefix, e.g. "agent-output/<userID>/<date>/".
	Prefix string
}

// UploadOutputFile uploads a single file from the local filesystem to COS and
// returns a presigned GET URL valid for 24 hours (T4 decision).
//
// The object key is <cfg.Prefix><filename>. The presigned URL is generated via
// util.GenerateSignedURL (which reads COS credentials from viper at runtime).
//
// If COS is disabled (local / test environment), util.UploadBytesToCOS returns
// an empty URL; this function returns a synthetic "/local-uploads/…" URL so
// callers still have a non-empty string to work with.
func UploadOutputFile(ctx context.Context, cfg COSUploadConfig, localPath string, filename string, mimeType string, data []byte) (string, error) {
	objectKey := cfg.Prefix + filename

	// Upload bytes to COS.
	publicURL, err := util.UploadBytesToCOS(ctx, objectKey, mimeType, data)
	if err != nil {
		return "", fmt.Errorf("sandbox.UploadOutputFile: COS upload %s: %w", objectKey, err)
	}

	// COS disabled (local/CI) — synthesise a placeholder URL.
	if publicURL == "" {
		return fmt.Sprintf("/local-uploads/%s", objectKey), nil
	}

	// Generate 24-hour presigned GET URL with response-content-disposition=attachment
	// so the cross-origin download from https://youshu.asia doesn't get flagged as
	// "不安全" by Chrome (cf. cos_download_content_disposition hotfix).
	presigned, err := util.GenerateSignedDownloadURL(ctx, objectKey, filename, int64(24*time.Hour/time.Second))
	if err != nil {
		// Fallback: return the public URL (non-signed) so the caller isn't blocked.
		// This can happen if COS signing keys are temporarily unavailable.
		return publicURL, nil
	}
	return presigned, nil
}

// BuildCOSPrefix returns the COS object-key prefix for sandbox output files
// belonging to a specific user on today's date.
// Format: "agent-output/<userID>/<YYYY-MM-DD>/"
func BuildCOSPrefix(userID uint) string {
	return fmt.Sprintf("agent-output/%d/%s/", userID, time.Now().UTC().Format("2006-01-02"))
}
