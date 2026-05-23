// Package agent — multimodal routing helpers for buildAgentInputForModel (task 1.3).
//
// This file contains the capability-aware routing layer that decides whether each
// attachment is sent to the LLM as an inline multimodal block (path A) or as
// text fallback (path B), based on the active model's Capabilities.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/biz/agent/attachment"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/capability"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// fallbackPollInterval is the base poll interval when waiting for a
	// fallback to become ready. Actual wait may include jitter added by
	// attachment.FallbackService.WaitReady.
	fallbackPollInterval = 100 * time.Millisecond

	// fallbackMaxWait is the maximum total time buildAgentInputForModel will
	// wait for a pending fallback before injecting the "pending" placeholder.
	fallbackMaxWait = 1500 * time.Millisecond

	// presignExpiry is the signed-URL validity duration for inline COS
	// attachments. A single agent turn completes well within 15 minutes.
	presignExpiry = 15 * time.Minute
)

// ErrFallbackTimeout is returned by waitForFallback when the attachment's
// fallback is still not ready after fallbackMaxWait. The caller should inject a
// user-visible placeholder rather than blocking the run.
var ErrFallbackTimeout = errors.New("multimodal: fallback not ready within timeout")

// ---------------------------------------------------------------------------
// buildAgentInputForModel — capability-aware routing
// ---------------------------------------------------------------------------

// buildAgentInputForModel constructs the LLM-facing user-message(s) from the
// human's message plus any uploaded attachments. For each attachment:
//
//   - If the active model supports the attachment's modality inline (capability
//     matrix from task 1.1), the attachment is sent as a multimodal MessagePart
//     (path A — inline).
//   - Otherwise the text_fallback field is used (path B — text fallback). If the
//     fallback is not yet ready, the function waits up to fallbackMaxWait (1500ms,
//     polling every 100ms) and falls back to a "pending" placeholder on timeout.
//
// The returned slice contains exactly one ChatMessage with role=user. System
// prompts are NOT constructed here — that is the caller's responsibility.
//
// Caller in student_run_lifecycle.go is responsible for threading the returned
// messages into the run path. Until runner.go (task 1.5) is updated to accept
// []aiservice.ChatMessage natively, lifecycle.go flattens this back to a string
// via MessagesToInputString for RunRequest.Input.
func buildAgentInputForModel(
	ctx context.Context,
	userMessage string,
	attachments []*model.AgentAttachment,
	modelKey string,
	attStore store.IAgentAttachmentStore,
) ([]aiservice.ChatMessage, error) {
	// No attachments → single plain-text user message (fast path).
	if len(attachments) == 0 {
		return []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{Text: userMessage},
			},
		}, nil
	}

	caps, capsErr := capability.GetCapabilities(modelKey)
	if capsErr != nil {
		// Unknown model or DB error: conservative defaults (all inline = false).
		// GetCapabilities already returns the conservative default Capabilities
		// pointer on error, so caps is never nil here.
		log.Warnw("buildAgentInputForModel: capability lookup failed, using conservative defaults",
			"model_key", modelKey, "error", capsErr)
	}

	var parts []aiservice.MessagePart
	// Always start with the user's text.
	if userMessage != "" {
		parts = append(parts, aiservice.MessagePart{
			Type: aiservice.MessagePartTypeText,
			Text: userMessage,
		})
	}

	for _, att := range attachments {
		if att == nil {
			continue
		}

		inline := false
		switch att.Modality {
		case attachment.ModalityImage:
			inline = caps.AcceptsImageInline
		case attachment.ModalityPDF:
			inline = caps.AcceptsPDFInline
		case attachment.ModalityAudio:
			inline = caps.AcceptsAudioInline
		default:
			// Unknown modality → conservatively route to fallback.
			log.Warnw("buildAgentInputForModel: unknown modality, falling back",
				"att_id", att.ID, "modality", att.Modality)
		}

		if inline {
			// Path A: send as native multimodal inline block.
			url, err := presignAttachmentURL(ctx, att)
			if err != nil {
				// Presign failure → degrade to path B (fallback).
				log.Warnw("buildAgentInputForModel: presign failed, degrading to fallback",
					"att_id", att.ID, "error", err)
				inline = false
			} else {
				parts = append(parts, mkInlineBlock(att.Modality, url))
			}
		}

		if !inline {
			// Path B: use text fallback.
			text, err := waitForFallback(ctx, att, attStore, fallbackMaxWait)
			if err != nil {
				// Timeout or ctx cancelled → inject pending placeholder.
				log.Warnw("buildAgentInputForModel: fallback wait failed, injecting placeholder",
					"att_id", att.ID, "error", err)
				text = pendingFallbackTextFor(att)
			}
			parts = append(parts, aiservice.MessagePart{
				Type: aiservice.MessagePartTypeText,
				Text: text,
			})
		}
	}

	msg := aiservice.ChatMessage{
		Role: aiservice.MessageRoleUser,
		Content: aiservice.MessageContent{
			Parts: parts,
		},
	}
	return []aiservice.ChatMessage{msg}, nil
}

