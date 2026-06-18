package voiceprint

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// makeEmbedding returns a length-n []float32 with distinct, non-zero values.
func makeEmbedding(n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(i)*0.001 + 0.1
	}
	return out
}

// TestEmbed_HappyPath: server returns a valid 192-d embedding → Valid=true,
// embedding round-trips, request body carries base64 PCM + session/segment.
func TestEmbed_HappyPath(t *testing.T) {
	wantEmb := makeEmbedding(EmbeddingDim)
	pcm := []byte{0x01, 0x02, 0x03, 0x04}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method %q", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var req embedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("server: decode request: %v", err)
		}
		if req.SessionID != "sess-1" {
			t.Errorf("session_id = %q, want sess-1", req.SessionID)
		}
		if req.SegmentID != 42 {
			t.Errorf("segment_id = %d, want 42", req.SegmentID)
		}
		gotPCM, err := base64.StdEncoding.DecodeString(req.AudioB64)
		if err != nil {
			t.Fatalf("server: decode pcm base64: %v", err)
		}
		if string(gotPCM) != string(pcm) {
			t.Errorf("pcm bytes = %v, want %v", gotPCM, pcm)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedResponse{Embedding: wantEmb, Valid: true})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Embed(context.Background(), pcm, "sess-1", 42)
	if err != nil {
		t.Fatalf("Embed returned error on happy path: %v", err)
	}
	if !res.Valid {
		t.Fatalf("Embed Valid=false on happy path, reason=%q", res.Reason)
	}
	if len(res.Embedding) != EmbeddingDim {
		t.Fatalf("embedding dim = %d, want %d", len(res.Embedding), EmbeddingDim)
	}
	for i := range wantEmb {
		if res.Embedding[i] != wantEmb[i] {
			t.Fatalf("embedding[%d] = %v, want %v", i, res.Embedding[i], wantEmb[i])
		}
	}
}

// TestEmbed_Server500: server returns 500 → Valid=false AND non-fatal (nil) err.
// The meeting must continue; the caller branches on Valid, not on err.
func TestEmbed_Server500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Embed(context.Background(), []byte{0xAA}, "sess-1", 7)
	if err != nil {
		t.Fatalf("Embed returned NON-NIL error on 5xx (must be soft): %v", err)
	}
	if res.Valid {
		t.Fatalf("Embed Valid=true on 5xx, want false")
	}
	if res.Reason == "" {
		t.Errorf("expected a degradation Reason on 5xx, got empty")
	}
}

// TestEmbed_Timeout: server hangs past the embed timeout → Valid=false AND
// non-fatal (nil) err. This is the core soft-error contract: a slow/unreachable
// voiceprint service must never kill the relay.
func TestEmbed_Timeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the connection open until the test releases it
	}))
	defer srv.Close()
	defer close(release)

	c := NewClient(srv.URL)
	// Bound the test with a context shorter than defaultEmbedTimeout so it stays fast.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := c.Embed(ctx, []byte{0x01}, "sess-1", 1)
	if err != nil {
		t.Fatalf("Embed returned NON-NIL error on timeout (must be soft): %v", err)
	}
	if res.Valid {
		t.Fatalf("Embed Valid=true on timeout, want false")
	}
	if res.Reason == "" {
		t.Errorf("expected a degradation Reason on timeout, got empty")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Embed took %v on timeout — expected to fail fast", elapsed)
	}
}

// TestEmbed_NotConfigured: empty base URL → soft skip (Valid=false, nil err).
func TestEmbed_NotConfigured(t *testing.T) {
	c := NewClient("")
	res, err := c.Embed(context.Background(), []byte{0x01}, "s", 1)
	if err != nil {
		t.Fatalf("Embed not-configured returned error (must be soft): %v", err)
	}
	if res.Valid {
		t.Fatalf("Embed Valid=true when not configured, want false")
	}
}

// TestEmbed_WrongDim: server returns a non-192 embedding → Valid=false, nil err.
func TestEmbed_WrongDim(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(embedResponse{Embedding: makeEmbedding(64), Valid: true})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Embed(context.Background(), []byte{0x01}, "s", 1)
	if err != nil {
		t.Fatalf("Embed wrong-dim returned error (must be soft): %v", err)
	}
	if res.Valid {
		t.Fatalf("Embed Valid=true on wrong dim, want false")
	}
}

