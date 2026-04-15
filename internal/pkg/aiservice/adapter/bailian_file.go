package adapter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/httpclient"
)

// Compile-time interface check.
var _ FileServiceAdapter = (*BailianFileAdapter)(nil)

// bailianDefaultBaseURL is the default Alibaba Bailian API base URL (production).
const bailianDefaultBaseURL = "https://bailian.cn-hangzhou.aliyuncs.com"

// BailianFileAdapter implements FileServiceAdapter using the Alibaba Bailian
// file upload API (ApplyFileUploadLease + direct OSS PUT + AddFile confirm).
//
// ServiceSpec:
//
//	route.Provider.APIKey    — "access_key_id:access_key_secret" (colon-separated).
//	route.Provider.BaseURL   — Optional Bailian API base URL.  Defaults to the
//	                           production endpoint when empty.  May be an HTTP URL
//	                           for test environments.
//	route.ProviderModelID    — Bailian WorkspaceID.
//
// Credential format: "access_key_id:access_key_secret" (colon-separated).
type BailianFileAdapter struct {
	client *httpclient.Client
}

// NewBailianFileAdapter creates a BailianFileAdapter backed by the shared httpclient pool.
func NewBailianFileAdapter() *BailianFileAdapter {
	return &BailianFileAdapter{
		client: httpclient.NewClient(nil),
	}
}

// Name returns the adapter identifier.
func (b *BailianFileAdapter) Name() string { return "bailian_file" }

// ProviderType returns the provider category.
func (b *BailianFileAdapter) ProviderType() string { return "bailian" }

// Capabilities lists the capabilities this adapter supports.
func (b *BailianFileAdapter) Capabilities() []string { return []string{"file_service"} }

// UploadFile uploads a file to the Alibaba Bailian file service and returns
// the provider-assigned FileID.
//
// The upload flow is:
//  1. ApplyFileUploadLease — obtain a pre-signed OSS URL and lease ID.
//  2. PUT the file bytes directly to OSS using the pre-signed URL.
//  3. AddFile (confirm) — notify Bailian the upload is complete; obtain FileID.
func (b *BailianFileAdapter) UploadFile(ctx context.Context, route *registry.ResolvedRoute, req aiservice.FileUploadRequest) (*aiservice.FileUploadResponse, error) {
	akID, akSecret, err := b.parseCredentials(route.Provider.APIKey)
	if err != nil {
		return nil, fmt.Errorf("bailian_file.UploadFile: %w", err)
	}

	workspaceID := route.ProviderModelID
	baseURL := b.resolveBaseURL(route.Provider.BaseURL)

	// Step 1: Apply for upload lease.
	ossURL, ossHeaders, leaseID, err := b.getLease(ctx, akID, akSecret, baseURL, workspaceID, req.FileName)
	if err != nil {
		return nil, fmt.Errorf("bailian_file.UploadFile: getLease: %w", err)
	}

	// Step 2: PUT file bytes to OSS.
	if err := b.putToOSS(ctx, ossURL, ossHeaders, req.FileBytes, req.MimeType); err != nil {
		return nil, fmt.Errorf("bailian_file.UploadFile: putToOSS: %w", err)
	}

	// Step 3: Confirm the upload and obtain the FileID.
	fileID, err := b.confirmFile(ctx, akID, akSecret, baseURL, workspaceID, leaseID)
	if err != nil {
		return nil, fmt.Errorf("bailian_file.UploadFile: confirmFile: %w", err)
	}

	return &aiservice.FileUploadResponse{
		FileID:     fileID,
		Provider:   b.Name(),
		UploadedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// parseCredentials splits "akID:akSecret" from the provider APIKey field.
func (b *BailianFileAdapter) parseCredentials(raw string) (akID, akSecret string, err error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid bailian credentials format; expected 'ak_id:ak_secret', got %q", raw)
	}
	return parts[0], parts[1], nil
}

// resolveBaseURL returns the Bailian API base URL.
// - If baseURL is empty, the production URL is returned.
// - If it already has a scheme (http:// / https://), it is returned trimmed.
// - Otherwise https:// is prepended (bare hostname case).
func (b *BailianFileAdapter) resolveBaseURL(baseURL string) string {
	if baseURL == "" {
		return bailianDefaultBaseURL
	}
	trimmed := strings.TrimRight(baseURL, "/")
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	return "https://" + trimmed
}

// extractHost derives the hostname (without scheme/path) from a full URL string.
// Used to populate the "host" header in the ACS3 signature.
func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		// Fallback: strip scheme manually.
		h := strings.TrimPrefix(rawURL, "https://")
		h = strings.TrimPrefix(h, "http://")
		if idx := strings.Index(h, "/"); idx >= 0 {
			h = h[:idx]
		}
		return h
	}
	return u.Host
}

