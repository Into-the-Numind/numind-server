package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/budget"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// StudentRunService handles learner-facing agent run lifecycle operations.
// Spec: #14 follow-up BETA — 6 run lifecycle endpoints.
type StudentRunService struct {
	runner          AgentRunner
	runStore        store.IAgentRunStore
	skillStore      store.IAgentDefinitionStore
	pricingCalc     pricing.ICalculator
	narrationProv   *narration.Provider
	narrationBuf    *NarrationBuffer
	attachmentStore store.IAgentAttachmentStore // managed file_read references + canonical parse cache
	streamLock      *stream.SubscriptionLock    // T07: SSE single-subscriber guard
	userStore       userByIDGetter              // b2b2c-student-agent-access: wired via WithUserStore; nil → parent-only access
}

// NewStudentRunService constructs a StudentRunService.
//
// narrationProv + narrationBuf must be wired together: the service forwards
// every event emitted by the Provider (per agent_run.id) into the Buffer,
// which is what PollNarration reads. Either side nil disables the forwarder
// — narration_provider init can fail gracefully (yaml missing) and the
// service still functions, just without learner-visible tool-call narration.
func NewStudentRunService(
	runner AgentRunner,
	runStore store.IAgentRunStore,
	skillStore store.IAgentDefinitionStore,
	pricingCalc pricing.ICalculator,
	narrationProv *narration.Provider,
	narrationBuf *NarrationBuffer,
) *StudentRunService {
	return &StudentRunService{
		runner:        runner,
		runStore:      runStore,
		skillStore:    skillStore,
		pricingCalc:   pricingCalc,
		narrationProv: narrationProv,
		narrationBuf:  narrationBuf,
		streamLock:    stream.NewSubscriptionLock(),
	}
}

// WithAttachmentStore wires the IAgentAttachmentStore so managed attachment
// IDs can be ownership-checked and represented as file_read references.
// Call this at biz.go wiring time alongside WithNarrationProvider.
func (s *StudentRunService) WithAttachmentStore(attStore store.IAgentAttachmentStore) *StudentRunService {
	s.attachmentStore = attStore
	return s
}

// WithUserStore wires the user store so resolveDefinition can resolve a caller's
// parent_user_id for B2B2C access (a child running a parent-configured agent).
// store.UserStore satisfies the narrow userByIDGetter. When nil, access falls
// back to the parent-only fast-path (pre-change behavior). Call at biz.go wiring
// time alongside WithAttachmentStore.
func (s *StudentRunService) WithUserStore(userStore store.UserStore) *StudentRunService {
	s.userStore = userStore
	return s
}

// forwardNarration drains the narration provider's per-runID channel into the
// poll-friendly NarrationBuffer. Without this bridge, Provider.Emit puts
// events into an in-memory channel that nobody reads from, and PollNarration
// (which queries the Buffer) returns [] — exactly the symptom that the
// learner-facing UI surfaced as "no narration visible despite tools running".
//
// Lifecycle: spawned per Create call, exits when runner.Run finishes and
// runner.go's defer runs provider.CloseRun(runID), which closes the channel.
// Safe to call when narrationProv or narrationBuf is nil (graceful degrade).
func (s *StudentRunService) forwardNarration(runID uint64) {
	s.forwardNarrationUntil(context.Background(), runID)
}

func (s *StudentRunService) forwardNarrationUntil(ctx context.Context, runID uint64) {
	if s.narrationProv == nil || s.narrationBuf == nil {
		return
	}
	ch, cleanup := s.narrationProv.Subscribe(runID)
	defer cleanup()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			evCopy := ev // pin the loop var; AppendEvent stores by pointer
			s.narrationBuf.AppendEvent(runID, &evCopy)
		}
	}
}

// ---------------------------------------------------------------------------
// Estimate
// ---------------------------------------------------------------------------

// EstimateRunRequest is the payload for POST /v1/agent-runs/estimate.
// Field names align with web-v3 src/types/agent.ts EstimateRequest contract.
type EstimateRunRequest struct {
	AgentDefinitionID uint64 `json:"agent_skill_id" binding:"required"`
	Message           string `json:"input_text" binding:"required"`
}

// EstimateResponse is the response for the estimate endpoint.
// Field names + range/flag shape align with web-v3 EstimateResponse contract:
// {min, max, is_large_task}. Min/Max are derived from the central single-value
// estimate (±20% band); is_large_task = true when central estimate > 100 credits.
type EstimateResponse struct {
	Min         int  `json:"min"`
	Max         int  `json:"max"`
	IsLargeTask bool `json:"is_large_task"`
}

