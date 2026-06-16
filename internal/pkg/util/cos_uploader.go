package util

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
	cos "github.com/tencentyun/cos-go-sdk-v5"
)

// COSClient is a lazy-initialized singleton for Tencent COS operations.
type COSClient struct {
	once    sync.Once
	client  *cos.Client
	baseURL string
	bucket  string
	enabled bool
}

var globalCOS COSClient

func getCOSClient() (*COSClient, error) {
	var initErr error
	globalCOS.once.Do(func() {
		// Read config
		enabled := viper.GetBool("cos.enabled")
		secretID := viper.GetString("cos.secret_id")
		secretKey := viper.GetString("cos.secret_key")
		bucket := viper.GetString("cos.bucket") // bucket-APPID
		region := viper.GetString("cos.region") // e.g. ap-beijing

		if !enabled || secretID == "" || secretKey == "" || bucket == "" || region == "" {
			globalCOS.enabled = false
			log.Infow("COS disabled or missing config", "enabled", enabled, "bucket", bucket, "region", region)
			return
		}

		// Construct base URL: https://<bucket>.cos.<region>.myqcloud.com
		base := fmt.Sprintf("https://%s.cos.%s.myqcloud.com", bucket, region)
		u, err := url.Parse(base)
		if err != nil {
			initErr = fmt.Errorf("parse COS base url: %w", err)
			return
		}

		b := &cos.BaseURL{BucketURL: u}
		transport := &cos.AuthorizationTransport{
			SecretID:  secretID,
			SecretKey: secretKey,
		}

		httpClient := &http.Client{Transport: transport}
		client := cos.NewClient(b, httpClient)

		globalCOS.client = client
		globalCOS.baseURL = strings.TrimRight(base, "/")
		globalCOS.bucket = bucket
		globalCOS.enabled = true
		log.Infow("COS client initialized", "bucket", bucket, "region", region, "baseURL", globalCOS.baseURL)
	})

	if initErr != nil {
		return nil, initErr
	}
	if !globalCOS.enabled {
		return &globalCOS, nil
	}
	return &globalCOS, nil
}

// UploadBytes uploads content to COS at given objectKey and returns the public URL.
// objectKey should be like: card/123/card_123.webp
func UploadBytesToCOS(ctx context.Context, objectKey string, contentType string, data []byte) (string, error) {
	cosClient, err := getCOSClient()
	if err != nil {
		return "", err
	}
	if !cosClient.enabled || cosClient.client == nil {
		log.Debugw("COS not enabled, skip upload")
		return "", nil
	}

	objectKey = strings.TrimPrefix(objectKey, "/")
	// Normalize path
	objectKey = path.Clean(objectKey)

	opt := &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: contentType}}

	log.Infow("COS upload start", "key", objectKey, "contentType", contentType, "bytes", len(data))
	_, err = cosClient.client.Object.Put(ctx, objectKey, bytes.NewReader(data), opt)
	if err != nil {
		log.Errorw("COS upload failed", "key", objectKey, "error", err)
		return "", err
	}

	// HEAD verify
	if _, err := cosClient.client.Object.Head(ctx, objectKey, nil); err != nil {
		log.Warnw("COS head verify failed", "key", objectKey, "error", err)
	} else {
		log.Infow("COS upload verified", "key", objectKey)
	}

	// Return base URL (non-signed). Callers can request signed URL via GenerateSignedURL.
	return fmt.Sprintf("%s/%s", cosClient.baseURL, objectKey), nil
}

// IsCOSEnabled returns whether COS is properly configured.
func IsCOSEnabled() bool {
	c, _ := getCOSClient()
	return c != nil && c.enabled && c.client != nil
}

