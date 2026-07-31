package attachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoder
	_ "image/jpeg" // register JPEG decoder
	_ "image/png"  // register PNG decoder
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/semaphore"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/parser"
	"numind-server/internal/pkg/util"
)

// cosURLPathRE matches a Tencent COS object URL and captures the object key
// (everything after the host). Used to presign URLs before handing them to
// downstream AI providers — COS buckets are private by default, so a raw
// public URL returns HTTP 403.
//
//	https://numind-dev-1334169463.cos.ap-chengdu.myqcloud.com/agent-attachments/1/file.png
//	  → captured group: "agent-attachments/1/file.png"
var cosURLPathRE = regexp.MustCompile(`^https?://[^/]+\.cos\.[^/]+\.myqcloud\.com/(.+)$`)

// presignedExpiry is how long downstream providers have to fetch the asset.
// 15 minutes covers retry chains (1s + 4s + 16s + VLM call times) with
// plenty of headroom.
const presignedExpiry int64 = 15 * 60

// presignIfCOS returns a signed GET URL when the input is a Tencent COS bucket
// URL, and passes through otherwise. Signing failures degrade to the original
// URL with a warning — downstream call may then 403, which is the existing
// retry/error pipeline.
func presignIfCOS(ctx context.Context, rawURL string) string {
	m := cosURLPathRE.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return rawURL // not a COS URL, pass through
	}
	objectKey := m[1]
	signed, err := util.GenerateSignedURL(ctx, objectKey, presignedExpiry)
	if err != nil {
		log.Warnw("attachment fallback: presign failed, using raw URL",
			"object_key", objectKey, "error", err)
		return rawURL
	}
	return signed
}

// ─────────────────────────────────────────────────────────────────────────────
// Modality constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	ModalityImage    = "image"
	ModalityPDF      = "pdf"
	ModalityAudio    = "audio"
	ModalityDocument = "document" // office docs: docx/doc/pptx/xlsx/rtf (local text extraction)
	ModalityText     = "text"     // plain text / markdown (local extraction)
	ModalityUnknown  = "unknown"

	// DetectModality helpers
	mimeImagePrefix = "image/"
	mimePDF         = "application/pdf"
	mimeAudioPrefix = "audio/"
	mimeMP3         = "audio/mpeg"
	mimeTextPlain   = "text/plain"
	mimeTextMD      = "text/markdown"
)

// documentMIMEs is the set of office-document MIME types routed to the document
// modality (local text extraction via parser.DocumentParser). PDF is handled by
// its own modality; these are the non-PDF office formats.
// Only formats parser.DocumentParser supports are listed; legacy .xls/.ppt are
// excluded (no parser support).
var documentMIMEs = map[string]struct{}{
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   {}, // .docx
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {}, // .xlsx
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {}, // .pptx
	"application/msword": {}, // .doc
	"application/rtf":    {}, // .rtf
	"text/rtf":           {}, // .rtf (alt)
}