// Estimate returns a pre-flight cost estimate for an agent run.
// Uses budget.EstimateAgentTurn with the agent definition's configured model.
// Falls back to simple inline estimate when pricingCalc is nil.
func (s *StudentRunService) Estimate(ctx context.Context, userID uint, req EstimateRunRequest) (*EstimateResponse, error) {
	// Look up the skill to get credit cap context (and validate it exists).
	ad, err := s.resolveDefinition(ctx, userID, req.AgentDefinitionID)
	if err != nil {
		return nil, err
	}

	// Combine message length + a representative system prompt size.
	// New agents store the prompt in ad.SystemPrompt (V2 runtime path); legacy
	// questionnaire/advanced agents use GeneratedSkillBody/CustomSkillBody. Mirror
	// assembleSystemPrompt's V2-vs-legacy split so the reserve estimate isn't 0 for
	// new agents (T1: questionnaire removed, SystemPrompt is the canonical prompt).
	systemPromptLen := len(ad.GeneratedSkillBody)
	if ad.AdvancedMode {
		systemPromptLen = len(ad.CustomSkillBody)
	}
	if ad.SystemPrompt != "" {
		systemPromptLen = len(ad.SystemPrompt)
	}
	promptCharCount := systemPromptLen + len(req.Message) + 500 // +500 heuristic overhead

	var estCredits int64
	var estPromptTokens int

	if s.pricingCalc != nil {
		// Use the canonical budget estimator for the default model (glm-4-7).
		result, estErr := budget.EstimateAgentTurn(ctx, s.pricingCalc,
			"volc", "glm-4-7-251222",
			promptCharCount, budget.DefaultCompletionEstimate)
		if estErr == nil {
			estCredits = result.EstimatedCredits
			estPromptTokens = result.EstimatedPromptTokens + result.EstimatedCompletionTokens
		}
	}

	// Inline fallback: 1 token ≈ 2 chars, 1 credit per 100 tokens (rough).
	if estCredits <= 0 {
		tokens := promptCharCount/2 + budget.DefaultCompletionEstimate
		if tokens < 1 {
			tokens = 1
		}
		estCredits = int64(tokens) / 100
		if estCredits < 1 {
			estCredits = 1
		}
		estPromptTokens = tokens
	}

	// Per-session credit cap removed (2026-06-17): estimate is no longer clamped.

	// Derive {min, max, is_large_task} band from central estimate (web-v3 contract).
	// ±20% band accounts for completion-token variance in ReAct loops.
	_ = estPromptTokens // retained for future telemetry; not exposed in response
	min := int(float64(estCredits) * 0.8)
	max := int(float64(estCredits) * 1.2)
	if min < 1 {
		min = 1
	}
	if max < min {
		max = min
	}
	return &EstimateResponse{
		Min:         min,
		Max:         max,
		IsLargeTask: estCredits > 100,
	}, nil
}

// ---------------------------------------------------------------------------
// Create (start async run)
// ---------------------------------------------------------------------------

// CreateRunRequest is the payload for POST /v1/agent-runs.
// Field names align with web-v3 src/types/agent.ts CreateRunRequest contract.
type CreateRunRequest struct {
	AgentDefinitionID uint64   `json:"agent_skill_id" binding:"required"`
	SessionID         string   `json:"session_id,omitempty"` // empty → new session
	Message           string   `json:"input_text"`           // empty allowed when an attachment is present (attachment-only send); validated by hasNoSendable
	AttachmentURLs    []string `json:"attachment_urls,omitempty"`
	// AttachmentIDs are the preferred identities for current-user uploaded
	// attachments. AttachmentURLs remains a rolling compatibility field for
	// rows whose upload result has no persisted ID; both fields can be merged.
	AttachmentIDs []uint64 `json:"attachment_ids,omitempty"`
	// ModelKey is the user-selected model identifier (from the model picker in
	// the UI). When non-empty, buildAgentInputForModel uses it to determine
	// capability routing. When empty, conservative defaults apply (full fallback).
	ModelKey string `json:"model_key,omitempty"`
	// IsTest marks a parent-account Builder「试聊」(test-chat) run. When true,
	// agent-mode-billing routes the run's credit charges to the admin_test pool
	// (credit_admin_test_grant) instead of the three-pool. Propagated to
	// RunRequest.IsTest → injectAgentBillingCtx → middleware pool selector.
	IsTest bool `json:"is_test,omitempty"`
}

// hasNoSendable reports whether the request carries nothing for the agent to act
// on — neither input text nor any attachment. An attachment alone IS sendable
// content: the user uploaded a file (e.g. a docx) for the agent to process, so an
// empty input_text WITH attachments must NOT be rejected (customer-reported,
// 2026-06-18). buildAgentInput already composes a valid prompt from attachments
// alone (it prepends the "用户上传了以下附件，请调用 file_read" instruction), so the
// downstream run is well-formed even when Message is empty.
//
// A whitespace-only Message counts as empty (TrimSpace) — it is rejected unless an
// attachment is present. (The prior guard used a bare `== ""`; trimming closes the
// "send a single space" loophole.)
func (r CreateRunRequest) hasNoSendable() bool {
	return strings.TrimSpace(r.Message) == "" && len(r.AttachmentURLs) == 0 && len(r.AttachmentIDs) == 0
}

// CreateRunResponse is returned from POST /v1/agent-runs.
// Field names align with web-v3 src/types/agent.ts CreateRunResponse contract.
// run_id is the real DB row id (Create pre-allocates the row synchronously so
// the frontend can immediately poll GET /agent-runs/:id).
type CreateRunResponse struct {
	RunID               uint64 `json:"run_id"`
	SessionID           string `json:"session_id"`
	EstimatedCreditsMin int    `json:"estimated_credits_min"`
	EstimatedCreditsMax int    `json:"estimated_credits_max"`
}

