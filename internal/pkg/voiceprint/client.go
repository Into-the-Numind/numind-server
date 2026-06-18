// Package voiceprint is a thin HTTP client for a self-hosted voiceprint
// (speaker-embedding / diarization) micro-service used by the meeting-copilot
// speaker-diarization feature. The service runs CAM++ on onnxruntime CPU and
// exposes:
//
//	POST {base}/embed   — PCM (16k mono, base64) → a 192-d L2-normalized embedding
//	POST {base}/diarize — a full recording (audio_url) + known segments → per-segment
//	                      cluster assignments via server-side VAD + sliding-window
//	                      embedding + global AHC.
//
// It is NOT an LLM/AI-model call, so it deliberately lives outside the aiservice
// gateway — it is plain inference infrastructure, sibling to crawl4ai and
// internal/pkg/httpclient (see DIARIZATION_SPEC.md §5 D1).
//
// SOFT-ERROR CONTRACT (DIARIZATION_SPEC.md §4 P1 — the load-bearing invariant of
// this package): voiceprint MUST NEVER kill a meeting. The relay forwarding ASR
// to the user is the priority; speaker attribution is a best-effort enrichment.
// Therefore Embed treats *any* transport failure (timeout, 5xx, unreachable host,
// malformed body) as a soft degradation: it returns an EmbedResult with
// Valid==false and a nil error. Callers branch on EmbedResult.Valid, NOT on err —
// a non-nil error from Embed signals only a programmer/usage fault (e.g. nil
// client), never a runtime service outage. When Embed returns Valid==false the
// online clustering loop simply leaves online_speaker_id NULL for that segment
// and the meeting carries on uninterrupted.
//
// Diarize is the OFFLINE refinement path (post-meeting, not in the hot relay
// loop), so it follows the conventional Go contract: it returns a non-nil error
// on failure and the caller decides whether to retry or fall back. It still
// never panics.
package voiceprint

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/pkg/httpclient"
)

// EmbeddingDim is the fixed dimensionality of a CAM++ speaker embedding.
const EmbeddingDim = 192

const (
	defaultEmbedTimeout   = 5 * time.Second
	defaultDiarizeTimeout = 120 * time.Second
)

// EmbedResult is the outcome of an /embed call.
//
// Valid is the soft-error signal: when false, the embedding was NOT obtained
// (service timed out / errored / returned garbage) and the caller must skip
// speaker attribution for this segment. Embedding is meaningful only when
// Valid is true.
type EmbedResult struct {
	Valid     bool      // false => soft-degraded; caller skips attribution, meeting continues
	Embedding []float32 // length EmbeddingDim (192) when Valid; nil otherwise
	Reason    string    // human-readable degradation cause (for logs/metrics); empty on success
}

// Segment is one known transcript segment fed to /diarize so the service can
// align its diarization output to existing meeting_segment rows.
type Segment struct {
	SegmentID int64 `json:"segment_id"`
	StartMs   int64 `json:"start_ms"`
	EndMs     int64 `json:"end_ms"`
}

// SegmentSpeaker is one element of a DiarizeResult: a segment's assigned cluster.
type SegmentSpeaker struct {
	SegmentID  int64   `json:"segment_id"`
	ClusterID  int     `json:"cluster_id"`
	Confidence float32 `json:"confidence"`
}

// DiarizeResult is the outcome of a successful /diarize call.
type DiarizeResult struct {
	SpeakerCount int              `json:"speaker_count"`
	Segments     []SegmentSpeaker `json:"segments"`
}

// Client calls a voiceprint service's /embed and /diarize endpoints.
//
// Two underlying http clients: embedHTTP has a tight 5s total timeout (hot relay
// path, fail-fast soft-degrade); diarizeHTTP has a generous 120s timeout because
// offline re-clustering is heavy (server-side VAD + sliding-window embedding +
// global AHC). A single shared client cannot serve both — the 5s embed cap would
// prematurely abort a legitimate 30s diarize.
type Client struct {
	baseURL     string
	embedHTTP   *httpclient.Client
	diarizeHTTP *httpclient.Client
}