// DetectModality maps a MIME type to one of the modality constants.
func DetectModality(mimeType string) string {
	// Strip parameters (e.g. "image/jpeg; charset=..." → "image/jpeg").
	if idx := strings.Index(mimeType, ";"); idx != -1 {
		mimeType = strings.TrimSpace(mimeType[:idx])
	}
	mimeType = strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(mimeType, mimeImagePrefix):
		return ModalityImage
	case mimeType == mimePDF:
		return ModalityPDF
	case strings.HasPrefix(mimeType, mimeAudioPrefix), mimeType == mimeMP3:
		return ModalityAudio
	case mimeType == mimeTextPlain, mimeType == mimeTextMD:
		return ModalityText
	default:
		if _, ok := documentMIMEs[mimeType]; ok {
			return ModalityDocument
		}
		return ModalityUnknown
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Service interface
// ─────────────────────────────────────────────────────────────────────────────

// ErrFallbackTimeout is returned by WaitReady when the attachment's fallback
// is still not ready after the caller-supplied deadline.
var ErrFallbackTimeout = errors.New("fallback timeout: attachment not ready yet")

// FallbackService manages the async fallback generation lifecycle.
type FallbackService interface {
	// Enqueue schedules fallback generation for the given attachment ID.
	// It is designed for fire-and-forget — the HTTP handler calls it and
	// returns immediately. If the internal channel is full, generation is
	// executed synchronously in a goroutine (degraded path).
	Enqueue(ctx context.Context, attID uint64) error

	// WaitReady blocks until the attachment's fallback is ready or timeout
	// expires. Returns the latest attachment state in either case so that
	// callers can inspect FallbackError.
	WaitReady(ctx context.Context, attID uint64, timeout time.Duration) (*model.AgentAttachment, error)

	// GetStatusForUser returns the attachment row only if it belongs to userID.
	// This is the biz-layer API for the GET /v1/agent-attachments/:id/status
	// handler so that the controller does not bypass the biz layer by calling
	// the store directly (P1 #1 fix — biz layer enforcement).
	GetStatusForUser(ctx context.Context, attID uint64, userID uint) (*model.AgentAttachment, error)

	// GenerateNow executes fallback generation synchronously (bypasses queue).
	// Intended for tests and admin-triggered retries.
	GenerateNow(ctx context.Context, att *model.AgentAttachment) error

	// RecoverPending re-enqueues attachment rows whose fallback generation
	// was interrupted (started_at older than 5 minutes and still not ready).
	// Must be called once on server startup.
	RecoverPending(ctx context.Context) error

	// Start launches the worker goroutines. Must be called once before Enqueue.
	// The pool drains gracefully when ctx is cancelled.
	Start(ctx context.Context)
}

// ─────────────────────────────────────────────────────────────────────────────
// Configuration & pool
// ─────────────────────────────────────────────────────────────────────────────

const (
	defaultWorkers    = 10
	defaultQueueSize  = 1000
	maxRetries        = 3
	perUserMaxConcur  = 3
	staleFallbackMins = 5

	// Per-modality single-call timeouts.
	vlmTimeout = 60 * time.Second
	asrTimeout = 90 * time.Second
	pdfTimeout = 90 * time.Second

	// Exponential backoff delays between retries: 1s, 4s, 16s.
	retryDelay1 = 1 * time.Second
	retryDelay2 = 4 * time.Second
	retryDelay3 = 16 * time.Second

	// Document size limit for local extraction; matches the agent attachment
	// upload cap (biz/attachment/upload.go MaxUploadSize = 20MB).
	maxDocumentBytes int64 = 20 * 1024 * 1024 // 20MB

	// text_fallback remains a MySQL TEXT compatibility column. Canonical full
	// content lives in parsed_content LONGTEXT; keep the wrapper below the
	// 65,535-byte TEXT ceiling so a large successful parse cannot fail solely
	// because of the legacy field.
	maxTextFallbackBytes = 60 * 1024

	// docDownloadTimeout bounds the HTTP GET when fetching a document's bytes.
	docDownloadTimeout = 60 * time.Second
)

var retryDelays = [maxRetries]time.Duration{retryDelay1, retryDelay2, retryDelay3}

// fallbackPool is the concrete FallbackService implementation.
type fallbackPool struct {
	store      store.IAgentAttachmentStore
	jobs       chan uint64 // buffered; capacity = defaultQueueSize
	perUserSem map[uint]*semaphore.Weighted
	semMu      sync.Mutex // protects perUserSem map
	workers    int
	workerCtx  context.Context // retained so degraded goroutines can be cancelled on shutdown
}

// NewFallbackService constructs a FallbackService backed by the given store.
func NewFallbackService(attStore store.IAgentAttachmentStore) FallbackService {
	return &fallbackPool{
		store:      attStore,
		jobs:       make(chan uint64, defaultQueueSize),
		perUserSem: make(map[uint]*semaphore.Weighted),
		workers:    defaultWorkers,
	}
}

// semFor returns the per-user semaphore, creating it on first access.
func (p *fallbackPool) semFor(userID uint) *semaphore.Weighted {
	p.semMu.Lock()
	defer p.semMu.Unlock()
	if s, ok := p.perUserSem[userID]; ok {
		return s
	}
	s := semaphore.NewWeighted(perUserMaxConcur)
	p.perUserSem[userID] = s
	return s
}

// Start launches worker goroutines and runs until ctx is cancelled.
// It also stores ctx so degraded goroutines (queue-full path) can be cancelled
// on server shutdown (P1 #2 fix: no more orphan goroutines with context.Background()).
func (p *fallbackPool) Start(ctx context.Context) {
	p.workerCtx = ctx
	for i := 0; i < p.workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case attID, ok := <-p.jobs:
					if !ok {
						return
					}
					p.processJob(ctx, attID)
				}
			}
		}()
	}
}