// GenerateSignedURL returns a temporary signed URL for reading the object.
// expirySeconds: validity duration in seconds (e.g., 600 for 10 minutes)
func GenerateSignedURL(ctx context.Context, objectKey string, expirySeconds int64) (string, error) {
	cosClient, err := getCOSClient()
	if err != nil {
		return "", err
	}
	if !cosClient.enabled || cosClient.client == nil {
		return "", fmt.Errorf("cos not enabled")
	}
	objectKey = strings.TrimPrefix(objectKey, "/")
	objectKey = path.Clean(objectKey)

	// SDK provides presigned URL generation
	// method GET for read
	u, err := cosClient.client.Object.GetPresignedURL(ctx, http.MethodGet, objectKey, viper.GetString("cos.secret_id"), viper.GetString("cos.secret_key"), timeDurationSeconds(expirySeconds), nil)
	if err != nil {
		log.Errorw("COS generate signed URL failed", "key", objectKey, "error", err)
		return "", err
	}
	signed := u.String()
	log.Debugw("COS signed URL generated", "key", objectKey, "expiresIn", expirySeconds)
	return signed, nil
}

// timeDurationSeconds converts seconds to time.Duration safely.
func timeDurationSeconds(sec int64) time.Duration {
	if sec <= 0 {
		sec = 600
	}
	return time.Duration(sec) * time.Second
}

// CheckObjectExists checks if an object exists in COS
func CheckObjectExists(ctx context.Context, objectKey string) bool {
	cosClient, err := getCOSClient()
	if err != nil {
		return false
	}
	if !cosClient.enabled || cosClient.client == nil {
		return false
	}

	objectKey = strings.TrimPrefix(objectKey, "/")
	objectKey = path.Clean(objectKey)

	// Use HEAD request to check if object exists
	_, err = cosClient.client.Object.Head(ctx, objectKey, nil)
	return err == nil
}

// GenerateSignedDownloadURL returns a presigned GET URL that asks Tencent COS
// to reflect a Content-Disposition: attachment header on the response. This is
// the URL to hand to a browser when the user clicks a download button.
//
// Without response-content-disposition, COS serves objects inline (no
// Content-Disposition header). When the browser is on https://youshu.asia and
// the download target is on https://*.cos.<region>.myqcloud.com, Chrome's
// cross-site-download safety heuristic flags the programmatic download as
// "不安全 (无法验证此文件来源)". Reflecting attachment + filename* fixes that.
//
// The filename is percent-encoded per RFC 5987 (filename*=UTF-8”...) so
// non-ASCII names (Chinese, spaces) round-trip correctly through HTTP headers.
//
// For non-download flows (inline images, files fetched by the LLM, etc.) keep
// using GenerateSignedURL / GenerateSignedURLForMethod — forcing attachment
// would break inline display.
func GenerateSignedDownloadURL(ctx context.Context, objectKey, filename string, expirySeconds int64) (string, error) {
	cosClient, err := getCOSClient()
	if err != nil {
		return "", err
	}
	if !cosClient.enabled || cosClient.client == nil {
		return "", fmt.Errorf("cos not enabled")
	}
	objectKey = strings.TrimPrefix(objectKey, "/")
	objectKey = path.Clean(objectKey)

	// Defense in depth: strip CR/LF from filename before percent-encoding so a
	// caller passing a hostile filename can't smuggle bytes into the reflected
	// Content-Disposition header. The double-encoding chain (PathEscape →
	// url.Values.Encode → COS decodes once) already prevents raw CRLF from
	// reaching the wire, but stripping is cheap and removes ambiguity.
	cleanName := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, filename)
	disp := "attachment; filename*=UTF-8''" + url.PathEscape(cleanName)
	opt := &cos.PresignedURLOptions{
		Query: &url.Values{
			"response-content-disposition": []string{disp},
		},
	}

	u, err := cosClient.client.Object.GetPresignedURL(
		ctx,
		http.MethodGet,
		objectKey,
		viper.GetString("cos.secret_id"),
		viper.GetString("cos.secret_key"),
		timeDurationSeconds(expirySeconds),
		opt,
	)
	if err != nil {
		log.Errorw("COS generate signed download URL failed", "key", objectKey, "error", err)
		return "", err
	}
	log.Debugw("COS signed download URL generated", "key", objectKey, "filename", filename, "expiresIn", expirySeconds)
	return u.String(), nil
}
