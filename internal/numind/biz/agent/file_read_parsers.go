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

func (p *documentParserImpl) Parse(ctx context.Context, fileURL, _ string) (content string, pageCount int, truncated bool, err error) {
	span := startSafePipelineToolSpan(ctx, "tool.file_read.document", map[string]any{"parser_kind": "document"})
	defer func() {
		errorClass := pipelineToolTraceNoError
		if err != nil {
			errorClass = "parser_error"
		}
		span.End(map[string]any{"returned_bytes": len(content)}, errorClass)
	}()

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
	data, err := io.ReadAll(io.LimitReader(resp.Body, docFetchMaxBytes+1))
	if err != nil {
		return "", 0, false, fmt.Errorf("read body: %w", err)
	}
	if len(data) > docFetchMaxBytes {
		return "", 0, false, fmt.Errorf("document exceeds 20 MiB download limit")
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
	return text, 0, false, nil
}

// imageParserImpl uses aiservice.OCR to extract text from images.
type imageParserImpl struct{}

var fileReadOCRFn = aiservice.OCR

func (p *imageParserImpl) Parse(ctx context.Context, fileURL, _ string) (content string, pageCount int, truncated bool, err error) {
	span := startSafePipelineToolSpan(ctx, "tool.file_read.ocr", map[string]any{"parser_kind": "ocr"})
	defer func() {
		errorClass := pipelineToolTraceNoError
		if err != nil {
			errorClass = "parser_error"
		}
		span.End(map[string]any{"returned_bytes": len(content)}, errorClass)
	}()

	resp, err := fileReadOCRFn(ctx, profile.OcrBaidu, aiservice.OCRRequest{
		ImageURL: fileURL,
	})
	if err != nil {
		return "", 0, false, fmt.Errorf("aiservice.OCR: %w", err)
	}

	return resp.Text, 0, false, nil
}

// textParserImpl reads text/plain or text/markdown content directly via HTTP GET.
// The 20 MiB + 1 read detects and rejects oversized sources without truncation.
type textParserImpl struct{}

func (p *textParserImpl) Parse(ctx context.Context, fileURL, _ string) (content string, pageCount int, truncated bool, err error) {
	span := startSafePipelineToolSpan(ctx, "tool.file_read.direct", map[string]any{"parser_kind": "direct"})
	defer func() {
		errorClass := pipelineToolTraceNoError
		if err != nil {
			errorClass = "parser_error"
		}
		span.End(map[string]any{"returned_bytes": len(content)}, errorClass)
	}()

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

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(docFetchMaxBytes)+1))
	if err != nil {
		return "", 0, false, fmt.Errorf("read body: %w", err)
	}
	if len(body) > docFetchMaxBytes {
		return "", 0, false, fmt.Errorf("text file exceeds 20 MiB download limit")
	}
	return string(body), 0, false, nil
}
