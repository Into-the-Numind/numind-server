# Multimodal Fallback — Agent Mode V1.5

This document describes the four-layer defense system that ensures multimodal
attachments are handled gracefully regardless of which LLM model is active.

---

## Layer 1 — Capability Matrix (Task 1.1)

The `ai_service_capability` table records per-model capability flags:
`accepts_image_inline`, `accepts_pdf_inline`, `accepts_audio_inline`.

`capability.GetCapabilities(modelKey)` looks up these flags. Conservative
defaults (all false) apply when the model key is unknown or the DB is
unavailable.

---

## Layer 2 — Attachment Preprocessing (Task 1.2)

When an agent run starts, each uploaded attachment is preprocessed:

- `text_fallback` is generated asynchronously (OCR / PDF extract / ASR).
- `fallback_ready` is set to `true` once the fallback text is available.

The preprocessing service ensures every attachment has at least a text
representation before the LLM call is made.

---

## Layer 3 — Capability-Aware Routing (Task 1.3)

`buildAgentInputForModel` routes each attachment to either:

- **Path A (inline)**: the attachment is sent as a native `image_url` /
  audio / PDF multimodal block if the active model supports it.
- **Path B (fallback)**: the `text_fallback` string is injected as a text
  part instead (with up to 1500 ms polling for `fallback_ready`).

When any path B fallback is injected, `HasFallbackAttachments` returns true
and the system prompt includes the `【附件说明】` reminder segment.

---

## Layer 4 — Runtime Strip-and-Retry (Task 1.5)

Layer 4 is the last-resort defence triggered when the LLM returns a runtime
error indicating it does not accept image input, despite the capability matrix
saying it should (or due to a routing gap).

### Trigger Condition

`IsMultimodalNotSupportedError(err)` returns true for errors matching any of
8 regex patterns across all known providers:

| # | Pattern | Provider |
|---|---------|----------|
| 0 | `Invalid value: 'image_url'` | OpenAI-compatible (DMXAPI / DashScope compat) |
| 1 | `model does not support image` | Ali DashScope native API |
| 2 | `unsupported.*modality.*image` | Volc Ark / generic |
| 3 | `multimodal.*not.*support` | DMXAPI aggregated |
| 4 | `does not support.*vision` | generic |
| 5 | `image.*input.*not.*support` | generic |
| 6 | `image_url.*not.*allowed` | OpenAI-compatible 422 |
| 7 | `vision.*not.*enabled` | generic |

A future `ErrMultimodalNotSupported` sentinel (`errors.Is` chain — Priority 3)
allows typed provider errors to be detected without text matching.

### Retry Behaviour

1. Strip all `image_url` MessageParts from the request, replacing each with a
   neutral placeholder text: `[图片内容不可用：当前模型不支持图片输入，此图片已被移除。]`
2. Retry the LLM call exactly once with the stripped messages.
3. If `n == 0` (no images to strip), skip retry and return the original error.
4. If the retry also fails, return the original error (not the retry error) so
   the state machine classifies it as `model_error`.

Layer 4 triggers only when Layers 1–3 have a gap. Every trigger is a signal
that the capability matrix needs a correction (spec §R2).

### Observability

Every strip-retry attempt where `n > 0` emits:

- **Structured log** (`log.Warnw`): key `"agent.runtime.strip_image_retry"`,
  fields `model_key`, `provider_id`, `stripped_count`, `orig_prompt_kb`,
  `retry_succeeded`.
- **Langfuse span**: name `"multimodal_strip_retry"`, input includes
  `model_key` / `provider_id` / `stripped_count` / `orig_prompt_kb`, output
  includes `retry_succeeded`.

When `n == 0` (misclassified error, nothing stripped), only a `log.Warnw` with
key `"strip_retry_exhausted: multimodal error but no images to strip"` is
emitted — no Langfuse span, to reduce noise.

### Key Files

| File | Role |
|------|------|
| `internal/numind/biz/agent/runner_strip_retry.go` | `callAIServiceWithStripRetry`, `stripImagesFromMessages`, `emitStripRetryEvent` |
| `internal/numind/biz/aiservice/errors/multimodal.go` | `IsMultimodalNotSupportedError`, 8-pattern table, `ErrMultimodalNotSupported` sentinel, `MultimodalStripRetryMetric` |
| `internal/numind/biz/agent/adapter.go` | `Generate` wraps Layer 4 via `callAIServiceWithStripRetry`; `Stream` does not (ReAct uses Generate only) |
