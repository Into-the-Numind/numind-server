package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/parser"
)

// docFetchTimeout bounds the HTTP GET used to download a document before local parsing.
// NOTE: kept in sync with attachment/fallback_service.go docDownloadTimeout.
const docFetchTimeout = 60 * time.Second

// docFetchMaxBytes caps the document download size (matches the 20MB upload cap).
// NOTE: kept in sync with attachment/fallback_service.go maxDocumentBytes.
const docFetchMaxBytes = 20 * 1024 * 1024

// documentParserImpl downloads a file and extracts plain text locally via the
// shared parser.DocumentParser (pdf/docx/doc/rtf/txt/md/html/xlsx/pptx — the
// same engine SOP uses; docx is pure-Go, xlsx/pptx use MarkItDown, doc uses
// antiword, pdf uses go-fitz, all baked into the server image).
//
// This replaces the previous bare-URL→qwen-long PDF path, which never worked:
// qwen-long cannot fetch a presigned COS URL, so it produced hallucinated or
// refusal output (dev run 104, 2026-06-08). Local extraction is deterministic,
// zero-cost, and cross-border-free.
type documentParserImpl struct{}

func (p *documentParserImpl) Parse(ctx context.Context, fileURL, _ string) (string, int, bool, error) {
	var spanID, traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(traceID, spanID, "tool.file_read.document",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{"file_url": fileURL}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", 0, false, fmt.Errorf("build GET request: %w", err)
	}
	httpClient := &http.Client{Timeout: docFetchTimeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("http GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", 0, false, fmt.Errorf("document fetch: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, docFetchMaxBytes))
	if err != nil {
		return "", 0, false, fmt.Errorf("read body: %w", err)
	}

	// Filename drives DocumentParser's extension dispatch; strip query/fragment.
	filename := path.Base(fileURL)
	if i := strings.IndexAny(filename, "?#"); i != -1 {
		filename = filename[:i]
	}

	text, err := parser.NewDocumentParser().Parse(ctx, bytes.NewReader(data), filename)
	if err != nil {
		return "", 0, false, err
	}
	truncated := len(text) > fileReadMaxBytes
	if truncated {
		text = text[:fileReadMaxBytes]
	}
	return text, 0, truncated, nil
}

// imageParserImpl uses aiservice.OCR to extract text from images.
type imageParserImpl struct{}

func (p *imageParserImpl) Parse(ctx context.Context, fileURL, _ string) (string, int, bool, error) {
	var spanID, traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(traceID, spanID, "tool.file_read.ocr",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{"file_url": fileURL}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	resp, err := aiservice.OCR(ctx, profile.OcrBaidu, aiservice.OCRRequest{
		ImageURL: fileURL,
	})
	if err != nil {
		return "", 0, false, fmt.Errorf("aiservice.OCR: %w", err)
	}

	content := resp.Text
	truncated := len(content) > fileReadMaxBytes
	if truncated {
		content = content[:fileReadMaxBytes]
	}
	return content, 0, truncated, nil
}

// textParserImpl reads text/plain or text/markdown content directly via HTTP GET,
// capping the download at fileReadMaxBytes+1 bytes to detect truncation.
type textParserImpl struct{}

func (p *textParserImpl) Parse(ctx context.Context, fileURL, _ string) (string, int, bool, error) {
	var spanID, traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(traceID, spanID, "tool.file_read.direct",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{"file_url": fileURL}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", 0, false, fmt.Errorf("build GET request: %w", err)
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("http GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", 0, false, fmt.Errorf("text parser: HTTP %d", resp.StatusCode)
	}

	// Read at most fileReadMaxBytes+1 bytes so we can detect truncation.
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(fileReadMaxBytes)+1))
	if err != nil {
		return "", 0, false, fmt.Errorf("read body: %w", err)
	}

	truncated := len(body) > fileReadMaxBytes
	if truncated {
		body = body[:fileReadMaxBytes]
	}
	return string(body), 0, truncated, nil
}