// Enqueue adds attID to the internal job channel.
// If the channel is full (queue backpressure), it falls back to launching a
// goroutine that will eventually process the job (best-effort).
func (p *fallbackPool) Enqueue(_ context.Context, attID uint64) error {
	select {
	case p.jobs <- attID:
		return nil
	default:
		// Channel full — degrade to async goroutine.
		// Use workerCtx so the goroutine is cancelled on server shutdown (P1 #2).
		log.Warnw("fallback queue full, degrading to ad-hoc goroutine", "att_id", attID)
		degradeCtx := p.workerCtx
		if degradeCtx == nil {
			degradeCtx = context.Background() // safety: Start not yet called
		}
		go p.processJob(degradeCtx, attID)
		return nil
	}
}

// WaitReady polls the DB until the attachment's fallback_ready is true or
// the timeout elapses. Returns the most recent attachment state.
// Poll interval is 100ms + up to 30ms random jitter to reduce thundering-herd
// when many callers poll the same attachment simultaneously (P2 #3).
// Respects ctx cancellation (P1 #3 fix).
func (p *fallbackPool) WaitReady(ctx context.Context, attID uint64, timeout time.Duration) (*model.AgentAttachment, error) {
	deadline := time.Now().Add(timeout)
	var last *model.AgentAttachment
	for time.Now().Before(deadline) {
		att, err := p.store.GetByID(ctx, attID)
		if err != nil {
			return nil, fmt.Errorf("fallbackPool.WaitReady: %w", err)
		}
		last = att
		if att.FallbackReady {
			return att, nil
		}
		// 100ms base poll + up to 30ms jitter (spec §"7. WaitReady 行为" D4 decision).
		jitter := time.Duration(rand.Intn(30)) * time.Millisecond //nolint:gosec // jitter, not crypto
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(100*time.Millisecond + jitter):
			// continue next poll
		}
	}
	return last, ErrFallbackTimeout
}

// GetStatusForUser returns the attachment row only if it belongs to userID.
// Satisfies FallbackService.GetStatusForUser so controllers call biz, not store
// directly (P1 #1 biz layer enforcement fix).
func (p *fallbackPool) GetStatusForUser(ctx context.Context, attID uint64, userID uint) (*model.AgentAttachment, error) {
	att, err := p.store.GetByIDAndUser(ctx, attID, userID)
	if err != nil {
		return nil, fmt.Errorf("fallbackPool.GetStatusForUser att=%d user=%d: %w", attID, userID, err)
	}
	return att, nil
}

// GenerateNow executes generation synchronously without queuing.
func (p *fallbackPool) GenerateNow(ctx context.Context, att *model.AgentAttachment) error {
	return p.generate(ctx, att)
}

// RecoverPending re-enqueues rows that were started but never finished
// (i.e., process crashed during generation). Stale threshold = 5 minutes ago.
func (p *fallbackPool) RecoverPending(ctx context.Context) error {
	stale := time.Now().Add(-staleFallbackMins * time.Minute)
	rows, err := p.store.ListPendingFallback(ctx, stale, defaultQueueSize)
	if err != nil {
		return fmt.Errorf("fallbackPool.RecoverPending: %w", err)
	}
	for _, r := range rows {
		if err := p.Enqueue(ctx, r.ID); err != nil {
			log.Warnw("RecoverPending: enqueue failed", "att_id", r.ID, "error", err)
		}
	}
	log.Infow("fallback RecoverPending complete", "recovered", len(rows))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal job processor
// ─────────────────────────────────────────────────────────────────────────────

// processJob fetches the attachment row and calls generate with retry logic.
// It injects a Langfuse trace so that VLM/ASR aiservice calls produce
// generation records in Langfuse (P0 fix: no more silent AI calls).
func (p *fallbackPool) processJob(ctx context.Context, attID uint64) {
	att, err := p.store.GetByID(ctx, attID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Warnw("fallback: attachment not found, skipping", "att_id", attID)
			return
		}
		log.Errorw("fallback: GetByID failed", "att_id", attID, "error", err)
		return
	}

	// Already done (idempotency guard).
	if att.FallbackReady {
		return
	}

	// Unknown modality: skip entirely, leave fallback_ready=false.
	if att.Modality == ModalityUnknown || att.Modality == "" {
		log.Infow("fallback: unknown modality, skipping", "att_id", attID, "mime", att.MimeType)
		return
	}

	// ── P0: Inject Langfuse trace ─────────────────────────────────────────────
	// System-triggered worker (not HTTP request) still needs observability.
	// langfuse.CreateTrace is a no-op when Langfuse is disabled (C == nil or !enabled).
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "attachment.fallback",
		langfuse.WithUserID(att.UserID),
		langfuse.WithTraceTags("attachment_fallback", att.Modality),
		langfuse.WithTraceInput(map[string]interface{}{
			"attachment_id": attID,
			"modality":      att.Modality,
			"filename":      att.Filename,
			"size_kb":       att.Size / 1024,
		}),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// Acquire per-user concurrency slot.
	sem := p.semFor(att.UserID)
	if err := sem.Acquire(ctx, 1); err != nil {
		log.Warnw("fallback: semaphore acquire cancelled", "att_id", attID)
		return
	}
	defer sem.Release(1)

	if err := p.generate(ctx, att); err != nil {
		log.Errorw("fallback: generate failed after retries", "att_id", attID, "error", err)
	}
}