// Create starts an agent run asynchronously and returns the run_id immediately.
// AgentRunner.Run is synchronous; it is wrapped in a goroutine so the HTTP
// handler can return without waiting for the ReAct loop to complete.
//
// The goroutine uses context.Background() (detached) so that HTTP request
// cancellation does NOT kill the in-flight run.
func (s *StudentRunService) Create(ctx context.Context, userID uint, req CreateRunRequest) (*CreateRunResponse, error) {
	if req.hasNoSendable() {
		return nil, errno.ErrBind.SetMessage("message or attachment is required")
	}

	// Validate agent definition exists and belongs to the learner's parent.
	ad, err := s.resolveDefinition(ctx, userID, req.AgentDefinitionID)
	if err != nil {
		return nil, err
	}

	// Generate session ID if not provided.
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Build the input as managed file_read references. Attachment bodies are not
	// injected here; they remain behind the canonical paged tool interface.
	//
	// Legacy path: AttachmentURLs (no DB entity) → buildAgentInput (plain text hint).
	// New path:    AttachmentIDs → ownership check + file_read reference.
	//
	// Until runner.go (task 1.5) accepts InputMessages natively, the result is
	// serialised to string via MessagesToInputString for RunRequest.Input.
	input, hasFallbackAttachments, displayAtts := s.composeAttachmentInput(ctx, userID, req)
	// Never hand the runner a blank user turn: attachment-only sends are allowed, so a
	// failed attachment load could leave input empty (hasNoSendable only gates the
	// truly-nothing case at entry). Substitute a graceful read-failure instruction.
	if strings.TrimSpace(input) == "" {
		input = emptyAttachmentInputFallback
	}

	// Resolve tool names from ToolFlags JSON.
	toolNames := toolNamesFromFlags(ad.ToolFlags)

	// 继承旧会话的 is_pinned 和 session_name
	var isPinned bool
	var sessionName string
	if sessionID != "" {
		runs, _, err := s.runStore.ListBySession(ctx, sessionID, 0, 1)
		if err == nil && len(runs) > 0 {
			isPinned = runs[0].IsPinned
			sessionName = runs[0].SessionName
		}
	}

	// Pre-create the agent_run row synchronously so the HTTP response can
	// return a real run_id to the frontend (which polls GET /agent-runs/:id).
	startedAt := time.Now()
	preRun := &model.AgentRun{
		UserID:            userID,
		SessionID:         sessionID,
		AgentDefinitionID: req.AgentDefinitionID,
		Status:            "running",
		Messages:          datatypes.JSON([]byte("[]")),
		StartedAt:         startedAt,
		// V1.5 compact-v1-removal — V1 包已删，所有新 run 默认走 V2 (maybeCompactV2)。
		UseCompactV2: true,
		IsPinned:     isPinned,
		SessionName:  sessionName,
		IsTest:       req.IsTest, // agent-mode-billing T10: persist 试聊审计标记
	}
	if err := s.runStore.Create(ctx, preRun); err != nil {
		return nil, fmt.Errorf("StudentRunService.Create pre-create row: %w", err)
	}

	runReq := RunRequest{
		UserID:                userID,
		SessionID:             sessionID,
		Input:                 input,
		DisplayInput:          strPtr(req.Message),
		DisplayAttachments:    displayAtts,
		ToolNames:             toolNames,
		EnforceToolAllowlist:  enforceExplicitToolAllowlist(ad.ToolFlags),
		AgentDefinitionID:     req.AgentDefinitionID,
		EnableMemory:          true,
		ExistingRunID:         preRun.ID,
		AttachmentHasFallback: hasFallbackAttachments,
		IsTest:                req.IsTest, // admin_test pool routing (agent-mode-billing)
		// Multi-turn context: load prior session turns (excluding this just-created
		// row) so the LLM remembers earlier conversation. Fail-open to nil.
		History: s.loadSessionHistory(ctx, sessionID, preRun.ID),
	}

	// Bridge narration events: Provider.Emit pushes events to an in-memory
	// channel keyed by runID; PollNarration reads from the queryable Buffer.
	// Without this forwarder the two halves never connect and the learner UI
	// gets [] forever despite the tools actually running. Spawn BEFORE the
	// runner goroutine so the Subscribe registration is in place when the
	// first PreToolCall fires its StateUse emit; memStreamer.Subscribe is
	// lazy-create-safe, so even a slight delay would not lose events, but
	// ordering is still cheaper to reason about this way.
	go s.forwardNarration(preRun.ID)

	// Async: detached context so HTTP cancel doesn't abort the run.
	go func() {
		detachedCtx := context.Background()
		_, _ = s.runner.Run(detachedCtx, runReq)
		// Result is persisted to DB by runner.Run; frontend polls via narration + GET run.
	}()

	// Return the pre-allocated run_id immediately so the frontend can poll
	// GET /agent-runs/:id without an extra session→run lookup.
	// EstimatedCredits{Min,Max} are populated by callers via Estimate first.
	return &CreateRunResponse{
		RunID:     preRun.ID,
		SessionID: sessionID,
	}, nil
}

