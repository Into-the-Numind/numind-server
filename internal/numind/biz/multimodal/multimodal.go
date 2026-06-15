// Package multimodal is the mode-agnostic, capability-aware attachment routing
// layer. Given a user's message plus uploaded attachments and the active model's
// capabilities, it produces the LLM-facing user-message parts:
//
//   - If the active model supports the attachment's modality inline, the
//     attachment is sent as a multimodal MessagePart (path A — inline image_url).
//   - Otherwise the attachment's pre-generated text_fallback is used (path B —
//     text fallback). If the fallback is not yet ready, the function waits up to
//     fallbackMaxWait and falls back to a "pending" placeholder on timeout.
//
// This package was extracted from biz/agent so that both the agent runner and the
// chatbot ChatStream can share the same routing logic without importing the heavy
// agent package. The agent's own copy (biz/agent/multimodal.go) remains in place
// for now; migrating it to this package is tracked as tech debt.
//
// TODO(dedup): biz/agent/multimodal.go duplicates the helpers below
// (buildAgentInputForModel / waitForFallback / presignAttachmentURL /
// mkInlineBlock / pendingFallbackTextFor / textFallbackOf). The agent should be
// migrated to call this package; until then the two copies must be kept in sync.
package multimodal

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	agentatt "numind-server/internal/numind/biz/agent/attachment"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/capability"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

const (
	// fallbackPollInterval is the base poll interval when waiting for a fallback
	// to become ready.
	fallbackPollInterval = 100 * time.Millisecond

	// fallbackMaxWait is the maximum total time BuildUserParts will wait for a
	// pending fallback before injecting the "pending" placeholder.
	fallbackMaxWait = 1500 * time.Millisecond

	// presignExpiry is the signed-URL validity for inline COS attachments.
	presignExpiry = 15 * time.Minute
)

// ErrFallbackTimeout is returned by waitForFallback when the attachment's
// fallback is still not ready after the timeout. Callers inject a placeholder.
var ErrFallbackTimeout = errors.New("multimodal: fallback not ready within timeout")

// BuildUserParts resolves the active model's capabilities and constructs the
// user-message parts for the given attachments. hasInlineImage is true when at
// least one part is a native image_url block (meaning the caller must route the
// turn through a path that preserves MessageContent.Parts — e.g. bill-only —
// because the context-fragment renderer would drop image parts).
//
// userMessage is always emitted as the first text part (when non-empty). The
// returned parts are appended in attachment order.
func BuildUserParts(
	ctx context.Context,
	userMessage string,
	atts []*model.AgentAttachment,
	modelKey string,
	attStore store.IAgentAttachmentStore,
) (parts []aiservice.MessagePart, hasInlineImage bool, err error) {
	caps, capsErr := capability.GetCapabilities(modelKey)
	if capsErr != nil {
		// Unknown model or DB error: GetCapabilities returns a conservative
		// (all-false) non-nil Capabilities pointer, so every attachment routes to
		// the text-fallback path. Log and continue.
		log.Warnw("multimodal.BuildUserParts: capability lookup failed, using conservative defaults",
			"model_key", modelKey, "error", capsErr)
	}
	return buildPartsWithCaps(ctx, userMessage, atts, caps, attStore)
}