// generate runs the generation loop with exponential retry.
// Total attempts = maxRetries+1 (initial + maxRetries retries).
// Delays between attempts: retryDelays[0]=1s, retryDelays[1]=4s, retryDelays[2]=16s (P2 #5 fix).
func (p *fallbackPool) generate(ctx context.Context, att *model.AgentAttachment) error {
	// Mark started — omit no-op retry_count write (P2 #1 fix).
	now := time.Now()
	_ = p.store.UpdateFallback(ctx, att.ID, map[string]interface{}{
		"fallback_started_at": now,
	})

	var lastErr error
	// attempt=0 is the initial try; attempts 1..maxRetries are retries.
	// retryDelays[0/1/2] are applied before attempts 1/2/3 respectively.
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryDelays[attempt-1]
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		lastErr = p.generateOnce(ctx, att)
		if lastErr == nil {
			return nil
		}

		log.Warnw("fallback: attempt failed, will retry", "att_id", att.ID,
			"attempt", attempt+1, "max_attempts", maxRetries+1, "error", lastErr)

		// Update retry count.
		_ = p.store.UpdateFallback(ctx, att.ID, map[string]interface{}{
			"retry_count": uint8(attempt + 1),
		})
	}

	// Terminal failure.
	errMsg := lastErr.Error()
	fallbackText := composeErrorFallback(att.Filename, att.Modality, errMsg)
	completed := time.Now()
	_ = p.store.UpdateFallback(ctx, att.ID, map[string]interface{}{
		"fallback_ready":        true,
		"fallback_error":        errMsg,
		"text_fallback":         fallbackText,
		"fallback_completed_at": completed,
	})
	return fmt.Errorf("generate att %d: %w", att.ID, lastErr)
}

// generateOnce performs a single attempt for the attachment's modality.
func (p *fallbackPool) generateOnce(ctx context.Context, att *model.AgentAttachment) error {
	switch att.Modality {
	case ModalityImage:
		return p.generateImage(ctx, att)
	case ModalityPDF:
		return p.generatePDF(ctx, att)
	case ModalityAudio:
		return p.generateAudio(ctx, att)
	case ModalityDocument, ModalityText:
		return p.generateDocument(ctx, att)
	default:
		return fmt.Errorf("unsupported modality: %s", att.Modality)
	}
}

