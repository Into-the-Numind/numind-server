package agent

// runner_strip_retry.go — Task 1.5: runtime multimodal strip-and-retry wrapper.
//
// Also provides the attachment-reminder system-prompt text (task 1.3 deferral):
// when RunRequest.AttachmentHasFallback is true, runner.Run injects
// attachmentReminderText into segment 5 ("System reminders") of the system
// prompt so that text-only models know the images were converted to text.
//
// This file implements the Layer 4 defensive fallback for multimodal capability
// mismatches. When the upstream LLM returns a "model does not support image"
// error (detected via the errors package regex table), we:
//  1. Strip all image_url MessageParts from the request messages.
//  2. Retry the call exactly once with the stripped messages.
//  3. If the retry also fails, return the original error (model_error terminal).
//
// This path should be invisible in normal operation — the capability matrix
// (Task 1.1) and routing layer (Task 1.3) should prevent image content from
// reaching text-only models. Every trigger here is a signal that the capability
// data needs a correction; see spec §R2.

import (
	"context"

	aierrors "numind-server/internal/numind/biz/aiservice/errors"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
)

// attachmentReminderText is injected into system prompt segment 5 ("System
// reminders") when RunRequest.AttachmentHasFallback is true. It tells the
// text-only model that any images/PDFs in the user message have been converted
// to text descriptions and should be answered from those descriptions.
//
// This constant is package-internal; it is surfaced through runner.go where
// runner.Run appends it to toolsSectionPlaceholder conditionally.
//
// Note: the body text must remain semantically consistent with
// multimodal.go::BuildAttachmentReminderSegment — both describe the same
// situation (attachments converted to text). This constant adds surrounding
// newlines for system-prompt formatting; BuildAttachmentReminderSegment omits
// them (callers handle their own spacing). Any wording change must be mirrored
// in both places.
const attachmentReminderText = "\n【附件说明】用户上传的图片/PDF 已转为文字描述。请基于描述内容回答用户问题。\n"

// stripImagesFromMessages walks the message list and replaces each image-type
// MessagePart with a single placeholder text part. Image parts are NOT dropped —
// they're replaced with a neutral text marker so the LLM keeps positional context
// of where an image used to be. Returns (newMsgs, stripped_count).
//
// Text parts, tool_call_id, tool_calls, role, and ReasoningContent are preserved.
// The original slice and its elements are not mutated.
//
// When n == 0 the caller should not retry — either there were no images to strip
// or the error is not due to image content, so retrying would be pointless.
func stripImagesFromMessages(msgs []aiservice.ChatMessage) ([]aiservice.ChatMessage, int) {
	n := 0
	out := make([]aiservice.ChatMessage, 0, len(msgs))
	for _, msg := range msgs {
		newMsg := aiservice.ChatMessage{
			Role:             msg.Role,
			ToolCallID:       msg.ToolCallID,
			ToolCalls:        msg.ToolCalls,
			ReasoningContent: msg.ReasoningContent,
		}

		// If there are no multipart content parts, copy Text as-is.
		if len(msg.Content.Parts) == 0 {
			newMsg.Content = msg.Content
			out = append(out, newMsg)
			continue
		}

		// Filter out image_url parts, keep the rest.
		kept := make([]aiservice.MessagePart, 0, len(msg.Content.Parts))
		for _, part := range msg.Content.Parts {
			if part.Type == aiservice.MessagePartTypeImageURL {
				// Inject neutral placeholder so the model retains positional context
				// of where an image used to be, without instructing the user to take
				// any action (which the LLM might echo back, breaking Layer 4 transparency).
				kept = append(kept, aiservice.MessagePart{
					Type: aiservice.MessagePartTypeText,
					Text: "[图片内容不可用：当前模型不支持图片输入，此图片已被移除。]",
				})
				n++
			} else {
				kept = append(kept, part)
			}
		}
		newMsg.Content = aiservice.MessageContent{
			Text:  msg.Content.Text,
			Parts: kept,
		}
		out = append(out, newMsg)
	}
	return out, n
}