// ---------------------------------------------------------------------------
// waitForFallback
// ---------------------------------------------------------------------------

// waitForFallback polls the DB until att.FallbackReady is true or timeout
// elapses. Returns the text_fallback string on success.
//
// Fast path: if att.FallbackReady is already true in the in-memory struct,
// the DB is not consulted.
//
// FallbackError convention: this function does NOT explicitly check
// FallbackError. When the fallback worker sets a terminal error, it also sets
// FallbackReady=true and writes a pre-composed error message into TextFallback
// (e.g. "[图片：file.jpg，处理失败：<reason>]"). So the polling loop exits
// normally (FallbackReady=true) and textFallbackOf returns the error message
// as the text. No separate FallbackError branch is needed here.
//
// On timeout: returns ("", ErrFallbackTimeout).
// On ctx cancellation: returns ("", ctx.Err()).
func waitForFallback(
	ctx context.Context,
	att *model.AgentAttachment,
	attStore store.IAgentAttachmentStore,
	timeout time.Duration,
) (string, error) {
	// Fast path: already ready.
	if att.FallbackReady {
		return textFallbackOf(att), nil
	}

	if attStore == nil {
		// No store → cannot poll; return timeout immediately.
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
				// DB error — treat as transient; continue polling.
				log.Warnw("waitForFallback: GetByID error", "att_id", att.ID, "error", err)
				continue
			}
			if fresh.FallbackReady {
				return textFallbackOf(fresh), nil
			}
			// Not ready yet; keep polling.
		}
	}
}

// textFallbackOf extracts the text_fallback string from att.TextFallback.
// Falls back to a composed pending placeholder if TextFallback is nil (should
// not happen once FallbackReady=true, but defended against).
func textFallbackOf(att *model.AgentAttachment) string {
	if att.TextFallback != nil && *att.TextFallback != "" {
		return *att.TextFallback
	}
	// Defensive: should not reach here with FallbackReady=true.
	return pendingFallbackTextFor(att)
}

// pendingFallbackTextFor composes a user-visible "pending" placeholder for an
// attachment whose fallback is not yet ready. The prefix is modality-specific
// so that image/PDF/audio contexts are not confused. This is used by
// buildAgentInputForModel when waitForFallback times out or the ctx is cancelled.
//
// Note: attachment/templates.go ComposePendingFallback always uses "图片" prefix
// regardless of modality — this function corrects that by switching on att.Modality.
// We intentionally do NOT modify templates.go (task 1.2 owned file).
func pendingFallbackTextFor(att *model.AgentAttachment) string {
	var prefix string
	switch att.Modality {
	case attachment.ModalityImage:
		prefix = "图片"
	case attachment.ModalityPDF:
		prefix = "PDF"
	case attachment.ModalityAudio:
		prefix = "音频"
	default:
		prefix = "附件"
	}
	return fmt.Sprintf("[%s：%s，描述生成中，请稍后重试或切换到多模态模型]", prefix, att.Filename)
}

// ---------------------------------------------------------------------------
// presignAttachmentURL
// ---------------------------------------------------------------------------

// presignAttachmentURL signs the COS object key extracted from att.URL.
// Signing is bound to http.MethodGet with presignExpiry validity.
//
// If att.URL is not a COS URL (e.g. test fixture), the original URL is
// returned unchanged (non-COS URLs are public by construction).
//
// Returns the original URL unchanged when COS is not enabled.
func presignAttachmentURL(ctx context.Context, att *model.AgentAttachment) (string, error) {
	if !util.IsCOSEnabled() {
		// COS not configured (local dev or test) — return URL as-is.
		return att.URL, nil
	}

	objectKey, isCOS := extractCOSObjectKey(att.URL)
	if !isCOS {
		// Not a COS URL — return unchanged (CDN / test fixture).
		return att.URL, nil
	}

	signed, err := util.GenerateSignedURL(ctx, objectKey, int64(presignExpiry.Seconds()))
	if err != nil {
		return "", fmt.Errorf("presignAttachmentURL att=%d: %w", att.ID, err)
	}
	return signed, nil
}

// ---------------------------------------------------------------------------
// mkInlineBlock
// ---------------------------------------------------------------------------