// generateDocument handles office documents (docx/doc/pptx/xlsx/rtf) by
// downloading the file and extracting plain text locally via the shared
// parser.DocumentParser — the same engine SOP uses (docx is pure-Go;
// xlsx/pptx use MarkItDown; doc uses antiword — all baked into the server
// image). Local extraction is deterministic, zero-cost, and cross-border-free,
// which is why agent mode reuses it rather than the bare-URL→qwen-long path.
func (p *fallbackPool) generateDocument(ctx context.Context, att *model.AgentAttachment) error {
	filesizeKB := att.Size / 1024

	if att.Size > maxDocumentBytes {
		errMsg := fmt.Sprintf("document too large for extraction (max %dMB)", maxDocumentBytes/1024/1024)
		fallbackText := fmt.Sprintf("[文档：%s（%dKB），文件过大无法提取（上限 %dMB）]",
			att.Filename, filesizeKB, maxDocumentBytes/1024/1024)
		completed := time.Now()
		_ = p.store.UpdateFallback(ctx, att.ID, map[string]interface{}{
			"fallback_ready":        true,
			"fallback_error":        errMsg,
			"text_fallback":         fallbackText,
			"fallback_completed_at": completed,
		})
		return nil // not retry-able
	}

	text, err := extractLocalDocumentText(ctx, att, maxDocumentBytes)
	if err != nil {
		return fmt.Errorf("generateDocument extract: %w", err)
	}
	if err := validateExtractedText(att, text); err != nil {
		return p.finishExtractionFailure(ctx, att, err.Error(), composeDocumentFallback(att.Filename, filesizeKB, ""))
	}

	fallbackText := composeDocumentFallback(att.Filename, filesizeKB, text)
	completed := time.Now()
	fields := canonicalParsedContentFields(text, 0, completed)
	fields["text_fallback"] = boundedTextFallback(fallbackText)
	fields["fallback_ready"] = true
	fields["fallback_error"] = nil
	fields["fallback_completed_at"] = completed
	if err := p.store.UpdateFallback(ctx, att.ID, fields); err != nil {
		return fmt.Errorf("generateDocument UpdateFallback: %w", err)
	}
	return nil
}

func extractLocalDocumentText(ctx context.Context, att *model.AgentAttachment, maxBytes int64) (string, error) {
	// Download bytes (COS objects are private → presign a GET URL).
	data, err := downloadBytes(ctx, presignIfCOS(ctx, att.URL), maxBytes)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}

	text, err := parser.NewDocumentParser().Parse(ctx, bytes.NewReader(data), att.Filename)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	return strings.TrimSpace(text), nil
}

func (p *fallbackPool) finishExtractionFailure(ctx context.Context, att *model.AgentAttachment, errMsg string, fallbackText string) error {
	completed := time.Now()
	if err := p.store.UpdateFallback(ctx, att.ID, map[string]interface{}{
		"fallback_ready":           true,
		"fallback_error":           errMsg,
		"text_fallback":            fallbackText,
		"fallback_completed_at":    completed,
		"parsed_content":           nil,
		"parsed_content_sha256":    "",
		"parsed_content_byte_size": int64(0),
		"parsed_page_count":        0,
		"parsed_at":                nil,
	}); err != nil {
		return fmt.Errorf("finish extraction failure: %w", err)
	}
	return nil
}

func validateExtractedText(att *model.AgentAttachment, text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fmt.Errorf("%s text extraction returned empty content", att.Modality)
	}
	if looksLikeProviderRefusal(trimmed) {
		return fmt.Errorf("%s text extraction returned provider refusal text", att.Modality)
	}
	if looksLikeAccessError(trimmed) {
		return fmt.Errorf("%s text extraction returned access error text", att.Modality)
	}
	if att.Modality == ModalityPDF && isSparsePDFExtraction(att.Size, trimmed) {
		return fmt.Errorf("PDF text extraction returned too little text; possible scanned or image-only PDF")
	}
	return nil
}

func looksLikeProviderRefusal(text string) bool {
	sample := strings.ToLower(firstNRunes(text, 1200))
	phrases := []string{
		"我无法直接访问",
		"我无法访问",
		"无法访问或提取",
		"不能下载",
		"无法下载",
		"不能读取该pdf",
		"无法读取该pdf",
		"cannot access",
		"can't access",
		"unable to access",
		"cannot download",
		"can't download",
		"unable to download",
	}
	for _, phrase := range phrases {
		if strings.Contains(sample, phrase) {
			return true
		}
	}
	return false
}

func looksLikeAccessError(text string) bool {
	sample := strings.ToLower(firstNRunes(text, 1200))
	phrases := []string{
		"403 forbidden",
		"access denied",
		"accessdenied",
		"signaturedoesnotmatch",
		"nosuchkey",
		"request has expired",
		"<error>",
	}
	for _, phrase := range phrases {
		if strings.Contains(sample, phrase) {
			return true
		}
	}
	return false
}

func isSparsePDFExtraction(fileSize int64, text string) bool {
	if fileSize < 256*1024 {
		return false
	}
	return meaningfulRuneCount(text) < 40
}

func meaningfulRuneCount(text string) int {
	count := 0
	for _, r := range text {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '\u4e00' && r <= '\u9fff' {
			count++
		}
	}
	return count
}

func firstNRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	for i := range text {
		if limit == 0 {
			return text[:i]
		}
		limit--
	}
	return text
}