// buildAgentInput composes the LLM-facing user-message text from the human's
// message plus any uploaded attachment COS URLs.
//
// Deprecated: use buildAgentInputForModel. Will be removed when task 1.5
// completes multimodal wiring (runner.go accepts InputMessages natively).
//
// When attachments are present, an unconditional Chinese imperative is appended
// telling the agent to invoke the file_read tool with each URL. The previous
// implementation emitted "[attachments: <JSON>]" which the LLM frequently
// ignored — it would reply "you didn't upload anything" despite the URLs
// being in the prompt.
//
// Phrasing notes (locked by tests, do NOT soften):
//   - "请立即调用" (imperative, no opt-out) — earlier "如需查看" gave thinking
//     models like deepseek-v4-pro an out and they would skip the tool call.
//   - The hint MUST come AFTER the user message, not before. Hoisting it to
//     the top changes the ack-then-act priming; tests assert the position.
//   - "然后再回答用户" makes the tool call a prerequisite, not optional.
//
// Returns the bare message unchanged if attachmentURLs is empty.
func buildAgentInput(message string, attachmentURLs []string) string {
	if len(attachmentURLs) == 0 {
		return message
	}
	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\n【系统提示】用户上传了以下附件，请立即调用 file_read 工具读取它们的内容（传入对应的 file_url 参数），然后再回答用户的问题：\n")
	for _, u := range attachmentURLs {
		b.WriteString("- ")
		b.WriteString(u)
		b.WriteString("\n")
	}
	return b.String()
}

// composeAttachmentInput is the single stream/non-stream attachment contract.
// Managed IDs are resolved with ownership checks and represented as file_read
// references; URL-only rows are retained solely for rolling compatibility.
// When both are supplied, URLs already represented by a loaded ID are deduped.
func (s *StudentRunService) composeAttachmentInput(
	ctx context.Context,
	userID uint,
	req CreateRunRequest,
) (input string, hasFileReadAttachments bool, displayAtts []displayAttachment) {
	input = req.Message
	seenURLs := make(map[string]struct{})

	if len(req.AttachmentIDs) > 0 && s.attachmentStore != nil {
		atts := loadAttachmentsByIDs(ctx, s.attachmentStore, req.AttachmentIDs, userID)
		for _, att := range atts {
			if att != nil && att.URL != "" {
				seenURLs[att.URL] = struct{}{}
			}
		}
		displayAtts = append(displayAtts, displayAttachmentsFromEntities(atts)...)
		msgs, buildErr := buildAgentInputForModel(ctx, req.Message, atts, req.ModelKey, s.attachmentStore)
		if buildErr != nil {
			log.Warnw("composeAttachmentInput: managed reference build failed",
				"user_id", userID, "error", buildErr)
		} else {
			input = MessagesToInputString(msgs)
			hasFileReadAttachments = HasFallbackAttachments(msgs)
		}
	} else if len(req.AttachmentIDs) > 0 {
		log.Warnw("composeAttachmentInput: attachmentStore not configured, AttachmentIDs ignored",
			"user_id", userID, "attachment_ids", req.AttachmentIDs)
	}

	legacyURLs := make([]string, 0, len(req.AttachmentURLs))
	for _, rawURL := range req.AttachmentURLs {
		if rawURL == "" {
			continue
		}
		if _, duplicate := seenURLs[rawURL]; duplicate {
			continue
		}
		legacyURLs = append(legacyURLs, rawURL)
	}
	if len(legacyURLs) > 0 {
		legacyHint := buildAgentInput("", legacyURLs)
		if strings.TrimSpace(input) == "" {
			input = legacyHint
		} else {
			input = strings.TrimSpace(input) + "\n" + legacyHint
		}
		displayAtts = append(displayAtts, displayAttachmentsFromURLs(legacyURLs)...)
		hasFileReadAttachments = true
	}

	return input, hasFileReadAttachments, displayAtts
}

// displayAttachmentsFromEntities maps loaded attachment entities to the {url,filename}
// display refs persisted onto the user turn for chip rendering (问题二).
func displayAttachmentsFromEntities(atts []*model.AgentAttachment) []displayAttachment {
	out := make([]displayAttachment, 0, len(atts))
	for _, a := range atts {
		if a != nil && a.URL != "" {
			out = append(out, displayAttachment{URL: a.URL, Filename: a.Filename})
		}
	}
	return out
}

// displayAttachmentsFromURLs builds display refs from a legacy AttachmentURLs list,
// deriving each filename from the URL's last path segment (问题二).
func displayAttachmentsFromURLs(urls []string) []displayAttachment {
	out := make([]displayAttachment, 0, len(urls))
	for _, u := range urls {
		if u != "" {
			out = append(out, displayAttachment{URL: u, Filename: filenameFromURL(u)})
		}
	}
	return out
}

// strPtr returns a pointer to an independent COPY of s (the value parameter), so the
// pointer never aliases a caller's struct field — safe to stash in RunRequest.DisplayInput
// that an async run goroutine reads later.
func strPtr(s string) *string { return &s }

// nanoPrefixRe matches the upload object-key's nanosecond ID prefix
// (agent-attachments/<userID>/<unixnano>-<filename>). It requires ≥13 leading
// digits + a hyphen so short numeric prefixes like "2024-报告.docx" are left alone.
var nanoPrefixRe = regexp.MustCompile(`^\d{13,}-`)

// filenameFromURL extracts a human-readable filename from a URL: last path segment,
// query stripped, percent-decoded, with the upload nanosecond ID prefix removed
// (问题二). Best-effort — returns the raw tail on decode error.
func filenameFromURL(u string) string {
	s := u
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if dec, err := url.PathUnescape(s); err == nil {
		s = dec
	}
	// Strip the nanosecond ID prefix added at upload time so chips show the
	// original filename. Only ≥13-digit prefixes are stripped (see nanoPrefixRe).
	// Guard: if the object key is a bare "<digits>-" with no name after it, stripping
	// would yield an empty chip label — keep the pre-strip tail instead (review P2).
	if stripped := nanoPrefixRe.ReplaceAllString(s, ""); stripped != "" {
		s = stripped
	}
	return s
}