// generateV3Signature generates an Alibaba Cloud ACS3-HMAC-SHA256 signature.
// baseURL is the full base URL of the Bailian endpoint (used to derive the host
// header value for signing).
//
// This is a direct port of BailianHTTPClient.GenerateV3Signature from
// internal/service/bailian_http.go, adapted to use the httpclient layer.
func (b *BailianFileAdapter) generateV3Signature(akID, akSecret, baseURL, method, action, version string, bodyBytes []byte) (map[string]string, error) {
	host := extractHost(baseURL)

	now := time.Now().UTC()
	xAcsDate := now.Format("2006-01-02T15:04:05Z")
	nonce := uuid.New().String()

	// 1. Payload SHA256.
	h := sha256.New()
	h.Write(bodyBytes)
	contentSHA256 := hex.EncodeToString(h.Sum(nil))

	// Base headers participating in the signature.
	headers := map[string]string{
		"host":                  host,
		"x-acs-action":          action,
		"x-acs-version":         version,
		"x-acs-date":            xAcsDate,
		"x-acs-signature-nonce": nonce,
		"x-acs-content-sha256":  contentSHA256,
		"content-type":          "application/json; charset=utf-8",
	}

	// 2. Canonical headers (sorted alphabetically).
	var signedHeaders []string
	for k := range headers {
		signedHeaders = append(signedHeaders, strings.ToLower(k))
	}
	sort.Strings(signedHeaders)

	var canonicalHeaders strings.Builder
	for _, k := range signedHeaders {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(headers[k]))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeadersStr := strings.Join(signedHeaders, ";")

	// 3. Canonical request.
	canonicalRequest := strings.Join([]string{
		method,
		"/",
		"",
		canonicalHeaders.String(),
		signedHeadersStr,
		contentSHA256,
	}, "\n")

	// 4. StringToSign.
	h2 := sha256.New()
	h2.Write([]byte(canonicalRequest))
	hashedCanonical := hex.EncodeToString(h2.Sum(nil))
	stringToSign := "ACS3-HMAC-SHA256\n" + hashedCanonical

	// 5. HMAC-SHA256 signature.
	mac := hmac.New(sha256.New, []byte(akSecret))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	// 6. Authorization header.
	auth := fmt.Sprintf("ACS3-HMAC-SHA256 Credential=%s,SignedHeaders=%s,Signature=%s",
		akID, signedHeadersStr, signature)

	headers["Authorization"] = auth
	return headers, nil
}