// downloadBytes fetches up to maxBytes from url via HTTP GET. Used by document
// fallback extraction (parser.DocumentParser needs the raw bytes, not a URL).
func downloadBytes(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{Timeout: docDownloadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d MiB download limit", maxBytes/1024/1024)
	}
	return data, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Modality-specific generators
// ─────────────────────────────────────────────────────────────────────────────

// generateImage handles the image modality with a single VLM pass through the
// attachment.vision_describe task profile.
func (p *fallbackPool) generateImage(ctx context.Context, att *model.AgentAttachment) error {
	filesizeKB := att.Size / 1024

	// ── Presign URL for downstream providers ────────────────────────────────
	// COS buckets are private; raw URLs return 403 to Ali VLM.
	// Generate a signed GET URL valid for 15 minutes (covers retries + LLM time).
	imageURL := presignIfCOS(ctx, att.URL)

	// ── Width/Height detection ──────────────────────────────────────────────
	// We try to decode image dimensions from the URL if not already set.
	// For COS-hosted images we cannot re-download bytes cheaply here, so we
	// skip dimension extraction if already blank and leave Width/Height nil.
	// (The controller can set them at upload time if it has the raw bytes.)

	// ── VLM description ─────────────────────────────────────────────────────
	var visDesc string
	vlmCtx, vlmCancel := context.WithTimeout(ctx, vlmTimeout)
	defer vlmCancel()

	vlmResp, vlmErr := aiservice.Chat(vlmCtx, profile.AttachmentVisionDescribe, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleSystem,
				Content: aiservice.MessageContent{Text: VLMSystemPrompt},
			},
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{
							Type: aiservice.MessagePartTypeText,
							Text: VLMUserPromptTemplate,
						},
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: imageURL},
						},
					},
				},
			},
		},
		MaxTokens: 512,
	})
	if vlmErr != nil {
		// VLM is a hard failure (we retry the whole attempt).
		return fmt.Errorf("generateImage VLM: %w", vlmErr)
	}
	if vlmResp != nil {
		visDesc = strings.TrimSpace(vlmResp.Content)
	}

	// ── Compose fallback text ────────────────────────────────────────────────
	td := imageTemplateData{
		Filename:          att.Filename,
		Width:             att.Width,
		Height:            att.Height,
		FilesizeKB:        filesizeKB,
		VisionDescription: visDesc,
	}
	fallbackText := composeImageFallback(td)
	canonicalText := composeCanonicalImageText(visDesc)

	// ── Persist ─────────────────────────────────────────────────────────────
	completed := time.Now()
	fields := canonicalParsedContentFields(canonicalText, 0, completed)
	fields["ocr_text"] = nil
	fields["vision_description"] = nilIfEmpty(visDesc)
	fields["text_fallback"] = boundedTextFallback(fallbackText)
	fields["fallback_ready"] = true
	fields["fallback_error"] = nil
	fields["fallback_completed_at"] = completed
	if err := p.store.UpdateFallback(ctx, att.ID, fields); err != nil {
		return fmt.Errorf("generateImage UpdateFallback: %w", err)
	}
	return nil
}

// generatePDF handles the PDF modality via the shared local DocumentParser.
// This keeps PDF behavior aligned with Word/PPT/Excel/text attachments and avoids
// treating upstream model refusals as extracted document text.
func (p *fallbackPool) generatePDF(ctx context.Context, att *model.AgentAttachment) error {
	filesizeKB := att.Size / 1024

	if att.Size > maxDocumentBytes {
		errMsg := fmt.Sprintf("PDF too large for extraction (max %dMB)", maxDocumentBytes/1024/1024)
		fallbackText := composePDFFallback(att.Filename, filesizeKB, "")
		return p.finishExtractionFailure(ctx, att, errMsg, fallbackText)
	}

	pdfCtx, pdfCancel := context.WithTimeout(ctx, pdfTimeout)
	defer pdfCancel()

	extractedText, err := extractLocalDocumentText(pdfCtx, att, maxDocumentBytes)
	if err != nil {
		return fmt.Errorf("generatePDF extract: %w", err)
	}
	if err := validateExtractedText(att, extractedText); err != nil {
		return p.finishExtractionFailure(ctx, att, err.Error(), composePDFFallback(att.Filename, filesizeKB, ""))
	}

	fallbackText := composePDFFallback(att.Filename, filesizeKB, extractedText)
	completed := time.Now()
	fields := canonicalParsedContentFields(extractedText, 0, completed)
	fields["text_fallback"] = boundedTextFallback(fallbackText)
	fields["fallback_ready"] = true
	fields["fallback_error"] = nil
	fields["fallback_completed_at"] = completed
	if err := p.store.UpdateFallback(ctx, att.ID, fields); err != nil {
		return fmt.Errorf("generatePDF UpdateFallback: %w", err)
	}
	return nil
}