// NewClient builds a Client for the given voiceprint service base URL. The URL
// is a constructor parameter (not read from config here) so callers — including
// tests pointing at an httptest server — control it explicitly; deploy-time
// wiring reads voiceprint.base_url from viper and passes it in.
//
// MaxRetries=0 on both: a voiceprint failure must degrade fast (soft error), not
// stack retries that delay the relay; the connection pool still helps the steady
// per-segment embed cadence.
func NewClient(baseURL string) *Client {
	embedHTTP := httpclient.NewClient(&httpclient.Config{
		Timeout:               defaultEmbedTimeout,
		ConnectTimeout:        2 * time.Second,
		ResponseHeaderTimeout: defaultEmbedTimeout,
		TLSHandshakeTimeout:   2 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		MaxRetries:            0, // fail fast → soft-degrade, do not retry in the hot path
		EnableCompression:     true,
		UserAgent:             "numind-server/1.0 (voiceprint)",
	})
	diarizeHTTP := httpclient.NewClient(&httpclient.Config{
		Timeout:               defaultDiarizeTimeout,
		ConnectTimeout:        2 * time.Second,
		ResponseHeaderTimeout: defaultDiarizeTimeout,
		TLSHandshakeTimeout:   2 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		MaxRetries:            0,
		EnableCompression:     true,
		UserAgent:             "numind-server/1.0 (voiceprint)",
	})
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), embedHTTP: embedHTTP, diarizeHTTP: diarizeHTTP}
}

// Configured reports whether a voiceprint base URL is set. When false the caller
// must skip the voiceprint path entirely (feature disabled / not wired).
func (c *Client) Configured() bool {
	return c != nil && c.baseURL != ""
}

// embedRequest mirrors the voiceprint POST /embed request body.
type embedRequest struct {
	SessionID  string `json:"session_id"`
	SegmentID  int64  `json:"segment_id"`
	AudioB64   string `json:"audio_b64"`   // 16k mono s16le PCM, base64-encoded（与 VP /embed + DIARIZATION_SPEC §5 一致）
	SampleRate int    `json:"sample_rate"` // 16000
}

// embedResponse mirrors the voiceprint POST /embed response body.
type embedResponse struct {
	Embedding []float32 `json:"embedding"` // 192-d
	// Valid lets the service itself signal "this segment was too short / silent
	// to embed" (a non-error soft skip). Absent field defaults to false, so the
	// service must set it true on success.
	Valid bool `json:"valid"`
}

