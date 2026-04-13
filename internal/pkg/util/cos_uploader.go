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
