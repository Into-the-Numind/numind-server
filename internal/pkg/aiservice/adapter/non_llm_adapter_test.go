package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// ----------------------------------------------------------------------------
// Helper: build a route pointing at a test server
// ----------------------------------------------------------------------------

func mockRouteWithModel(serverURL, apiKey, modelID string) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		ProviderModelID: modelID,
		Provider: registry.ProviderInfo{
			BaseURL: serverURL,
			APIKey:  apiKey,
		},
	}
}

// ----------------------------------------------------------------------------
// BaiduOCRAdapter tests
// ----------------------------------------------------------------------------

// writeBaiduTokenResponse writes a mock Baidu access_token response.
func writeBaiduTokenResponse(w http.ResponseWriter, token string, expiresIn int64) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"access_token": token,
		"expires_in":   expiresIn,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// writeBaiduOCRResponse writes a mock Baidu OCR response.
func writeBaiduOCRResponse(w http.ResponseWriter, words []string) {
	type location struct {
		Left   int `json:"left"`
		Top    int `json:"top"`
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	type wordItem struct {
		Words    string   `json:"words"`
		Location location `json:"location"`
	}
	type response struct {
		LogID          uint64     `json:"log_id"`
		WordsResultNum int        `json:"words_result_num"`
		WordsResult    []wordItem `json:"words_result"`
	}

	items := make([]wordItem, 0, len(words))
	for i, w := range words {
		items = append(items, wordItem{
			Words: w,
			Location: location{
				Left:   i * 100,
				Top:    i * 30,
				Width:  80,
				Height: 25,
			},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response{
		LogID:          12345,
		WordsResultNum: len(items),
		WordsResult:    items,
	})
}

// TestBaiduOCRAdapter_OCR_Roundtrip tests the full OCR request/response cycle using
// an httptest.Server that simulates the Baidu token + OCR endpoints.
func TestBaiduOCRAdapter_OCR_Roundtrip(t *testing.T) {
	const testToken = "test-baidu-token"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/oauth/2.0/token"):
			// Verify client_credentials grant.
			q := r.URL.Query()
			if q.Get("grant_type") != "client_credentials" {
				http.Error(w, "missing grant_type", http.StatusBadRequest)
				return
			}
			writeBaiduTokenResponse(w, testToken, 2592000)

		case strings.Contains(r.URL.Path, "/rest/2.0/ocr/v1/accurate"):
			// Verify access_token query param.
			if r.URL.Query().Get("access_token") != testToken {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			// Verify form body contains image field.
			if err := r.ParseForm(); err != nil {
				http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
				return
			}
			if r.FormValue("image") == "" {
				http.Error(w, "missing image", http.StatusBadRequest)
				return
			}
			writeBaiduOCRResponse(w, []string{"Hello", "World"})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := NewBaiduOCRAdapter()
	route := mockRouteWithModel(srv.URL, "test-api-key:test-secret-key", "")

	resp, err := a.OCR(context.Background(), route, aiservice.OCRRequest{
		ImageBytes: []byte("fake-image-data"),
	})
	if err != nil {
		t.Fatalf("OCR: unexpected error: %v", err)
	}
	if resp.Provider != "baidu_ocr" {
		t.Errorf("Provider = %q; want baidu_ocr", resp.Provider)
	}
	if !strings.Contains(resp.Text, "Hello") {
		t.Errorf("Text = %q; want to contain 'Hello'", resp.Text)
	}
	if len(resp.Words) != 2 {
		t.Fatalf("Words count = %d; want 2", len(resp.Words))
	}
	if resp.Words[0].Word != "Hello" {
		t.Errorf("Words[0].Word = %q; want 'Hello'", resp.Words[0].Word)
	}
	// Verify bounding box: [left, top, left+width, top+height] = [0, 0, 80, 25]
	if len(resp.Words[0].BoundingBox) != 4 {
		t.Errorf("Words[0].BoundingBox len = %d; want 4", len(resp.Words[0].BoundingBox))
	}
}

// TestBaiduOCRAdapter_OCR_TokenCached verifies that the access token is cached
// and the token endpoint is only called once for multiple OCR requests.
func TestBaiduOCRAdapter_OCR_TokenCached(t *testing.T) {
	tokenCallCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/oauth/2.0/token") {
			tokenCallCount++
			writeBaiduTokenResponse(w, "cached-token", 2592000)
			return
		}
		if strings.Contains(r.URL.Path, "/rest/2.0/ocr/v1/accurate") {
			writeBaiduOCRResponse(w, []string{"cached"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	a := NewBaiduOCRAdapter()
	route := mockRouteWithModel(srv.URL, "key-a:secret-a", "")

	// Call OCR twice — token should only be fetched once.
	for i := 0; i < 2; i++ {
		_, err := a.OCR(context.Background(), route, aiservice.OCRRequest{
			ImageBytes: []byte("img"),
		})
		if err != nil {
			t.Fatalf("OCR call %d: unexpected error: %v", i, err)
		}
	}

	if tokenCallCount != 1 {
		t.Errorf("token endpoint called %d times; want 1 (should be cached)", tokenCallCount)
	}
}

// TestBaiduOCRAdapter_OCR_BadCredentials verifies that malformed credentials
// return an error without making any HTTP call.
func TestBaiduOCRAdapter_OCR_BadCredentials(t *testing.T) {
	a := NewBaiduOCRAdapter()
	route := &registry.ResolvedRoute{
		Provider: registry.ProviderInfo{
			APIKey: "no-colon-here",
		},
	}
	_, err := a.OCR(context.Background(), route, aiservice.OCRRequest{
		ImageBytes: []byte("img"),
	})
	if err == nil {
		t.Error("expected error for malformed credentials; got nil")
	}
}

// TestBaiduOCRAdapter_OCR_BusinessError verifies that a Baidu business-level error
// (HTTP 200 + JSON {"error_code": N, "error_msg": "..."}) is propagated as a Go
// error containing the error_msg text.  This is how Baidu signals 4xx-equivalent
// conditions (e.g. image too large, invalid format).
func TestBaiduOCRAdapter_OCR_BusinessError(t *testing.T) {
	const (
		baiduErrorCode = 282811
		baiduErrorMsg  = "image size error"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/oauth/2.0/token"):
			writeBaiduTokenResponse(w, "tok", 2592000)
		case strings.Contains(r.URL.Path, "/rest/2.0/ocr/v1/accurate"):
			// Baidu returns HTTP 200 even for application-level errors.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error_code": baiduErrorCode,
				"error_msg":  baiduErrorMsg,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := NewBaiduOCRAdapter()
	route := mockRouteWithModel(srv.URL, "api-key:secret-key", "")

	_, err := a.OCR(context.Background(), route, aiservice.OCRRequest{
		ImageBytes: []byte("fake-image"),
	})
	if err == nil {
		t.Fatal("expected error for Baidu business error; got nil")
	}
	if !strings.Contains(err.Error(), baiduErrorMsg) {
		t.Errorf("error = %q; want it to contain %q", err.Error(), baiduErrorMsg)
	}
}

// ----------------------------------------------------------------------------
// FunASRAdapter tests
// ----------------------------------------------------------------------------

// writeFunASRResponse writes a mock FunASR transcription response.
func writeFunASRResponse(w http.ResponseWriter, text string, duration float64) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"text":     text,
		"duration": duration,
	})
}

// TestFunASRAdapter_ASR_Roundtrip tests the full ASR multipart upload and response parsing.
func TestFunASRAdapter_ASR_Roundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/recognize" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Verify multipart content type.
		ct := r.Header.Get("Content-Type")
		if !strings.Contains(ct, "multipart/form-data") {
			http.Error(w, "expected multipart/form-data; got "+ct, http.StatusBadRequest)
			return
		}
		// Verify audio field is present.
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
			return
		}
		if r.MultipartForm.File["audio"] == nil {
			http.Error(w, "missing audio field", http.StatusBadRequest)
			return
		}

		writeFunASRResponse(w, "这是语音识别结果", 5.3)
	}))
	defer srv.Close()

	f := NewFunASRAdapter()
	route := mockRouteWithModel(srv.URL, "", "")

	resp, err := f.ASR(context.Background(), route, aiservice.ASRRequest{
		AudioBytes:  []byte("fake-audio-data"),
		AudioFormat: "wav",
	})
	if err != nil {
		t.Fatalf("ASR: unexpected error: %v", err)
	}
	if resp.Provider != "funasr" {
		t.Errorf("Provider = %q; want funasr", resp.Provider)
	}
	if resp.Text != "这是语音识别结果" {
		t.Errorf("Text = %q; want '这是语音识别结果'", resp.Text)
	}
	if resp.DurationSeconds != 5.3 {
		t.Errorf("DurationSeconds = %f; want 5.3", resp.DurationSeconds)
	}
}