// generateAudio handles the audio modality using the ASR service.
func (p *fallbackPool) generateAudio(ctx context.Context, att *model.AgentAttachment) error {
	asrCtx, asrCancel := context.WithTimeout(ctx, asrTimeout)
	defer asrCancel()

	// Detect audio format from MIME type for the ASR hint.
	audioFmt := audioFormatFromMIME(att.MimeType)

	resp, err := aiservice.ASR(asrCtx, profile.MonitorTranscribe, aiservice.ASRRequest{
		AudioURL:    presignIfCOS(ctx, att.URL),
		AudioFormat: audioFmt,
		Language:    "zh",
	})
	if err != nil {
		return fmt.Errorf("generateAudio ASR: %w", err)
	}

	transcript := ""
	durationSec := 0.0
	if resp != nil {
		transcript = strings.TrimSpace(resp.Text)
		durationSec = resp.DurationSeconds
	}

	fallbackText := composeAudioFallback(att.Filename, durationSec, transcript)
	completed := time.Now()
	fields := canonicalParsedContentFields(transcript, 0, completed)
	fields["text_fallback"] = boundedTextFallback(fallbackText)
	fields["fallback_ready"] = true
	fields["fallback_error"] = nil
	fields["fallback_completed_at"] = completed
	if err := p.store.UpdateFallback(ctx, att.ID, fields); err != nil {
		return fmt.Errorf("generateAudio UpdateFallback: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// nilIfEmpty returns nil if s is empty, else &s.
// Used when updating optional text fields in the DB so that empty strings
// result in SQL NULL rather than an empty string.
func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// canonicalParsedContentFields returns the atomic DB update fragment shared
// by every successful modality. Normalization happens once here so persisted
// byte size and the file_read continuation token can never disagree.
func canonicalParsedContentFields(content string, pageCount int, completed time.Time) map[string]interface{} {
	normalized := strings.ToValidUTF8(content, "\uFFFD")
	sum := sha256.Sum256([]byte(normalized))
	return map[string]interface{}{
		"parsed_content":           normalized,
		"parsed_content_sha256":    fmt.Sprintf("sha256:%x", sum),
		"parsed_content_byte_size": int64(len(normalized)),
		"parsed_page_count":        pageCount,
		"parsed_at":                completed,
	}
}

func composeCanonicalImageText(visionDescription string) string {
	sections := make([]string, 0, 1)
	if visionDescription != "" {
		sections = append(sections, "画面描述：\n"+visionDescription)
	}
	return strings.Join(sections, "\n\n")
}

func boundedTextFallback(text string) string {
	if len(text) <= maxTextFallbackBytes {
		return text
	}
	end := maxTextFallbackBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

// audioFormatFromMIME extracts a short format hint from a MIME type string.
func audioFormatFromMIME(mimeType string) string {
	mimeType = strings.ToLower(mimeType)
	switch {
	case strings.Contains(mimeType, "wav"):
		return "wav"
	case strings.Contains(mimeType, "mp3"), strings.Contains(mimeType, "mpeg"):
		return "mp3"
	case strings.Contains(mimeType, "m4a"), strings.Contains(mimeType, "mp4"):
		return "m4a"
	case strings.Contains(mimeType, "ogg"):
		return "ogg"
	case strings.Contains(mimeType, "webm"):
		return "webm"
	default:
		return ""
	}
}

// decodeImageDimensions reads width and height from raw image bytes without
// fully decoding the image. Returns (nil, nil) if decoding fails.
func decodeImageDimensions(data []byte) (*int, *int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, nil
	}
	w, h := cfg.Width, cfg.Height
	return &w, &h
}

// DecodeImageDimensionsFromBytes is the exported wrapper used by upload.go
// at upload time to record image dimensions in the attachment row.
func DecodeImageDimensionsFromBytes(data []byte) (*int, *int) {
	return decodeImageDimensions(data)
}