// Embed POSTs base64-encoded PCM to {base}/embed and returns a 192-d embedding.
//
// SOFT ERROR (see package doc / DIARIZATION_SPEC.md §4 P1): on ANY failure —
// not configured, transport error/timeout, non-2xx, body-read error, malformed
// JSON, wrong-dimension or service-flagged-invalid embedding — this returns an
// EmbedResult{Valid:false} and a NIL error. The meeting MUST continue; the
// caller branches on result.Valid, never on err. A non-nil error is reserved
// for a usage fault (nil client) and is itself non-fatal to the caller.
func (c *Client) Embed(ctx context.Context, pcm []byte, sessionID string, segmentID int64) (*EmbedResult, error) {
	if c == nil {
		return &EmbedResult{Valid: false, Reason: "nil client"}, fmt.Errorf("voiceprint: nil client")
	}
	if !c.Configured() {
		// Not an error: feature not wired ⇒ soft skip.
		return &EmbedResult{Valid: false, Reason: "not configured"}, nil
	}

	payload := embedRequest{
		SessionID:  sessionID,
		SegmentID:  segmentID,
		AudioB64:   base64.StdEncoding.EncodeToString(pcm),
		SampleRate: 16000,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		// Marshal of these primitives effectively cannot fail; treat as soft.
		return &EmbedResult{Valid: false, Reason: "marshal request: " + err.Error()}, nil
	}

	req := &httpclient.Request{
		Method:  http.MethodPost,
		URL:     c.baseURL + "/embed",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    bytes.NewReader(body),
		Context: ctx,
		// MaxRetries:0 (Do() defaults to 3 otherwise). RetryPolicy has no Delay field
		// here, but a zero delay is harmless: with MaxRetries=0 the retry loop never
		// runs, so the (absent) backoff delay is never consulted.
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	}

	resp, err := c.embedHTTP.Do(req)
	if err != nil {
		// Transport failure / timeout / unreachable — the whole reason this
		// contract exists. Soft-degrade, nil error.
		return &EmbedResult{Valid: false, Reason: "transport: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &EmbedResult{Valid: false, Reason: "read body: " + err.Error()}, nil
	}
	if resp.StatusCode >= 400 {
		// Includes 5xx — soft-degrade rather than kill the relay.
		return &EmbedResult{Valid: false, Reason: fmt.Sprintf("status %d: %s", resp.StatusCode, truncateForErr(respBytes))}, nil
	}

	var er embedResponse
	if err := json.Unmarshal(respBytes, &er); err != nil {
		return &EmbedResult{Valid: false, Reason: "decode response: " + err.Error()}, nil
	}
	if !er.Valid {
		// Service explicitly flagged the segment unusable (too short/silent).
		return &EmbedResult{Valid: false, Reason: "service reported invalid embedding"}, nil
	}
	if len(er.Embedding) != EmbeddingDim {
		return &EmbedResult{Valid: false, Reason: fmt.Sprintf("unexpected embedding dim %d (want %d)", len(er.Embedding), EmbeddingDim)}, nil
	}

	return &EmbedResult{Valid: true, Embedding: er.Embedding}, nil
}

// diarizeRequest mirrors the voiceprint POST /diarize request body.
type diarizeRequest struct {
	SessionID string    `json:"session_id"`
	AudioURL  string    `json:"audio_url"` // COS URL to the full recording (webm/opus); server transcodes via ffmpeg
	Segments  []Segment `json:"segments"`
}

// Diarize POSTs the full recording URL + known segments to {base}/diarize for
// offline global re-clustering and returns per-segment cluster assignments.
//
// This is the offline refinement path (not the hot relay loop), so it follows
// the conventional Go contract: any failure (not configured, transport, non-2xx,
// malformed body) returns a non-nil error and the caller decides retry/fallback.
// It never panics.
func (c *Client) Diarize(ctx context.Context, audioURL string, sessionID string, segments []Segment) (*DiarizeResult, error) {
	if c == nil || !c.Configured() {
		return nil, fmt.Errorf("voiceprint: not configured")
	}

	payload := diarizeRequest{SessionID: sessionID, AudioURL: audioURL, Segments: segments}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("voiceprint: marshal request: %w", err)
	}

	req := &httpclient.Request{
		Method:  http.MethodPost,
		URL:     c.baseURL + "/diarize",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    bytes.NewReader(body),
		Context: ctx,
		// Diarize is heavy (server-side VAD + sliding-window embedding + AHC) and
		// well past the default 5s embed timeout; diarizeHTTP allows up to 120s.
		// Honor the caller's ctx deadline for the overall cap.
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	}

	resp, err := c.diarizeHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voiceprint: http: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("voiceprint: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("voiceprint: status %d: %s", resp.StatusCode, truncateForErr(respBytes))
	}

	var dr DiarizeResult
	if err := json.Unmarshal(respBytes, &dr); err != nil {
		return nil, fmt.Errorf("voiceprint: decode response: %w", err)
	}
	return &dr, nil
}

// DiarizeTimeout returns the default offline diarization timeout, exported so
// callers can derive a context deadline without hardcoding the value.
func DiarizeTimeout() time.Duration { return defaultDiarizeTimeout }

// truncateForErr caps an error-embedded response body so logs/spans stay sane.
// It slices on a byte boundary then drops any trailing partial rune so the
// error string is always valid UTF-8.
func truncateForErr(b []byte) string {
	const max = 256
	if len(b) <= max {
		return string(b)
	}
	return strings.ToValidUTF8(string(b[:max]), "") + "…"
}
