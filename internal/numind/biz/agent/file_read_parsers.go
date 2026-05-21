package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
)

// pdfParserImpl uses aiservice.Chat (with ModelOverride qwen-long) to extract
// PDF content as Markdown. Full bailian file-upload wiring is deferred to T7
// (qwen-long requires a fileID from the DashScope file API; in v1 the URL is
// passed directly in the message body and the model attempts to fetch it).
// TODO(T7): wire through internal/service/bailian_http.go upload → fileID flow.
type pdfParserImpl struct{}

func (p *pdfParserImpl) Parse(ctx context.Context, fileURL, prompt string) (string, int, bool, error) {
	sysPrompt := "Read the file at the following URL and extract its full content as clean Markdown. Preserve headings, tables, and lists."
	if prompt != "" {
		sysPrompt = "Read the file at the following URL and " + prompt + ". Return the result as Markdown."
	}

	// Langfuse Generation for the LLM call.
	var genID, traceID, parentID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		genID = langfuse.SpanID()
		traceID = tc.TraceID
		parentID = tc.ParentObservationID
		langfuse.CreateGeneration(traceID, genID,
			langfuse.WithGenName("tool.file_read.pdf.qwen-long"),
			langfuse.WithGenParent(parentID),
			langfuse.WithGenModel("qwen-long"),
			langfuse.WithGenInput(map[string]any{"file_url": fileURL, "prompt": prompt}),
		)
	}

	resp, err := aiservice.Chat(ctx, profile.SopText, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{Role: aiservice.MessageRoleSystem, Content: aiservice.MessageContent{Text: sysPrompt}},
			{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "File URL: " + fileURL}},
		},
		ModelOverride: "qwen-long",
	})
	if err != nil {
		if traceID != "" {
			langfuse.EndGeneration(traceID, genID, langfuse.WithGenError(err.Error()))
		}
		return "", 0, false, fmt.Errorf("aiservice.Chat (qwen-long): %w", err)
	}

	if traceID != "" {
		langfuse.EndGeneration(traceID, genID,
			langfuse.WithGenOutput(map[string]any{"content_len": len(resp.Content)}),
			langfuse.WithGenUsage(resp.Usage.PromptTokens, resp.Usage.CompletionTokens),
		)
	}

	content := resp.Content
	truncated := len(content) > fileReadMaxBytes
	if truncated {
		content = content[:fileReadMaxBytes]
	}
	// TODO(T7): extract page count from qwen-long response metadata when available.
	return content, 0, truncated, nil
}

// imageParserImpl uses aiservice.OCR to extract text from images.
type imageParserImpl struct{}

func (p *imageParserImpl) Parse(ctx context.Context, fileURL, _ string) (string, int, bool, error) {
	var spanID, traceID string
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID = langfuse.SpanID()
		traceID = tc.TraceID
		langfuse.CreateSpan(traceID, spanID, "tool.file_read.image.ocr",
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
		langfuse.CreateSpan(traceID, spanID, "tool.file_read.text.direct",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{"file_url": fileURL}),
		)
		defer func() { langfuse.EndSpan(traceID, spanID) }()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", 0, false, fmt.Errorf("build GET request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("http GET: %w", err)
	}
	defer resp.Body.Close()

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