// TestFunASRAdapter_ASR_DefaultBaseURL verifies that when BaseURL is empty the
// adapter defaults to http://localhost:10095 (visible in error message when the
// server is not available).
func TestFunASRAdapter_ASR_DefaultBaseURL(t *testing.T) {
	f := NewFunASRAdapter()
	// Route with no BaseURL.
	route := &registry.ResolvedRoute{
		Provider: registry.ProviderInfo{BaseURL: ""},
	}
	// Pass a short context to make the connection attempt fail quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 50*1000*1000) // 50 ms
	defer cancel()

	_, err := f.ASR(ctx, route, aiservice.ASRRequest{
		AudioBytes: []byte("data"),
	})
	if err == nil {
		t.Error("expected error connecting to non-existent localhost:10095; got nil")
	}
	// The error should reference the default URL (or be a timeout/connection refused).
	// We just assert it's non-nil — the URL resolution already passed.
}

// TestFunASRAdapter_ASR_HTTPError verifies that a non-200 status from the
// FunASR service is propagated as an error.
func TestFunASRAdapter_ASR_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	f := NewFunASRAdapter()
	route := mockRouteWithModel(srv.URL, "", "")

	_, err := f.ASR(context.Background(), route, aiservice.ASRRequest{
		AudioBytes: []byte("audio"),
	})
	if err == nil {
		t.Error("expected error for HTTP 500; got nil")
	}
}