// emptyAttachmentInputFallback is the user-turn substituted when an attachment-only
// send composes to an empty input — e.g. the attachment_ids path with attachmentStore
// unset, or all ids unreadable. Allowing attachment-only sends (no input_text) makes
// this reachable; rather than hand the runner a blank user turn — or orphan the
// already-created run row with a late hard error — the agent is told to surface the
// read failure to the user.
const emptyAttachmentInputFallback = "用户上传了附件，但系统暂时无法读取其内容。请告知用户附件读取失败，并建议重新上传或直接用文字描述需求。"

// loadAttachmentsByIDs fetches AgentAttachment entities for the given IDs,
// enforcing that each row belongs to userID. Rows that fail the ownership
// check or cannot be fetched are skipped (logged as warnings).
//
// Silent skip is intentional: a single attachment fetch failure should not
// abort the entire run. The run continues with whichever attachments loaded
// successfully. Callers that need strict all-or-nothing semantics should not
// use this function.
//
// The HTTP handler binds CreateRunRequest.AttachmentIDs from the frontend; this
// function resolves them to current-user entities for safe references.
func loadAttachmentsByIDs(
	ctx context.Context,
	attStore store.IAgentAttachmentStore,
	ids []uint64,
	userID uint,
) []*model.AgentAttachment {
	var results []*model.AgentAttachment
	for _, id := range ids {
		att, err := attStore.GetByIDAndUser(ctx, id, userID)
		if err != nil {
			log.Warnw("loadAttachmentsByIDs: skipping attachment",
				"att_id", id, "user_id", userID, "error", err)
			continue
		}
		results = append(results, att)
	}
	return results
}

// ---------------------------------------------------------------------------
// T07 — SSE streaming: AcquireStreamLock / ReleaseStreamLock / RunStream
// ---------------------------------------------------------------------------

// AcquireStreamLock creates the agent_run row (reusing the same pre-create
// logic as Create) and then tries to acquire a single-subscriber SSE lock on
// it.  Only one SSE connection per run is allowed; a second caller gets
// acquired=false with the existing runID so it can surface a 409 with the ID.
//
// If acquired=false the agent_run row is NOT rolled back — it has been written
// to DB and the caller must NOT try to clean it up (the row may already have
// been picked up by a background runner in a concurrent CreateStream request).
func (s *StudentRunService) AcquireStreamLock(ctx context.Context, userID uint, req CreateRunRequest) (runID uint64, acquired bool, err error) {
	if req.hasNoSendable() {
		return 0, false, errno.ErrBind.SetMessage("message or attachment is required")
	}

	// Validate agent definition. The returned ad is intentionally unused — its
	// fields (ToolFlags / ParentUserID) are not stored on agent_run; ToolFlags
	// are re-resolved from skillStore inside RunStream, and ParentUserID acts as
	// an access guard inside resolveDefinition itself. Calling for the side
	// effect (validation + error propagation) is sufficient.
	if _, err := s.resolveDefinition(ctx, userID, req.AgentDefinitionID); err != nil {
		return 0, false, err
	}

	// Generate session ID if not provided.
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Inherit is_pinned / session_name from prior session runs.
	var isPinned bool
	var sessionName string
	if sessionID != "" {
		runs, _, listErr := s.runStore.ListBySession(ctx, sessionID, 0, 1)
		if listErr == nil && len(runs) > 0 {
			isPinned = runs[0].IsPinned
			sessionName = runs[0].SessionName
		}
	}

	// Pre-create the agent_run row synchronously (same pattern as Create).
	// P1 fix (T07): use ad fields to populate preRun, matching Create()'s pattern.
	// ad.ParentUserID is the parent account for this learner — not stored on
	// agent_run directly, but validated by resolveDefinition above (access guard).
	// UseCompactV2 is intentionally hardcoded to true for streaming: all new runs
	// use V2 compact (V1 compact package was removed in compact-v1-removal). A
	// future toggle via ad.UseCompactV2 field can replace this when the schema lands.
	startedAt := time.Now()
	preRun := &model.AgentRun{
		UserID:            userID,
		SessionID:         sessionID,
		AgentDefinitionID: req.AgentDefinitionID,
		Status:            "running",
		Messages:          datatypes.JSON([]byte("[]")),
		StartedAt:         startedAt,
		UseCompactV2:      true,       // always V2; see comment above
		IsTest:            req.IsTest, // agent-mode-billing T10: persist 试聊审计标记
		IsPinned:          isPinned,
		SessionName:       sessionName,
		// Note: ToolFlags from ad are NOT stored on agent_run — they are resolved
		// at execution time by RunStream (re-loads the skill from skillStore).
		// ParentUserID from ad is validated above (access guard) and not a model field.
	}
	if err := s.runStore.Create(ctx, preRun); err != nil {
		return 0, false, fmt.Errorf("StudentRunService.AcquireStreamLock pre-create row: %w", err)
	}

	// Attempt to acquire the SSE lock for the new run.
	if !s.streamLock.Acquire(preRun.ID) {
		// Another subscriber already holds this run's lock (extremely unlikely for
		// a brand-new run, but the interface contract must be upheld).
		return preRun.ID, false, nil
	}
	return preRun.ID, true, nil
}

