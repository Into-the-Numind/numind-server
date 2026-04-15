package adapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
)

// Compile-time interface check.
var _ OCRAdapter = (*BaiduOCRAdapter)(nil)

// baiduTokenCache caches a Baidu access_token per API-key pair to avoid
// fetching a new token on every request.  Baidu tokens are valid for 30 days;
// we refresh 1 hour before expiry (same strategy as biz/baidu/ocr.go).
type baiduTokenCache struct {
	mu        sync.RWMutex
	token     string
	expiresAt time.Time
	// apiKey and secretKey identify which credentials this cache entry belongs to.
	apiKey    string
	secretKey string
}

func (c *baiduTokenCache) get() (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.token != "" && time.Now().Before(c.expiresAt) {
		return c.token, true
	}
	return "", false
}

func (c *baiduTokenCache) set(token string, expiresIn int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
	c.expiresAt = time.Now().Add(time.Duration(expiresIn-3600) * time.Second)
}

// baiduOCRResult is the raw Baidu OCR API response.
type baiduOCRResult struct {
	LogID          uint64         `json:"log_id"`
	WordsResultNum int            `json:"words_result_num"`
	WordsResult    []baiduOCRWord `json:"words_result"`
	ErrorCode      int            `json:"error_code,omitempty"`
	ErrorMsg       string         `json:"error_msg,omitempty"`
}

// baiduOCRWord is a single recognised word from the Baidu OCR response.
type baiduOCRWord struct {
	Words    string           `json:"words"`
	Location baiduOCRLocation `json:"location"`
}

// baiduOCRLocation holds the bounding box of a recognised word.
type baiduOCRLocation struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// BaiduOCRAdapter implements OCRAdapter using the Baidu AI Cloud OCR high-accuracy API.
//
// Credentials are read from the registry route:
//
//	route.Provider.APIKey = "<api_key>:<secret_key>"   (colon-separated)
//	route.Provider.BaseURL                             (optional, overrides the default Baidu endpoint)
//
// Access-token caching: tokens are cached in-process per credential pair and
// refreshed 1 hour before expiry.  Tokens are valid for 30 days according to
// Baidu documentation.
type BaiduOCRAdapter struct {
	client *httpclient.Client

	// tokenCacheMu guards tokenCaches.
	tokenCacheMu sync.Mutex
	// tokenCaches maps "apiKey:secretKey" to a per-credential token cache.
	tokenCaches map[string]*baiduTokenCache
}

// NewBaiduOCRAdapter creates a BaiduOCRAdapter backed by the shared httpclient pool.
func NewBaiduOCRAdapter() *BaiduOCRAdapter {
	return &BaiduOCRAdapter{
		client:      httpclient.NewClient(nil),
		tokenCaches: make(map[string]*baiduTokenCache),
	}
}

// Name returns the adapter identifier.
func (b *BaiduOCRAdapter) Name() string { return "baidu_ocr" }

// ProviderType returns the provider category.
func (b *BaiduOCRAdapter) ProviderType() string { return "baidu" }

// Capabilities lists the capabilities this adapter supports.
func (b *BaiduOCRAdapter) Capabilities() []string { return []string{"ocr"} }

// OCR extracts text from an image using the Baidu high-accuracy OCR API.
func (b *BaiduOCRAdapter) OCR(ctx context.Context, route *registry.ResolvedRoute, req aiservice.OCRRequest) (*aiservice.OCRResponse, error) {
	apiKey, secretKey, err := b.parseCredentials(route.Provider.APIKey)
	if err != nil {
		return nil, fmt.Errorf("baidu_ocr.OCR: %w", err)
	}

	token, err := b.getOrRefreshToken(ctx, apiKey, secretKey, route.Provider.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("baidu_ocr.OCR: get token: %w", err)
	}

	imageData, err := b.resolveImageData(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("baidu_ocr.OCR: resolve image: %w", err)
	}

	result, err := b.callOCRAPI(ctx, token, imageData, route.Provider.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("baidu_ocr.OCR: %w", err)
	}

	return b.buildResponse(result), nil
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// parseCredentials splits "apiKey:secretKey" from the provider APIKey field.
func (b *BaiduOCRAdapter) parseCredentials(raw string) (apiKey, secretKey string, err error) {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid baidu credentials format; expected 'api_key:secret_key', got %q", raw)
	}
	return parts[0], parts[1], nil
}

// baiduBase returns the base URL for Baidu API calls.  If baseURL is set it
// is used directly (useful for tests); otherwise the production Baidu endpoint
// is used.
func baiduBase(baseURL string) string {
	if baseURL != "" {
		return strings.TrimRight(baseURL, "/")
	}
	return "https://aip.baidubce.com"
}