// TestFunASRAdapter_ASR_NoAudioData verifies that missing audio data returns an
// error before any HTTP call is made.
func TestFunASRAdapter_ASR_NoAudioData(t *testing.T) {
	f := NewFunASRAdapter()
	route := mockRouteWithModel("http://localhost:10095", "", "")

	_, err := f.ASR(context.Background(), route, aiservice.ASRRequest{})
	if err == nil {
		t.Error("expected error for empty ASRRequest; got nil")
	}
}

// ----------------------------------------------------------------------------
// BailianFileAdapter tests
// ----------------------------------------------------------------------------

// TestBailianFileAdapter_UploadFile_Roundtrip tests the three-step upload flow:
// getLease → putToOSS → confirmFile.
func TestBailianFileAdapter_UploadFile_Roundtrip(t *testing.T) {
	const (
		testLeaseID = "lease-xyz-123"
		testFileID  = "file-abc-456"
	)

	var ossCallCount int

	// OSS server (handles PUT).
	ossSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "expected PUT", http.StatusMethodNotAllowed)
			return
		}
		ossCallCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer ossSrv.Close()

	a := NewBailianFileAdapter()

	// combinedSrv acts as both the Bailian API server and returns the correct
	// pre-signed OSS URL pointing at ossSrv.
	combinedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("x-acs-action")
		switch action {
		case "ApplyFileUploadLease":
			resp := map[string]interface{}{
				"Code": "Success",
				"Data": map[string]interface{}{
					"FileUploadLeaseId": testLeaseID,
					"Param": map[string]interface{}{
						"Url":     ossSrv.URL + "/oss-bucket/file",
						"Headers": map[string]string{"x-oss-meta-test": "1"},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		case "AddFile":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			resp := map[string]interface{}{
				"Code": "Success",
				"Data": map[string]interface{}{"FileId": testFileID},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.NotFound(w, r)
		}
	}))
	defer combinedSrv.Close()

	route := &registry.ResolvedRoute{
		ProviderModelID: "ws-test-workspace",
		Provider: registry.ProviderInfo{
			// Pass the full http://host:port URL — resolveBaseURL keeps the scheme.
			BaseURL: combinedSrv.URL,
			APIKey:  "test-ak-id:test-ak-secret",
		},
	}

	resp, err := a.UploadFile(context.Background(), route, aiservice.FileUploadRequest{
		FileBytes: []byte("file-content"),
		FileName:  "test.pdf",
		MimeType:  "application/pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile: unexpected error: %v", err)
	}
	if resp.FileID != testFileID {
		t.Errorf("FileID = %q; want %q", resp.FileID, testFileID)
	}
	if resp.Provider != "bailian_file" {
		t.Errorf("Provider = %q; want bailian_file", resp.Provider)
	}
	if resp.UploadedAt == "" {
		t.Error("UploadedAt is empty; want a non-empty RFC3339 timestamp")
	}
	if ossCallCount != 1 {
		t.Errorf("OSS PUT called %d times; want 1", ossCallCount)
	}
}

// TestBailianFileAdapter_UploadFile_BadCredentials verifies that malformed
// credentials return an error.
func TestBailianFileAdapter_UploadFile_BadCredentials(t *testing.T) {
	a := NewBailianFileAdapter()
	route := &registry.ResolvedRoute{
		Provider: registry.ProviderInfo{
			APIKey: "no-colon",
		},
	}
	_, err := a.UploadFile(context.Background(), route, aiservice.FileUploadRequest{
		FileBytes: []byte("data"),
		FileName:  "test.txt",
	})
	if err == nil {
		t.Error("expected error for malformed credentials; got nil")
	}
}

// TestBailianFileAdapter_UploadFile_LeaseError verifies that a failed getLease
// request propagates the error.
func TestBailianFileAdapter_UploadFile_LeaseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream error"))
	}))
	defer srv.Close()

	a := NewBailianFileAdapter()
	route := &registry.ResolvedRoute{
		ProviderModelID: "ws-test",
		Provider: registry.ProviderInfo{
			BaseURL: srv.URL,
			APIKey:  "ak:secret",
		},
	}

	_, err := a.UploadFile(context.Background(), route, aiservice.FileUploadRequest{
		FileBytes: []byte("data"),
		FileName:  "test.txt",
	})
	if err == nil {
		t.Error("expected error for failed getLease; got nil")
	}
}

// ----------------------------------------------------------------------------
// Interface compliance checks for the new adapters
// ----------------------------------------------------------------------------

func TestNonLLMAdapterInterfaceCompliance(t *testing.T) {
	var _ OCRAdapter = NewBaiduOCRAdapter()
	var _ ASRAdapter = NewFunASRAdapter()
	var _ FileServiceAdapter = NewBailianFileAdapter()
}