// ReleaseStreamLock releases the SSE single-subscriber lock for runID.
// It is idempotent — safe to call via defer even if AcquireStreamLock was
// never called or returned acquired=false.
func (s *StudentRunService) ReleaseStreamLock(runID uint64) {
	s.streamLock.Release(runID)
}

// AcquireResumeStreamLock acquires the SSE single-subscriber lock for an
// ALREADY-EXISTING paused run (issue4 streaming answer resume). Unlike
// AcquireStreamLock it does NOT pre-create an agent_run row — the run is the one
// the user paused via ask_user_question, loaded by RunStream via runStore.Get.
// Returns false when another SSE subscriber already holds this run's lock; the
// controller then surfaces a 409 and the frontend falls back to the poll path.
// Release via ReleaseStreamLock (idempotent).
func (s *StudentRunService) AcquireResumeStreamLock(runID uint64) bool {
	return s.streamLock.Acquire(runID)
}

// RunStream executes the agent in streaming mode, emitting stream.Event values
// onto ch. The caller must have already called AcquireStreamLock (which
// pre-creates the agent_run row and acquires the SSE lock).
//
// RunStream does NOT close ch; the controller goroutine that spawns RunStream
// closes ch after RunStream returns so that the SSE pump can drain all
// remaining events.
//
// The req.SessionID / req.AgentDefinitionID fields are used to build the
// runner's RunRequest; the session ID is re-derived from the existing row to
// ensure consistency.
func (s *StudentRunService) RunStream(ctx context.Context, userID uint, req CreateRunRequest, runID uint64, ch chan<- stream.Event) (*RunResult, error) {
	// Load the pre-created run to get the canonical sessionID from DB.
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("StudentRunService.RunStream: load run: %w", err)
	}

	// Build capability-aware input (same logic as Create).
	input, hasFallbackAttachments, displayAtts := s.composeAttachmentInput(ctx, userID, req)
	// See Create: never hand the runner a blank user turn on an empty-composed
	// attachment-only send.
	if strings.TrimSpace(input) == "" {
		input = emptyAttachmentInputFallback
	}

	toolNames := toolNamesFromFlags(nil) // tool flags resolved from the loaded run's definition below
	enforceToolAllowlist := false
	if s.skillStore != nil {
		ad, adErr := s.skillStore.GetByIDIncludeInactive(ctx, run.AgentDefinitionID)
		if adErr == nil {
			toolNames = toolNamesFromFlags(ad.ToolFlags)
			enforceToolAllowlist = enforceExplicitToolAllowlist(ad.ToolFlags)
		}
	}

	runReq := RunRequest{
		UserID:                userID,
		SessionID:             run.SessionID,
		Input:                 input,
		DisplayInput:          strPtr(req.Message),
		DisplayAttachments:    displayAtts,
		ToolNames:             toolNames,
		EnforceToolAllowlist:  enforceToolAllowlist,
		AgentDefinitionID:     run.AgentDefinitionID,
		EnableMemory:          true,
		ExistingRunID:         runID,
		AttachmentHasFallback: hasFallbackAttachments,
		IsTest:                req.IsTest, // admin_test pool routing (agent-mode-billing)
		// Multi-turn context: load prior session turns (excluding the current run)
		// so the streaming chat remembers earlier conversation. Fail-open to nil.
		History: s.loadSessionHistory(ctx, run.SessionID, runID),
	}

	// Bridge narration events (same as Create).
	go s.forwardNarration(runID)

	return s.runner.RunStream(ctx, runReq, runID, ch)
}

// turnsToHistoryMessages converts persisted [{role,content}] transcript turns
// into user/assistant schema.Messages (tool_group / system roles dropped — v1
// history is text-only). Shared by loadSessionHistory (multi-run) and the
// resume path (a single waiting run's pre-yield transcript, HW-33).
func turnsToHistoryMessages(turns []map[string]any) []*schema.Message {
	out := make([]*schema.Message, 0, len(turns))
	for _, turn := range turns {
		content := strings.TrimSpace(historyTurnText(turn["content"]))
		if content == "" {
			continue
		}
		role, _ := turn["role"].(string)
		var sr schema.RoleType
		switch role {
		case "user":
			sr = schema.User
		case "assistant":
			sr = schema.Assistant
		default:
			continue
		}
		out = append(out, &schema.Message{Role: sr, Content: content})
	}
	return out
}