// callAIServiceWithStripRetry wraps chatFn (aiservice.Chat) with a single
// strip-and-retry on multimodal-not-supported errors. It is the canonical
// call site for agent-mode LLM calls; all chat paths through the adapter
// should use this instead of calling chatFn directly.
//
// Behaviour:
//  1. Call chatFn(ctx, taskID, req).
//  2. If err != nil and IsMultimodalNotSupportedError(err):
//     a. Strip image parts → get n and stripped messages.
//     b. If n == 0: skip retry (no images to strip; error is unrelated).
//     c. Else: log warn + increment metric + retry once.
//     d. Emit Langfuse event "multimodal_strip_retry" with metric.
//  3. Return the final (resp, err) pair to the caller.
//
// The function NEVER recurses and does NOT modify req in place.
func callAIServiceWithStripRetry(
	ctx context.Context,
	taskID string,
	req aiservice.ChatRequest,
	modelKey string,
) (*aiservice.ChatResponse, error) {
	resp, err := chatFn(ctx, taskID, req)
	if err == nil {
		return resp, nil
	}

	// Only attempt strip-retry for multimodal errors.
	if !aierrors.IsMultimodalNotSupportedError(err) {
		return nil, err
	}

	// Check context cancellation before doing extra work.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Strip image parts from the request messages.
	stripped, n := stripImagesFromMessages(req.Messages)
	metric := &aierrors.MultimodalStripRetryMetric{
		ModelKey:       modelKey,
		ProviderID:     0, // TODO: thread from aiservice gateway when provider context becomes available
		StrippedCount:  n,
		OrigPromptKB:   estimatePromptKB(req.Messages),
		RetrySucceeded: false,
	}

	if n == 0 {
		// No images were present — the error is being misclassified or there is
		// nothing to strip. Skipping retry to avoid pointless duplication.
		// No strip-retry event is emitted here (n==0 means nothing was stripped;
		// emitting would add noise with zero signal for capability-matrix operators).
		log.Warnw("strip_retry_exhausted: multimodal error but no images to strip",
			"model_key", modelKey,
			"task_id", taskID,
			"error", err.Error(),
		)
		return nil, err
	}

	// Retry with the stripped messages.
	retryReq := req
	retryReq.Messages = stripped
	log.Warnw("multimodal not supported, stripping and retrying",
		"model_key", modelKey,
		"task_id", taskID,
		"stripped_count", n,
		"orig_prompt_kb", metric.OrigPromptKB,
	)

	resp2, err2 := chatFn(ctx, taskID, retryReq)
	metric.RetrySucceeded = (err2 == nil)

	// Emit observability events regardless of retry outcome.
	emitStripRetryEvent(ctx, metric)

	if err2 != nil {
		log.Warnw("strip_retry_exhausted: retry also failed after image stripping",
			"model_key", modelKey,
			"task_id", taskID,
			"retry_error", err2.Error(),
		)
		// Return the original error so the state machine can classify it.
		return nil, err
	}

	return resp2, nil
}

// emitStripRetryEvent records a Langfuse trace event and a structured log for
// the strip-retry attempt. Safe to call when the trace is not present in ctx.
func emitStripRetryEvent(ctx context.Context, metric *aierrors.MultimodalStripRetryMetric) {
	// Structured log is always emitted (for non-Langfuse environments).
	log.Warnw("agent.runtime.strip_image_retry",
		"model_key", metric.ModelKey,
		"provider_id", metric.ProviderID,
		"stripped_count", metric.StrippedCount,
		"orig_prompt_kb", metric.OrigPromptKB,
		"retry_succeeded", metric.RetrySucceeded,
	)

	// Langfuse event — gracefully no-ops when tracing is disabled.
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID := langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "multimodal_strip_retry",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{
				"model_key":      metric.ModelKey,
				"provider_id":    metric.ProviderID,
				"stripped_count": metric.StrippedCount,
				"orig_prompt_kb": metric.OrigPromptKB,
			}),
			langfuse.WithSpanOutput(map[string]any{
				"retry_succeeded": metric.RetrySucceeded,
			}),
		)
		langfuse.EndSpan(tc.TraceID, spanID)
	}
}

// bytesPerKB is the divisor used to convert byte counts to kilobytes in telemetry.
const bytesPerKB = 1024 // 1 KiB = 1024 bytes

// estimatePromptKB computes a rough byte-size estimate of the messages in
// kilobytes, used only for telemetry.
func estimatePromptKB(msgs []aiservice.ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content.Text)
		for _, p := range m.Content.Parts {
			total += len(p.Text)
			if p.ImageURL != nil {
				total += len(p.ImageURL.URL)
			}
		}
	}
	return total / bytesPerKB
}