// buildPartsWithCaps is the pure routing core: given already-resolved
// Capabilities it decides inline vs fallback per attachment. Extracted from
// BuildUserParts so the routing can be unit-tested without a capability DB.
func buildPartsWithCaps(
	ctx context.Context,
	userMessage string,
	atts []*model.AgentAttachment,
	caps *capability.Capabilities,
	attStore store.IAgentAttachmentStore,
) ([]aiservice.MessagePart, bool, error) {
	var parts []aiservice.MessagePart
	if userMessage != "" {
		parts = append(parts, aiservice.MessagePart{
			Type: aiservice.MessagePartTypeText,
			Text: userMessage,
		})
	}

	hasInlineImage := false
	for _, att := range atts {
		if att == nil {
			continue
		}

		inline := false
		switch att.Modality {
		case agentatt.ModalityImage:
			inline = caps != nil && caps.AcceptsImageInline
		case agentatt.ModalityPDF, agentatt.ModalityAudio, agentatt.ModalityDocument:
			// Only image inline is wired here (mkInlineBlock emits image_url only).
			// PDF/audio/document ALWAYS route to the text-fallback path (their
			// pre-generated OCR/extracted text), never inline — otherwise a model
			// with AcceptsPDFInline=true would get a "not supported" placeholder
			// instead of the real fallback text. Native PDF/audio inline is future
			// work. (review P1; differs intentionally from agent copy, which has a
			// file_read tool to consume the URL.)
		default:
			log.Warnw("multimodal.buildPartsWithCaps: unknown modality, falling back",
				"att_id", att.ID, "modality", att.Modality)
		}

		if inline {
			url, presignErr := presignAttachmentURL(ctx, att)
			if presignErr != nil {
				log.Warnw("multimodal.buildPartsWithCaps: presign failed, degrading to fallback",
					"att_id", att.ID, "error", presignErr)
				inline = false
			} else {
				block := mkInlineBlock(att.Modality, url)
				parts = append(parts, block)
				if block.Type == aiservice.MessagePartTypeImageURL {
					hasInlineImage = true
				}
			}
		}

		if !inline {
			text, waitErr := waitForFallback(ctx, att, attStore, fallbackMaxWait)
			if waitErr != nil {
				log.Warnw("multimodal.buildPartsWithCaps: fallback wait failed, injecting placeholder",
					"att_id", att.ID, "error", waitErr)
				text = pendingFallbackTextFor(att)
			}
			parts = append(parts, aiservice.MessagePart{
				Type: aiservice.MessagePartTypeText,
				Text: text,
			})
		}
	}

	return parts, hasInlineImage, nil
}

// LoadAttachmentsByIDs resolves attachment IDs to full entities, filtering by
// owner. A row that fails to load (not found / wrong owner / DB error) is
// silently skipped — a single bad id must not abort the turn. Returns nil when
// ids is empty.
//
// TODO(dedup): mirrors biz/agent/student_run_lifecycle.go:loadAttachmentsByIDs.
func LoadAttachmentsByIDs(
	ctx context.Context,
	attStore store.IAgentAttachmentStore,
	ids []uint64,
	userID uint,
) []*model.AgentAttachment {
	if attStore == nil || len(ids) == 0 {
		return nil
	}
	var results []*model.AgentAttachment
	for _, id := range ids {
		att, err := attStore.GetByIDAndUser(ctx, id, userID)
		if err != nil {
			log.Warnw("multimodal.LoadAttachmentsByIDs: skipping attachment",
				"att_id", id, "user_id", userID, "error", err)
			continue
		}
		results = append(results, att)
	}
	return results
}