// loadSessionHistory loads prior completed turns of the same session and converts
// them into chronological []*schema.Message (user/assistant text pairs) so the
// runner can seed multi-turn context. Without this, every turn was a fresh
// agent_run with messages=[] and the LLM had no memory of earlier turns
// (User-reported, dev 2026-06-08).
//
// Design choices (v1):
//   - excludeRunID drops the just-created row for the current turn (its messages=[]).
//   - Only user/assistant text turns are kept; tool_call/tool_result detail is
//     dropped to avoid OAI tool-pair protocol errors (see MEMORY oai-function-calling).
//   - The newest maxHistoryRuns runs are scanned; total content is capped at
//     maxHistoryChars, trimming the OLDEST messages first to protect the window.
//   - Fail-open: any error returns nil so a history-load failure never blocks the run.
func (s *StudentRunService) loadSessionHistory(ctx context.Context, sessionID string, excludeRunID uint64) []*schema.Message {
	if sessionID == "" {
		return nil
	}
	const (
		maxHistoryRuns  = 40      // ~ last 40 turns of the session
		maxHistoryChars = 200_000 // generous char budget guard (model windows are large but not infinite)
	)

	runs, _, err := s.runStore.ListBySession(ctx, sessionID, 0, maxHistoryRuns)
	if err != nil {
		log.Warnw("StudentRunService.loadSessionHistory: ListBySession failed, proceeding without history",
			"session_id", sessionID, "error", err)
		return nil
	}

	// ListBySession yields DESC (newest first); iterate in reverse for chronological order.
	var msgs []*schema.Message
	total := 0
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		// Only completed turns of this session contribute history: skip the
		// current run, soft-deleted runs, and any non-terminal (still running)
		// row whose messages may be partial.
		if r.ID == excludeRunID || r.IsDeleted || r.Status != "terminated" || len(r.Messages) == 0 {
			continue
		}
		var turns []map[string]any
		if uerr := json.Unmarshal(r.Messages, &turns); uerr != nil {
			continue // skip a malformed row rather than fail the whole load
		}
		for _, m := range turnsToHistoryMessages(turns) {
			msgs = append(msgs, m)
			total += len(m.Content)
		}
	}

	// Over budget → trim from the front (oldest) keeping the most recent turns.
	for total > maxHistoryChars && len(msgs) > 0 {
		total -= len(msgs[0].Content)
		msgs = msgs[1:]
	}
	// Trimming (or a leading assistant-only run) can leave the slice starting
	// with an assistant turn; drop leading non-user turns so history begins with
	// a user message (clean role alternation for the LLM).
	for len(msgs) > 0 && msgs[0].Role != schema.User {
		msgs = msgs[1:]
	}
	return msgs
}

// historyTurnText extracts plain text from a stored turn's "content" field. It is
// normally a string, but may be an OAI-style multimodal array
// ([{"type":"text","text":"..."},{"type":"image_url",...}]). For arrays it
// concatenates the text parts and ignores non-text parts (v1 history is text-only).
// Without this, an array-valued content would type-assert to "" and the whole
// turn (including its text) would be silently dropped from history.
func historyTurnText(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			part, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := part["text"].(string); ok {
				b.WriteString(t)
			}
		}
		return b.String()
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// PollNarration
// ---------------------------------------------------------------------------

// PollNarration returns narration events for runID where event.Timestamp > since.
// Verifies that the run belongs to userID (404 if not).
func (s *StudentRunService) PollNarration(ctx context.Context, userID uint, runID uint64, since time.Time) ([]*narration.Event, error) {
	if err := s.verifyRunOwnership(ctx, userID, runID); err != nil {
		return nil, err
	}

	if s.narrationBuf == nil {
		return []*narration.Event{}, nil
	}

	return s.narrationBuf.QuerySince(runID, since), nil
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

// Cancel sends a cancellation signal to a running agent run.
// Verifies ownership; returns ErrAgentRunNotFound (404) if the run belongs to a
// different user or doesn't exist.
func (s *StudentRunService) Cancel(ctx context.Context, userID uint, runID uint64) error {
	if err := s.verifyRunOwnership(ctx, userID, runID); err != nil {
		return err
	}

	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("StudentRunService.Cancel: get run: %w", err)
	}
	if run.Status != "running" {
		return errno.ErrAgentRunNotCancellable
	}

	s.runner.Cancel(runID)
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// resolveDefinition looks up the agent_definition by ID and validates that
// userID has access to it under the B2B2C tenant model.
//
// Access rule (agentTenantAccess): the caller may use the agent iff it owns it
// (caller is the agent's parent account) OR it is a child/learner of the owning
// parent. Children may run active agents only (R9); the owning parent retains
// access to their own inactive drafts. Cross-tenant / unknown callers get
// ErrSkillNotFound so existence is never revealed.
//
// Returns ErrSkillNotFound (404) for missing, cross-tenant, or de-listed (for a
// child) definitions.
func (s *StudentRunService) resolveDefinition(ctx context.Context, userID uint, agentDefID uint64) (*model.AgentDefinition, error) {
	if s.skillStore == nil {
		return nil, fmt.Errorf("StudentRunService: skillStore not configured")
	}
	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, agentDefID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSkillNotFound
		}
		return nil, fmt.Errorf("StudentRunService.resolveDefinition: %w", err)
	}
	if err := agentTenantAccess(ctx, s.userStore, userID, ad); err != nil {
		return nil, err
	}
	return ad, nil
}

// verifyRunOwnership returns ErrAgentRunNotFound (404) if runID doesn't exist
// or belongs to a different user.
func (s *StudentRunService) verifyRunOwnership(ctx context.Context, userID uint, runID uint64) error {
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrAgentRunNotFound
		}
		return fmt.Errorf("StudentRunService.verifyRunOwnership: %w", err)
	}
	if run.UserID != userID {
		// Do not reveal existence to other users.
		return errno.ErrAgentRunNotFound
	}
	return nil
}