// TestEmbed_ServiceFlagsInvalid: server returns HTTP 200 but a body explicitly
// flagging the segment as unusable ("valid":false, empty embedding) → Valid=false,
// nil err. Covers the hot-path branch in client.go where the SERVICE itself
// signals "too short / silent to embed" (a non-error soft skip) rather than a
// transport/decode failure.
func TestEmbed_ServiceFlagsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 200 OK + the documented service-invalid shape.
		_, _ = w.Write([]byte(`{"valid":false,"embedding":[],"dim":192,"duration_ms":200}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Embed(context.Background(), []byte{0x01}, "s", 1)
	if err != nil {
		t.Fatalf("Embed returned NON-NIL error on service-invalid (must be soft): %v", err)
	}
	if res.Valid {
		t.Fatalf("Embed Valid=true when service reported invalid, want false")
	}
	if res.Reason == "" {
		t.Errorf("expected a degradation Reason on service-invalid, got empty")
	}
}

// TestEmbed_MalformedResponseBody: server returns HTTP 200 but a non-JSON body →
// decode fails → Valid=false, nil err, Reason mentions decode (soft-degrade).
func TestEmbed_MalformedResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json-at-all <<<`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Embed(context.Background(), []byte{0x01}, "s", 1)
	if err != nil {
		t.Fatalf("Embed returned NON-NIL error on malformed body (must be soft): %v", err)
	}
	if res.Valid {
		t.Fatalf("Embed Valid=true on malformed body, want false")
	}
	if !strings.Contains(res.Reason, "decode") {
		t.Errorf("Reason = %q, want it to mention 'decode'", res.Reason)
	}
}

// TestEmbed_NilClient: calling Embed on a nil *Client → non-nil err (usage fault),
// Valid=false. A nil error is reserved for runtime outages; a nil receiver is a
// programmer fault and must surface as an error.
func TestEmbed_NilClient(t *testing.T) {
	var c *Client
	res, err := c.Embed(context.Background(), []byte{0x01}, "s", 1)
	if err == nil {
		t.Fatalf("Embed on nil client returned nil error, want non-nil (usage fault)")
	}
	if res == nil || res.Valid {
		t.Fatalf("Embed on nil client: want non-nil result with Valid=false, got %+v", res)
	}
}

// TestDiarize_HappyPath: server returns cluster assignments → parsed correctly,
// request carries audio_url + segments.
func TestDiarize_HappyPath(t *testing.T) {
	segments := []Segment{
		{SegmentID: 1, StartMs: 0, EndMs: 1000},
		{SegmentID: 2, StartMs: 1000, EndMs: 2500},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/diarize" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req diarizeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("server: decode request: %v", err)
		}
		if req.AudioURL != "https://cos.example/full.webm" {
			t.Errorf("audio_url = %q", req.AudioURL)
		}
		if len(req.Segments) != 2 || req.Segments[1].SegmentID != 2 {
			t.Errorf("segments not round-tripped: %+v", req.Segments)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DiarizeResult{
			SpeakerCount: 2,
			Segments: []SegmentSpeaker{
				{SegmentID: 1, ClusterID: 0, Confidence: 0.9},
				{SegmentID: 2, ClusterID: 1, Confidence: 0.8},
			},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Diarize(context.Background(), "https://cos.example/full.webm", "sess-1", segments)
	if err != nil {
		t.Fatalf("Diarize happy path error: %v", err)
	}
	if res.SpeakerCount != 2 {
		t.Errorf("speaker_count = %d, want 2", res.SpeakerCount)
	}
	if len(res.Segments) != 2 || res.Segments[1].ClusterID != 1 {
		t.Errorf("segments wrong: %+v", res.Segments)
	}
}

// TestDiarize_Server500: offline path follows conventional Go contract —
// failure returns a non-nil error (caller decides retry/fallback).
func TestDiarize_Server500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	res, err := c.Diarize(context.Background(), "https://cos.example/full.webm", "s", nil)
	if err == nil {
		t.Fatalf("Diarize on 5xx returned nil error, want non-nil (offline conventional contract)")
	}
	if res != nil {
		t.Errorf("Diarize on 5xx returned non-nil result: %+v", res)
	}
}

// TestDiarize_NotConfigured: empty base URL → non-nil error.
func TestDiarize_NotConfigured(t *testing.T) {
	c := NewClient("")
	if _, err := c.Diarize(context.Background(), "u", "s", nil); err == nil {
		t.Fatalf("Diarize not-configured returned nil error, want non-nil")
	}
}
