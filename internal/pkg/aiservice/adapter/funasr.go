package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/httpclient"
)

// Compile-time interface check.
var _ ASRAdapter = (*FunASRAdapter)(nil)

// funasrResponse is the response returned by the FunASR HTTP service.
type funasrResponse struct {
	// Text is the full transcription result.
	Text string `json:"text"`
	// Duration is the audio duration in seconds (may not always be populated).
	Duration float64 `json:"duration,omitempty"`
}

// FunASRAdapter implements ASRAdapter using the local FunASR HTTP service.
//
// The FunASR service is a self-hosted local HTTP endpoint.  The adapter sends
// audio bytes as multipart/form-data and parses the JSON transcription response.
//
// ServiceSpec:
//
//	route.Provider.BaseURL — full base URL of the FunASR service
//	                         (e.g. "http://localhost:10095").  Defaults to
//	                         "http://localhost:10095" when empty.
type FunASRAdapter struct {
	client *httpclient.Client
}

// NewFunASRAdapter creates a FunASRAdapter backed by the shared httpclient pool.
func NewFunASRAdapter() *FunASRAdapter {
	return &FunASRAdapter{
		client: httpclient.NewClient(nil),
	}
}

// Name returns the adapter identifier.
func (f *FunASRAdapter) Name() string { return "funasr" }

// ProviderType returns the provider category.
func (f *FunASRAdapter) ProviderType() string { return "funasr" }

// Capabilities lists the capabilities this adapter supports.
func (f *FunASRAdapter) Capabilities() []string { return []string{"asr"} }

// ASR transcribes audio bytes using the FunASR HTTP service.
//
// The audio file is sent as a multipart form upload to POST /recognize.
// Either ASRRequest.AudioBytes or ASRRequest.AudioURL must be provided.
func (f *FunASRAdapter) ASR(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ASRRequest) (*aiservice.ASRResponse, error) {
	audioData, audioName, err := f.resolveAudioData(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("funasr.ASR: resolve audio: %w", err)
	}

	baseURL := strings.TrimRight(route.Provider.BaseURL, "/")
	if baseURL == "" {
		baseURL = "http://localhost:10095"
	}
	recognizeURL := baseURL + "/recognize"

	// Build multipart/form-data body.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("audio", audioName)
	if err != nil {
		return nil, fmt.Errorf("funasr.ASR: create form file: %w", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(audioData)); err != nil {
		return nil, fmt.Errorf("funasr.ASR: copy audio data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("funasr.ASR: close multipart writer: %w", err)
	}

	resp, err := f.client.Do(&httpclient.Request{
		Method:  "POST",
		URL:     recognizeURL,
		Body:    &buf,
		Context: ctx,
		Headers: map[string]string{
			"Content-Type": writer.FormDataContentType(),
		},
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return nil, wrapHTTPClientErr("funasr.ASR POST", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr("funasr.ASR POST", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("funasr.ASR: read response: %w", err)
	}

	var result funasrResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("funasr.ASR: decode response: %w", err)
	}

	return &aiservice.ASRResponse{
		Text:            result.Text,
		DurationSeconds: result.Duration,
		Provider:        f.Name(),
	}, nil
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// resolveAudioData fetches or uses the provided audio bytes and derives a
// sensible file name for the multipart upload.
func (f *FunASRAdapter) resolveAudioData(ctx context.Context, req aiservice.ASRRequest) (data []byte, name string, err error) {
	ext := req.AudioFormat
	if ext == "" {
		ext = "wav"
	}
	name = "audio." + ext

	if req.AudioURL != "" {
		resp, err := f.client.Do(&httpclient.Request{
			Method:      "GET",
			URL:         req.AudioURL,
			Context:     ctx,
			RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 1},
		})
		if err != nil {
			return nil, "", wrapHTTPClientErr("resolveAudioData download", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, "", wrapHTTPStatusErr("resolveAudioData download", resp.StatusCode, body)
		}
		data, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, "", fmt.Errorf("resolveAudioData: read body: %w", err)
		}
		return data, name, nil
	}

	if len(req.AudioBytes) > 0 {
		return req.AudioBytes, name, nil
	}

	return nil, "", fmt.Errorf("ASRRequest must provide either AudioURL or AudioBytes")
}