// safeToolBaseline 是所有 Agent 默认启用的无害工具集，配置者无需显式勾选。
// configurator 通过 tool_flags 仅控制 3 个风险类别（code_sandbox/media/dangerous）
// 是否启用，不影响这个 baseline。
//
// 等未来 UX 增加 "individual tool 单工具开关" 时，可以让 tool_flags 显式传
// {"web_search": false} 等覆盖 baseline；当前 frontend (AgentAdvancedEdit.vue)
// 只有 3 个 category 开关，所以 baseline 永远启用。
var safeToolBaseline = []string{
	"kb_search",         // RAG 检索
	"memory_read",       // 长期记忆读
	"memory_write",      // 长期记忆写
	"get_current_date",  // 当前时间
	"ask_user_question", // 反问学员
	"web_search",        // 网络搜索（Tavily）
	"web_fetch",         // URL → Markdown
	"file_read",         // PDF/图/文本
	"create_csv",        // CSV 生成
	"create_html",       // HTML 生成
	"create_json",       // JSON 生成
	"create_text",       // 文本生成
	"create_png_chart",  // 图表生成
	"load_skill",        // open-tools-skill-as-guidance: merged use_skill+read_skill — loads DB-bound + disk platform skill guidance; agent writes Python then run_python executes
	"run_python",        // 2026-05-29 hotfix: load_skill is useless without an executor. The OutputToolsPriorityAddendum already promises every agent the load_skill→run_python path; baseline must match the promise. run_python is sandbox-isolated (docker), so the risk surface is the sandbox image itself, not the agent permission flag.
	"image_gen",         // 2026-06-17: 文生图是常用功能、不再当开关，永远可用；改用每用户并发上限(imageGenMaxConcurrentPerUser=6)控制。
}

// categoryToTools 把 frontend AgentAdvancedEdit.vue 的 3 个 risk-category 开关
// 展开为受限工具（这些工具默认 OFF，必须通过 category 显式启用）。
//
//	code_sandbox  → bash_exec      (RequiresSandbox=true)
//	dangerous     → bash_exec      (RiskLevel="dangerous" 别名)
//	(media/image_gen 已于 2026-06-17 移除：文生图不再当开关，永远可用 + 每用户并发上限)
//	enable_skills → load_skill + run_python   (open-tools-skill-as-guidance merged
//	  use_skill+read_skill into load_skill; the two-step flow needs BOTH tools —
//	  load_skill loads the SKILL.md guidance, run_python executes the Python the LLM
//	  authors from it. Same flag-name retained for zero-migration backward compat
//	  with existing agent_definition.tool_flags JSON. Note: load_skill is also in
//	  safeToolBaseline, so it is registered regardless of this flag; the entry is
//	  kept so the flag's documented contract stays consistent.)
var categoryToTools = map[string][]string{
	"code_sandbox": {"bash_exec"},
	"dangerous":    {"bash_exec"}, // alias of code_sandbox for now
	// Single-loop progressive disclosure: catalog → load_skill → run_python.
	// Both tools must be reachable or the agent crashes mid-flow with
	// `tool run_python not found in toolsNode indexes` (dev QA 2026-05-29).
	"enable_skills": {"load_skill", "run_python"},
}

// toolNamesFromFlags resolves agent_definition.ToolFlags JSON to []string of
// enabled tool names that AgentRunner can look up in the registry.
//
// Frontend stores `tool_flags` as `{category_name: bool}` over 3 risk gates
// (code_sandbox, media, dangerous — see AgentAdvancedEdit.vue), NOT as raw
// tool names. This function:
//  1. Always includes safeToolBaseline (kb_search, web_search, memory_*, ...).
//  2. Expands enabled categories into their tool sets via categoryToTools.
//  3. Honors any direct tool-name keys not in categoryToTools (future-proofs
//     for when frontend gains per-tool toggles); explicit false disables.
//
// Returns nil only if json unmarshal fails. Empty/missing ToolFlags returns
// just the safe baseline so Agents are never useless ReAct short-circuits.
func toolNamesFromFlags(toolFlagsJSON []byte) []string {
	// Start with safe baseline always-on.
	enabled := make(map[string]bool, len(safeToolBaseline)+3)
	for _, name := range safeToolBaseline {
		enabled[name] = true
	}

	if len(toolFlagsJSON) == 0 {
		return mapKeysWhereTrue(enabled)
	}

	var flags map[string]bool
	if err := json.Unmarshal(toolFlagsJSON, &flags); err != nil {
		// Malformed JSON: fall back to safe baseline. Logging happens at the
		// caller (runner.go) once the result reaches the registry resolver.
		return mapKeysWhereTrue(enabled)
	}

	for key, on := range flags {
		if tools, isCategory := categoryToTools[key]; isCategory {
			// Category toggle: expand to tool set.
			for _, t := range tools {
				enabled[t] = on
			}
			continue
		}
		// Direct tool name (future: per-tool toggles in UI); explicit false
		// disables a baseline tool.
		enabled[key] = on
	}
	// Rolling compatibility for Feishu-enabled definitions created before the
	// deterministic explicit-connect tool existed. Missing means inherit the
	// Feishu capability set; an explicit false remains authoritative.
	if _, declared := flags["lark_connect"]; !declared && flags["lark_execute"] {
		enabled["lark_connect"] = true
	}

	return mapKeysWhereTrue(enabled)
}

func mapKeysWhereTrue(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for name, on := range m {
		if on {
			out = append(out, name)
		}
	}
	return out
}