// mkInlineBlock constructs an aiservice.MessagePart for an inline attachment.
// Currently only "image" modality is supported as image_url; all other modalities
// are logged and return a text placeholder (audio not yet supported in V1.5).
func mkInlineBlock(modality, url string) aiservice.MessagePart {
	switch modality {
	case attachment.ModalityImage:
		return aiservice.MessagePart{
			Type:     aiservice.MessagePartTypeImageURL,
			ImageURL: &aiservice.ImageURL{URL: url},
		}
	default:
		// PDF inline and audio inline are not yet wired in V1.5.
		// Return a text description so the LLM at least knows a file is present.
		log.Warnw("mkInlineBlock: unsupported inline modality, downgrading to text",
			"modality", modality)
		return aiservice.MessagePart{
			Type: aiservice.MessagePartTypeText,
			Text: fmt.Sprintf("[%s 文件已上传，当前模型不支持直接读取，请通过 file_read 工具访问]", modality),
		}
	}
}

// ---------------------------------------------------------------------------
// HasFallbackAttachments
// ---------------------------------------------------------------------------

// HasFallbackAttachments reports whether any message part in msgs is a text
// fallback produced by the fallback path (path B). Callers (e.g.
// buildSystemPrompt in runner.go / task 1.5) use this to decide whether to
// inject the 【附件说明】 segment into the system prompt.
//
// Detection heuristic: any text part whose content starts with "[图片：",
// "[PDF：", or "[音频：" is assumed to be a fallback block.
func HasFallbackAttachments(msgs []aiservice.ChatMessage) bool {
	for _, msg := range msgs {
		for _, part := range msg.Content.Parts {
			if part.Type == aiservice.MessagePartTypeText {
				if looksLikeFallbackText(part.Text) {
					return true
				}
			}
		}
	}
	return false
}

// looksLikeFallbackText returns true when text matches a composed fallback
// block prefix (image / PDF / audio / generic attachment).
//
// Known limitation: this is a prefix heuristic. Any user message that starts
// with one of these patterns (e.g., "[图片：some note]") would be detected as
// a fallback block, causing HasFallbackAttachments to return true spuriously.
// In practice this is extremely unlikely in Chinese UI chat, but callers should
// be aware that this is a best-effort heuristic, not a structural check.
func looksLikeFallbackText(text string) bool {
	return strings.HasPrefix(text, "[图片：") ||
		strings.HasPrefix(text, "[PDF：") ||
		strings.HasPrefix(text, "[音频：") ||
		strings.HasPrefix(text, "[附件：")
}

// ---------------------------------------------------------------------------
// BuildAttachmentReminderSegment
// ---------------------------------------------------------------------------

// BuildAttachmentReminderSegment returns the system-prompt segment to append
// when one or more attachments were routed through the fallback (text) path.
// Callers (runner.go task 1.5 — System reminders segment 5) should append this
// to the "System reminders" block.
//
// Returns empty string when no fallback attachments are present so that callers
// can unconditionally append without affecting the system prompt length.
func BuildAttachmentReminderSegment(msgs []aiservice.ChatMessage) string {
	if !HasFallbackAttachments(msgs) {
		return ""
	}
	return "【附件说明】用户上传的图片/PDF 已转为文字描述。请基于描述内容回答用户问题。"
}

// ---------------------------------------------------------------------------
// MessagesToInputString
// ---------------------------------------------------------------------------

// MessagesToInputString collapses a []aiservice.ChatMessage into a single
// plain-text string for backward-compatible injection into RunRequest.Input.
//
// This is a transitional helper for student_run_lifecycle.go until runner.go
// (task 1.5) accepts InputMessages []aiservice.ChatMessage natively.
//
// TODO(task-1.5): remove once runner.go accepts InputMessages natively.
//
// Serialisation strategy:
//   - Single user message, text-only (no attachments): return text as-is.
//   - Multiple parts: join text parts with newlines; image_url parts are
//     represented as "[图片：<url>]" so that any ReAct tool logic that parses
//     the input still has a URL to work with.
func MessagesToInputString(msgs []aiservice.ChatMessage) string {
	if len(msgs) == 0 {
		return ""
	}
	// Use only the user message (role == user).
	var sb strings.Builder
	for _, msg := range msgs {
		if msg.Role != aiservice.MessageRoleUser {
			continue
		}
		if msg.Content.Text != "" && len(msg.Content.Parts) == 0 {
			// Simple text-only message.
			return msg.Content.Text
		}
		for i, part := range msg.Content.Parts {
			if i > 0 {
				sb.WriteString("\n")
			}
			switch part.Type {
			case aiservice.MessagePartTypeText:
				sb.WriteString(part.Text)
			case aiservice.MessagePartTypeImageURL:
				url := ""
				if part.ImageURL != nil {
					url = part.ImageURL.URL
				}
				sb.WriteString(fmt.Sprintf("[图片：%s]", url))
			default:
				sb.WriteString(fmt.Sprintf("[%s attachment]", part.Type))
			}
		}
	}
	return sb.String()
}