// FlattenTextParts joins all text parts into a single newline-separated string.
// Non-text parts are skipped. Used by the non-inline (text-fallback) path so the
// caller can feed the combined text back into the normal context-fragment route.
func FlattenTextParts(parts []aiservice.MessagePart) string {
	var sb strings.Builder
	first := true
	for _, p := range parts {
		if p.Type != aiservice.MessagePartTypeText {
			continue
		}
		if !first {
			sb.WriteString("\n")
		}
		sb.WriteString(p.Text)
		first = false
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// waitForFallback — poll DB until FallbackReady (TODO(dedup) mirrors agent copy)
// ---------------------------------------------------------------------------

// waitForFallback polls the DB until att.FallbackReady is true or timeout
// elapses, returning the text_fallback string on success.
//
// Fast path: if att.FallbackReady is already set in the in-memory struct, the DB
// is not consulted. Terminal-error fallbacks also set FallbackReady=true with a
// pre-composed error message in TextFallback, so the loop exits normally and
// textFallbackOf returns that message — no separate error branch needed.
func waitForFallback(
	ctx context.Context,
	att *model.AgentAttachment,
	attStore store.IAgentAttachmentStore,
	timeout time.Duration,
) (string, error) {
	if att.FallbackReady {
		return textFallbackOf(att), nil
	}
	if attStore == nil {
		return "", ErrFallbackTimeout
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(fallbackPollInterval)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return "", ErrFallbackTimeout
		case <-ticker.C:
			fresh, err := attStore.GetByID(ctx, att.ID)
			if err != nil {
				log.Warnw("multimodal.waitForFallback: GetByID error", "att_id", att.ID, "error", err)
				continue
			}
			if fresh.FallbackReady {
				return textFallbackOf(fresh), nil
			}
		}
	}
}

// textFallbackOf extracts the text_fallback string, defending against a nil
// field (should not happen once FallbackReady=true).
//
// TODO(dedup): mirrors biz/agent/multimodal.go.
func textFallbackOf(att *model.AgentAttachment) string {
	if att.TextFallback != nil && *att.TextFallback != "" {
		return *att.TextFallback
	}
	return pendingFallbackTextFor(att)
}

// pendingFallbackTextFor composes a modality-aware "pending" placeholder for an
// attachment whose fallback is not yet ready.
//
// TODO(dedup): mirrors biz/agent/multimodal.go.
func pendingFallbackTextFor(att *model.AgentAttachment) string {
	var prefix string
	switch att.Modality {
	case agentatt.ModalityImage:
		prefix = "图片"
	case agentatt.ModalityPDF:
		prefix = "PDF"
	case agentatt.ModalityAudio:
		prefix = "音频"
	case agentatt.ModalityDocument:
		prefix = "文档"
	default:
		prefix = "附件"
	}
	return fmt.Sprintf("[%s：%s，描述生成中，请稍后重试或切换到多模态模型]", prefix, att.Filename)
}

// ---------------------------------------------------------------------------
// presign + inline block (TODO(dedup) mirrors agent copy)
// ---------------------------------------------------------------------------

// cosURLPathRE matches COS bucket URLs (bucket.cos.region.myqcloud.com hosts).
var cosURLPathRE = regexp.MustCompile(`^https?://[^/]+\.cos\.[^/]+\.myqcloud\.com/(.+)$`)

// extractCOSObjectKey returns the COS object key for a COS bucket URL and whether
// the URL was recognized as COS.
//
// TODO(dedup): mirrors biz/agent/tool_file_read.go:extractCOSObjectKey.
func extractCOSObjectKey(fileURL string) (string, bool) {
	m := cosURLPathRE.FindStringSubmatch(fileURL)
	if len(m) < 2 {
		return "", false
	}
	return m[1], true
}

// presignAttachmentURL signs the COS object key extracted from att.URL, bound to
// GET with presignExpiry validity. Non-COS URLs (test fixtures, public CDN) and
// the COS-disabled case return att.URL unchanged.
func presignAttachmentURL(ctx context.Context, att *model.AgentAttachment) (string, error) {
	if !util.IsCOSEnabled() {
		return att.URL, nil
	}
	objectKey, isCOS := extractCOSObjectKey(att.URL)
	if !isCOS {
		return att.URL, nil
	}
	signed, err := util.GenerateSignedURL(ctx, objectKey, int64(presignExpiry.Seconds()))
	if err != nil {
		return "", fmt.Errorf("presignAttachmentURL att=%d: %w", att.ID, err)
	}
	return signed, nil
}

// mkInlineBlock constructs an image_url MessagePart. buildPartsWithCaps only
// routes image modality here (PDF/audio/document always take the text-fallback
// path), so the default branch is defensive and should be unreachable in
// practice.
//
// TODO(dedup): mirrors biz/agent/multimodal.go (agent's default branch keeps a
// file_read hint; chatbot has no such tool so the text is shortened — divergence
// is intentional).
func mkInlineBlock(modality, url string) aiservice.MessagePart {
	switch modality {
	case agentatt.ModalityImage:
		return aiservice.MessagePart{
			Type:     aiservice.MessagePartTypeImageURL,
			ImageURL: &aiservice.ImageURL{URL: url},
		}
	default:
		log.Warnw("multimodal.mkInlineBlock: unsupported inline modality, downgrading to text",
			"modality", modality)
		return aiservice.MessagePart{
			Type: aiservice.MessagePartTypeText,
			Text: fmt.Sprintf("[%s 文件已上传，当前模型不支持直接读取]", modality),
		}
	}
}