// getLease calls the Bailian ApplyFileUploadLease action and returns the
// pre-signed OSS upload URL, required OSS headers, and the lease ID.
func (b *BailianFileAdapter) getLease(ctx context.Context, akID, akSecret, baseURL, workspaceID, fileName string) (ossURL string, ossHeaders map[string]string, leaseID string, err error) {
	bodyMap := map[string]interface{}{
		"FileName":     fileName,
		"CategoryType": "SESSION_FILE",
		"WorkspaceId":  workspaceID,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	signedHeaders, err := b.generateV3Signature(akID, akSecret, baseURL, "POST", "ApplyFileUploadLease", "2023-12-29", bodyBytes)
	if err != nil {
		return "", nil, "", fmt.Errorf("getLease: sign: %w", err)
	}

	apiURL := baseURL + "/"
	resp, err := b.client.Do(&httpclient.Request{
		Method:      "POST",
		URL:         apiURL,
		Body:        bytes.NewReader(bodyBytes),
		Context:     ctx,
		Headers:     signedHeaders,
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return "", nil, "", wrapHTTPClientErr("getLease", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", nil, "", wrapHTTPStatusErr("getLease", resp.StatusCode, body)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, "", fmt.Errorf("getLease: read body: %w", err)
	}

	var result struct {
		Data struct {
			FileUploadLeaseID string `json:"FileUploadLeaseId"`
			Param             struct {
				URL     string            `json:"Url"`
				Headers map[string]string `json:"Headers"`
			} `json:"Param"`
		} `json:"Data"`
		Message string `json:"Message"`
		Code    string `json:"Code"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", nil, "", fmt.Errorf("getLease: decode: %w", err)
	}
	if result.Code != "" && result.Code != "200" && result.Code != "Success" {
		return "", nil, "", fmt.Errorf("getLease: API error [%s]: %s", result.Code, result.Message)
	}

	return result.Data.Param.URL, result.Data.Param.Headers, result.Data.FileUploadLeaseID, nil
}

// putToOSS uploads the file bytes directly to the pre-signed OSS URL via HTTP PUT.
func (b *BailianFileAdapter) putToOSS(ctx context.Context, ossURL string, ossHeaders map[string]string, data []byte, mimeType string) error {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Merge provider OSS headers with our content-type.
	merged := make(map[string]string, len(ossHeaders)+1)
	for k, v := range ossHeaders {
		merged[k] = v
	}
	merged["Content-Type"] = mimeType

	resp, err := b.client.Do(&httpclient.Request{
		Method:      "PUT",
		URL:         ossURL,
		Body:        bytes.NewReader(data),
		Context:     ctx,
		Headers:     merged,
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return wrapHTTPClientErr("putToOSS", err)
	}
	defer resp.Body.Close()

	// OSS returns 200 or 2xx for successful PUT.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return wrapHTTPStatusErr("putToOSS", resp.StatusCode, body)
	}
	return nil
}

// confirmFile calls the Bailian AddFile action to confirm the upload and
// obtain the permanent FileID.
func (b *BailianFileAdapter) confirmFile(ctx context.Context, akID, akSecret, baseURL, workspaceID, leaseID string) (string, error) {
	bodyMap := map[string]interface{}{
		"FileUploadLeaseId": leaseID,
		"CategoryType":      "SESSION_FILE",
		"WorkspaceId":       workspaceID,
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	signedHeaders, err := b.generateV3Signature(akID, akSecret, baseURL, "POST", "AddFile", "2023-12-29", bodyBytes)
	if err != nil {
		return "", fmt.Errorf("confirmFile: sign: %w", err)
	}

	apiURL := baseURL + "/"
	resp, err := b.client.Do(&httpclient.Request{
		Method:      "POST",
		URL:         apiURL,
		Body:        bytes.NewReader(bodyBytes),
		Context:     ctx,
		Headers:     signedHeaders,
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return "", wrapHTTPClientErr("confirmFile", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", wrapHTTPStatusErr("confirmFile", resp.StatusCode, body)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("confirmFile: read body: %w", err)
	}

	var result struct {
		Data struct {
			FileID string `json:"FileId"`
		} `json:"Data"`
		Message string `json:"Message"`
		Code    string `json:"Code"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("confirmFile: decode: %w", err)
	}
	if result.Code != "" && result.Code != "200" && result.Code != "Success" {
		return "", fmt.Errorf("confirmFile: API error [%s]: %s", result.Code, result.Message)
	}

	return result.Data.FileID, nil
}