// getOrRefreshToken returns a valid access token, fetching a new one if necessary.
func (b *BaiduOCRAdapter) getOrRefreshToken(ctx context.Context, apiKey, secretKey, baseURL string) (string, error) {
	cacheKey := apiKey + ":" + secretKey

	b.tokenCacheMu.Lock()
	cache, ok := b.tokenCaches[cacheKey]
	if !ok {
		cache = &baiduTokenCache{apiKey: apiKey, secretKey: secretKey}
		b.tokenCaches[cacheKey] = cache
	}
	b.tokenCacheMu.Unlock()

	if token, valid := cache.get(); valid {
		return token, nil
	}

	// Double-checked lock: acquire write lock and check again.
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.token != "" && time.Now().Before(cache.expiresAt) {
		return cache.token, nil
	}

	tokenURL := baiduBase(baseURL) + "/oauth/2.0/token"
	params := url.Values{}
	params.Set("grant_type", "client_credentials")
	params.Set("client_id", apiKey)
	params.Set("client_secret", secretKey)
	tokenURL += "?" + params.Encode()

	resp, err := b.client.Do(&httpclient.Request{
		Method:      "POST",
		URL:         tokenURL,
		Body:        bytes.NewReader(nil),
		Context:     ctx,
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 1},
		Headers: map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		},
	})
	if err != nil {
		return "", wrapHTTPClientErr("getToken", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", wrapHTTPStatusErr("getToken", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("getToken: read body: %w", err)
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error,omitempty"`
		ErrorDesc   string `json:"error_description,omitempty"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("getToken: decode: %w", err)
	}
	if tokenResp.Error != "" {
		return "", fmt.Errorf("getToken: baidu error: %s - %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("getToken: empty access_token in response")
	}

	cache.token = tokenResp.AccessToken
	cache.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn-3600) * time.Second)
	log.Infow("baidu_ocr: access_token refreshed", "expires_in", tokenResp.ExpiresIn)

	return cache.token, nil
}

// resolveImageData returns the raw image bytes from the request.
// If ImageURL is provided it downloads the image; otherwise ImageBytes is used directly.
func (b *BaiduOCRAdapter) resolveImageData(ctx context.Context, req aiservice.OCRRequest) ([]byte, error) {
	if req.ImageURL != "" {
		resp, err := b.client.Do(&httpclient.Request{
			Method:      "GET",
			URL:         req.ImageURL,
			Context:     ctx,
			RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 1},
		})
		if err != nil {
			return nil, wrapHTTPClientErr("resolveImageData download", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, wrapHTTPStatusErr("resolveImageData download", resp.StatusCode, body)
		}
		return io.ReadAll(resp.Body)
	}
	if len(req.ImageBytes) > 0 {
		return req.ImageBytes, nil
	}
	return nil, fmt.Errorf("OCRRequest must provide either ImageURL or ImageBytes")
}

// callOCRAPI calls the Baidu accurate OCR endpoint and returns the raw result.
func (b *BaiduOCRAdapter) callOCRAPI(ctx context.Context, token string, imageData []byte, baseURL string) (*baiduOCRResult, error) {
	ocrURL := baiduBase(baseURL) + "/rest/2.0/ocr/v1/accurate?access_token=" + url.QueryEscape(token)

	b64 := base64.StdEncoding.EncodeToString(imageData)
	formValues := url.Values{}
	formValues.Set("image", b64)
	formValues.Set("language_type", "CHN_ENG")
	formValues.Set("detect_direction", "true")

	resp, err := b.client.Do(&httpclient.Request{
		Method:  "POST",
		URL:     ocrURL,
		Body:    strings.NewReader(formValues.Encode()),
		Context: ctx,
		Headers: map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		},
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return nil, wrapHTTPClientErr("callOCRAPI", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr("callOCRAPI", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("callOCRAPI: read body: %w", err)
	}

	var result baiduOCRResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("callOCRAPI: decode: %w", err)
	}
	if result.ErrorCode != 0 {
		return nil, fmt.Errorf("callOCRAPI: baidu error %d: %s", result.ErrorCode, result.ErrorMsg)
	}
	return &result, nil
}

// buildResponse converts the raw Baidu OCR result to the unified aiservice response.
func (b *BaiduOCRAdapter) buildResponse(result *baiduOCRResult) *aiservice.OCRResponse {
	words := make([]aiservice.OCRWord, 0, len(result.WordsResult))
	textParts := make([]string, 0, len(result.WordsResult))

	for _, w := range result.WordsResult {
		if w.Words == "" {
			continue
		}
		textParts = append(textParts, w.Words)
		words = append(words, aiservice.OCRWord{
			Word: w.Words,
			BoundingBox: []int{
				w.Location.Left,
				w.Location.Top,
				w.Location.Left + w.Location.Width,
				w.Location.Top + w.Location.Height,
			},
		})
	}

	return &aiservice.OCRResponse{
		Text:     strings.Join(textParts, "\n"),
		Words:    words,
		Provider: b.Name(),
	}
}
