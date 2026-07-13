package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// RunSummary is a lightweight view of model.AgentRun returned by list endpoints.
// Full messages are omitted to keep list payloads small.
// JSON field names align with web-v3 src/types/agent.ts AgentRun / RecentSession contract.
type RunSummary struct {
	ID                uint64 `json:"id"`
	UserID            uint   `json:"user_id"`
	SessionID         string `json:"session_id"`
	AgentDefinitionID uint64 `json:"agent_skill_id,omitempty"`
	Status            string `json:"status"`
	StateReason       string `json:"state_reason,omitempty"`
	// V1.5 compact-dead-schema-cleanup — CompactSummary 字段（legacy V1）已删；
	// 前端向后兼容：API JSON 体不再含 compact_summary 键（之前永远是空串，下线无感）。
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`

	// Fix 2: enriched identity fields (from agent_definition).
	AgentName  string `json:"agent_name,omitempty"`
	AgentEmoji string `json:"agent_emoji,omitempty"`

	// Fix 2: timing / preview.
	// LastActiveAt is ended_at if set, else updated_at, else started_at (RFC3339).
	LastActiveAt string `json:"last_active_at,omitempty"`
	// PreviewText is the first user-role content from messages, truncated to ~60 chars.
	PreviewText string `json:"preview_text,omitempty"`

	// Fix 1: credits used computed on read (no DB columns added).
	// 单次会话积分上限已删除（2026-06-17）；不再有 per-run budget / threshold 展示。
	CreditsUsed int `json:"credits_used"`

	// 会话管理字段
	IsPinned    bool   `json:"is_pinned"`
	SessionName string `json:"session_name"`
}

// frontendStatus maps backend agent_run.status + state_reason to the AgentRunStatus
// enum the web-v3 frontend expects: 'pending' | 'running' | 'completed' | 'timeout'
// | 'failed' | 'cancelled' | 'budget_exhausted'.
//
// Backend stores "running" while a run is active and "terminated" once it finishes;
// state_reason (TerminalReason) carries the granular outcome. The frontend never
// learned the "terminated" status, so we collapse it here at the response boundary.
func frontendStatus(status, stateReason string) string {
	if stateReason == "external_resume_ready" || strings.HasPrefix(stateReason, "ext_resume:") {
		return "running"
	}
	switch status {
	case "running", "pending":
		return status
	case "terminated":
		switch stateReason {
		case "completed":
			return "completed"
		case "error_max_budget":
			return "budget_exhausted"
		case "max_turns":
			return "timeout"
		case "cancelled", "aborted_streaming", "aborted_tools":
			return "cancelled"
		case "waiting_for_user_choice":
			// Run is paused awaiting an ask_user_question answer. Surface as
			// active "running" rather than the default "failed" so the chat
			// header / cancel / input-disable logic stay correct while the
			// question is pending; the frontend distinguishes the paused
			// sub-state via state_reason (isWaitingForUser). Applies to both
			// the streaming and polling resume paths.
			return "running"
		default:
			return "failed"
		}
	}
	return status
}

// runEnrichment holds the computed-on-read extra fields for a single run.
type runEnrichment struct {
	agentName   string
	agentEmoji  string
	creditsUsed int
}

// runToSummary converts a model.AgentRun to a RunSummary without enrichment.
// Call enrichSummary afterwards to populate credits / agent name / preview.
func runToSummary(r model.AgentRun) RunSummary {
	lastActive := r.StartedAt
	if r.EndedAt != nil {
		lastActive = *r.EndedAt
	} else if !r.UpdatedAt.IsZero() {
		lastActive = r.UpdatedAt
	}

	return RunSummary{
		ID:                r.ID,
		UserID:            r.UserID,
		SessionID:         r.SessionID,
		AgentDefinitionID: r.AgentDefinitionID,
		Status:            frontendStatus(r.Status, r.StateReason),
		StateReason:       r.StateReason,
		StartedAt:         r.StartedAt,
		EndedAt:           r.EndedAt,
		CreatedAt:         r.CreatedAt,
		LastActiveAt:      lastActive.UTC().Format(time.RFC3339),
		PreviewText:       extractPreviewText(r.Messages),
		IsPinned:          r.IsPinned,
		SessionName:       r.SessionName,
	}
}

// enrichSummary applies computed enrichment to an already-constructed RunSummary.
func enrichSummary(s *RunSummary, e runEnrichment) {
	s.AgentName = e.agentName
	s.AgentEmoji = e.agentEmoji
	s.CreditsUsed = e.creditsUsed
}

// extractPreviewText pulls the first user-role content from the messages JSON array,
// truncated to 60 Unicode codepoints (avoids mid-rune splits with CJK text).
func extractPreviewText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var turns []map[string]any
	if err := json.Unmarshal(raw, &turns); err != nil {
		return ""
	}
	for _, turn := range turns {
		if role, _ := turn["role"].(string); role == "user" {
			if content, ok := turn["content"].(string); ok && content != "" {
				return truncateRunes(content, 60)
			}
		}
	}
	return ""
}

// truncateRunes truncates s to at most n Unicode codepoints.
func truncateRunes(s string, n int) string {
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	_ = utf8.RuneCountInString(s) // silence staticcheck
	return s
}

// SessionSnapshot is returned by GetSessionSnapshot for resume flows.
// Messages is the transformed frontend-shaped array (see transformMessages).
//
// V1.5 compact-dead-schema-cleanup — CompactSummary 字段（legacy V1）已删；
// 前端 resume flow 不再依赖此字段（之前永远是空串）。
type SessionSnapshot struct {
	Run      RunSummary  `json:"run"`
	Messages interface{} `json:"messages"` // frontend-shaped AgentMessage array
}

// StudentQueryService handles the student-facing read + session-management endpoints.
// (#14 follow-up ALPHA).
type StudentQueryService struct {
	runStore     store.IAgentRunStore
	userStore    store.UserStore
	skillStore   store.IAgentDefinitionStore // for agent_name/emoji
	creditStore  store.CreditStore           // for credits_used computation (SumByReservationIDs)
	runCanceller RunCanceller
}

// RunCanceller is the narrow active-run capability needed by session deletion.
type RunCanceller interface {
	Cancel(runID uint64) bool
}

// NewStudentQueryService constructs a StudentQueryService.
// skillStore and creditStore may be nil; the service degrades gracefully
// (credits_used=0, agent_name=”, etc.) rather than panicking.
func NewStudentQueryService(
	runStore store.IAgentRunStore,
	userStore store.UserStore,
	opts ...StudentQueryOption,
) *StudentQueryService {
	svc := &StudentQueryService{runStore: runStore, userStore: userStore}
	for _, o := range opts {
		o(svc)
	}
	return svc
}

// StudentQueryOption is a functional option for NewStudentQueryService.
type StudentQueryOption func(*StudentQueryService)

// WithQuerySkillStore injects an IAgentDefinitionStore so RunSummary can be
// enriched with agent_name and agent_emoji.
func WithQuerySkillStore(s store.IAgentDefinitionStore) StudentQueryOption {
	return func(svc *StudentQueryService) { svc.skillStore = s }
}

// WithQueryCreditStore injects a CreditStore so credits_used can be computed.
func WithQueryCreditStore(s store.CreditStore) StudentQueryOption {
	return func(svc *StudentQueryService) { svc.creditStore = s }
}

func WithQueryRunCanceller(c RunCanceller) StudentQueryOption {
	return func(svc *StudentQueryService) { svc.runCanceller = c }
}

// ListRecentSessions returns the last N sessions for the learner, ordered by
// started_at DESC. limit ≤ 0 defaults to 5.
func (s *StudentQueryService) ListRecentSessions(ctx context.Context, userID uint, limit int) ([]*RunSummary, error) {
	if limit <= 0 {
		limit = 5
	}
	runs, err := s.runStore.ListByUser(ctx, userID, nil, limit)
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.ListRecentSessions: %w", err)
	}
	return s.toEnrichedSummaries(ctx, runs)
}

// ListAllHistorySessions returns ALL of the learner's sessions, newest first.
//
// adaptive-session-titles US4: the sidebar must show every session the user ever
// created, not just the last 30 days, so the previous `since = now-30d` window is
// removed (sinceTime=nil). ListByUser already dedups to one row per session
// (latest run by started_at) and orders is_pinned DESC, started_at DESC. 500 is a
// generous safety cap that effectively means "all" for any real user.
func (s *StudentQueryService) ListAllHistorySessions(ctx context.Context, userID uint) ([]*RunSummary, error) {
	const historyCap = 500
	runs, err := s.runStore.ListByUser(ctx, userID, nil, historyCap)
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.ListAllHistorySessions: %w", err)
	}
	// No-silent-cap: surface when a power user actually hits the safety bound so we
	// notice if pagination is ever truly needed (rather than silently dropping the
	// oldest sessions).
	if len(runs) == historyCap {
		log.C(ctx).Warnw("ListAllHistorySessions hit 500-session cap; oldest sessions omitted",
			"user_id", userID)
	}
	return s.toEnrichedSummaries(ctx, runs)
}

// GetSessionSnapshot returns all messages across all runs in the session
// (ordered chronologically) for the learner-facing resume flow.
//
// Lookup is by agent_run.session_id (UUID string), NOT agent_run.id.
// Fetches up to 100 runs to reconstruct full multi-turn chat history.
//
// Returns errno.ErrAgentRunNotFound if no runs match the session_id, and
// errno.ErrForbidden if the session belongs to a different user.
func (s *StudentQueryService) GetSessionSnapshot(ctx context.Context, userID uint, sessionID string) (*SessionSnapshot, error) {
	runs, _, err := s.runStore.ListBySession(ctx, sessionID, 0, 100)
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.GetSessionSnapshot list: %w", err)
	}
	if len(runs) == 0 {
		return nil, errno.ErrAgentRunNotFound
	}

	// Use the newest run to represent the session's current status and metadata
	latestRun := &runs[0]
	if latestRun.UserID != userID {
		return nil, errno.ErrForbidden.SetMessage("access to another user's session is not allowed")
	}

	summaries, err := s.toEnrichedSummaries(ctx, []model.AgentRun{*latestRun})
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.GetSessionSnapshot enrich: %w", err)
	}
	runSummary := *summaries[0]

	snap := &SessionSnapshot{
		Run: runSummary,
	}

	// Concatenate all messages chronologically (ListBySession yields DESC, so process in reverse)
	var allMessages []agentMessage
	for i := len(runs) - 1; i >= 0; i-- {
		runMessages := transformMessages(runs[i].Messages, runs[i].ID, runs[i].StartedAt, runs[i].EndedAt, runs[i].Status, runs[i].StateReason)
		allMessages = append(allMessages, runMessages...)
		// A run paused at ask_user_question has no assistant turn for the
		// question; synthesize an interactive card so a reloaded session can
		// render it and resume (yield-session-reload fix).
		if hasPendingExternalAction(runs[i].PendingExternalActionJSON) {
			if action, ok := synthesizeExternalAction(&runs[i]); ok {
				allMessages = append(allMessages, action)
			}
		} else if q, ok := synthesizeQuestionPrompt(&runs[i]); ok {
			allMessages = append(allMessages, q)
		}
	}

	// Re-sign COS links in every rendered message (cos-url-lazy-resign): a
	// reopened session re-signs from the object key, healing truncated and
	// expired URLs (dev run 150). transformMessages is a pure, ctx-less helper,
	// so the signing pass lives here where the request ctx is available.
	// Only Markdown is re-signed: COS object links come from tool results into
	// assistant content, never into reasoning/thinking text, so Reasoning needs
	// no pass.
	for i := range allMessages {
		// 问题二: re-sign user attachment URLs too (they expire/truncate like
		// embedded markdown links) so reloaded chips stay clickable.
		for j := range allMessages[i].Attachments {
			if u := allMessages[i].Attachments[j].URL; u != "" {
				allMessages[i].Attachments[j].URL = resignCOSLinks(ctx, u)
			}
		}
		if allMessages[i].Markdown != "" {
			allMessages[i].Markdown = resignCOSLinks(ctx, allMessages[i].Markdown)
		}
	}

	snap.Messages = allMessages
	return snap, nil
}

// RunDetail is the GET /v1/agent-runs/:id response. RunSummary fields are
// included so the frontend can read the same shape as list endpoints, plus
// `final_output` (extracted assistant text from the last turn) for the chat UI
// to render once the run completes.
type RunDetail struct {
	RunSummary
	FinalOutput string `json:"final_output,omitempty"`
}

// GetRun returns a single run, ownership-checked.
// Returns errno.ErrForbidden if the run belongs to a different user.
func (s *StudentQueryService) GetRun(ctx context.Context, userID uint, runID uint64) (*RunDetail, error) {
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || isNotFoundErr(err) {
			return nil, errno.ErrAgentRunNotFound
		}
		return nil, fmt.Errorf("StudentQueryService.GetRun get: %w", err)
	}
	if run.UserID != userID {
		return nil, errno.ErrForbidden.SetMessage("access to another user's run is not allowed")
	}

	summaries, err := s.toEnrichedSummaries(ctx, []model.AgentRun{*run})
	if err != nil {
		return nil, fmt.Errorf("StudentQueryService.GetRun enrich: %w", err)
	}
	return &RunDetail{
		RunSummary: *summaries[0],
		// Re-sign any COS link the model embedded in its answer: the persisted
		// URL may be truncated (model dropped the signature) or expired
		// (cos-url-lazy-resign, dev run 150).
		FinalOutput: resignCOSLinks(ctx, extractFinalAssistantText(run.Messages)),
	}, nil
}

// extractFinalAssistantText pulls the last assistant message content out of
// agent_run.messages (a JSON array of {role,content} turns). Returns "" if
// nothing useful is present (still-running run, error before WriteTurn, etc.).
func extractFinalAssistantText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var turns []map[string]any
	if err := json.Unmarshal(raw, &turns); err != nil {
		return ""
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if role, _ := turns[i]["role"].(string); role == "assistant" {
			if content, ok := turns[i]["content"].(string); ok {
				return content
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Enrichment helpers (Fix 1 + Fix 2)
// ---------------------------------------------------------------------------

// toEnrichedSummaries converts a slice of AgentRun rows to enriched RunSummary
// using batch queries for agent_definitions and credit_transactions.
func (s *StudentQueryService) toEnrichedSummaries(ctx context.Context, runs []model.AgentRun) ([]*RunSummary, error) {
	out := make([]*RunSummary, len(runs))
	for i, r := range runs {
		rs := runToSummary(r)
		out[i] = &rs
	}
	if len(runs) == 0 {
		return out, nil
	}

	// --- Fix 2: batch-load agent_definitions for name/emoji/credit_cap ---
	defMap := make(map[uint64]*model.AgentDefinition)
	if s.skillStore != nil {
		defIDs := make([]uint64, 0, len(runs))
		seen := make(map[uint64]bool)
		for _, r := range runs {
			if r.AgentDefinitionID != 0 && !seen[r.AgentDefinitionID] {
				defIDs = append(defIDs, r.AgentDefinitionID)
				seen[r.AgentDefinitionID] = true
			}
		}
		for _, id := range defIDs {
			def, err := s.skillStore.GetByIDIncludeInactive(ctx, id)
			if err == nil && def != nil {
				defMap[id] = def
			}
			// silently skip missing / soft-deleted defs
		}
	}

	// --- Fix 1: batch-load credit sums by reservation_id ---
	creditsByReservation := make(map[uint64]int64)
	if s.creditStore != nil {
		rsvIDs := make([]uint64, 0, len(runs))
		for _, r := range runs {
			if r.ReservationID != nil && *r.ReservationID != 0 {
				rsvIDs = append(rsvIDs, *r.ReservationID)
			}
		}
		if len(rsvIDs) > 0 {
			sums, err := s.creditStore.SumByReservationIDs(ctx, rsvIDs)
			if err == nil {
				creditsByReservation = sums
			}
			// graceful degrade: if error, leave creditsByReservation empty → credits_used=0
		}
	}

	// Apply enrichment to each summary.
	for i, r := range runs {
		var creditsUsed int64
		if r.ReservationID != nil {
			creditsUsed = creditsByReservation[*r.ReservationID]
		}

		agentName := ""
		agentEmoji := ""
		if def, ok := defMap[r.AgentDefinitionID]; ok {
			agentName = def.Name
			// AgentDefinition doesn't have an emoji field in the model; use "" for now.
			// TODO: add emoji/icon field to agent_definition when the model gets it.
			_ = agentEmoji
		}

		enrichSummary(out[i], runEnrichment{
			agentName:   agentName,
			agentEmoji:  agentEmoji,
			creditsUsed: int(creditsUsed),
		})
	}

	return out, nil
}

// ---------------------------------------------------------------------------
// Fix 3: SessionSnapshot.Messages transform
// ---------------------------------------------------------------------------

// agentMessage is the frontend-shaped message type.
// Discriminated by Type: 'user' | 'assistant' | 'final_answer' | 'tool_group'.
// messageAttachment is a user-uploaded file {url, filename} rendered as a chip on a
// type='user' message. JSON shape matches the frontend UserMessage.attachments item.
type messageAttachment struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
}

type agentMessage struct {
	ID   string `json:"id"`
	Type string `json:"type"`           // 'user' | 'assistant' | 'final_answer' | 'tool_group' | 'question_prompt'
	Text string `json:"text,omitempty"` // for type='user'
	// Attachments are the user's uploaded files {url, filename} for type='user',
	// rendered as chips. Persisted onto the user turn so a reloaded session shows
	// them (agent-output-ux-fixes 问题二) — replaces the old leaked URL-list text.
	Attachments []messageAttachment `json:"attachments,omitempty"`
	Markdown    string              `json:"markdown,omitempty"` // for type='assistant' | 'final_answer'
	Reasoning   string              `json:"reasoning,omitempty"`
	RunID       uint64              `json:"run_id,omitempty"` // for type='final_answer' | 'question_prompt'
	// ToolCalls carries the persisted tool-call timeline for type='tool_group'.
	// Shape is 1:1 with the frontend ToolCallAggregate so it renders untransformed.
	ToolCalls []persistedToolCall `json:"tool_calls,omitempty"`
	Timestamp string              `json:"timestamp"` // RFC3339

	// Question-prompt fields (type='question_prompt'): synthesized for a run
	// paused at ask_user_question so a reloaded session re-renders the
	// interactive card and the learner can answer (yield-session-reload fix).
	// agent-multi-question: a run may pose 1-4 questions, carried as an array
	// whose item shape matches the live stream QuestionPromptItem so the
	// frontend renders reloaded and streamed questions identically. omitempty:
	// agentMessage is a shared union struct — without it every non-question
	// message would serialize "questions":null. A question_prompt message always
	// carries >=1 question (synthesizeQuestionPrompt returns false otherwise), so
	// the frontend always sees a populated array on the messages it reads it from.
	Questions    []questionPromptItem `json:"questions,omitempty"`
	AnswerStatus string               `json:"answer_status,omitempty"` // 'pending' | 'answered'

	// ExternalActionPayload is anonymously embedded so external_action snapshot
	// messages use the same flat payload shape as live SSE. A nil pointer adds no
	// fields to ordinary messages; synthesized actions always use Persistent().
	*ExternalActionPayload

	// feishu-integration: pause classification + auth URL on a synthesized
	// question_prompt card so a RELOADED / polled session (design §10 auto续显)
	// renders an authorization card for an auth pause, not a plain question card.
	// Mirrors the live SSE QuestionPromptPayload.PauseType/AuthURL. Both omitempty
	// so ordinary question cards serialize unchanged.
	PauseType string `json:"pause_type,omitempty"`
	AuthURL   string `json:"auth_url,omitempty"`
}

// synthesizeExternalAction builds an external_action waiting card from the
// restart-safe identity stored on agent_run. The live URL is deliberately not
// recoverable after reload; unknown or transient fields fail closed.
func synthesizeExternalAction(run *model.AgentRun) (agentMessage, bool) {
	if run.StateReason != string(TerminalWaitingForUserChoice) &&
		run.StateReason != "external_resume_ready" &&
		!strings.HasPrefix(run.StateReason, "ext_resume:") {
		return agentMessage{}, false
	}
	if !hasPendingExternalAction(run.PendingExternalActionJSON) {
		return agentMessage{}, false
	}
	payload, err := ParsePendingExternalAction(run.PendingExternalActionJSON)
	if err != nil {
		return agentMessage{}, false
	}
	ts := run.StartedAt.UTC().Format(time.RFC3339)
	if run.PendingExternalActionAt != nil {
		ts = run.PendingExternalActionAt.UTC().Format(time.RFC3339)
	}
	return agentMessage{
		ID:                    "external-action-" + strconv.FormatUint(run.ID, 10),
		Type:                  "external_action",
		RunID:                 run.ID,
		Timestamp:             ts,
		ExternalActionPayload: &payload,
	}, true
}

// questionPromptOpt mirrors the frontend QuestionPromptOption {label, description}.
type questionPromptOpt struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// questionPromptItem is one question in a synthesized question_prompt message.
// Its JSON shape matches the live stream.QuestionPromptItem so the frontend
// renders a reloaded question identically to a streamed one.
type questionPromptItem struct {
	Question string `json:"question"`
	// Options must ALWAYS serialize as an array (never omitted/null) — the
	// frontend reads options.length unguarded (dev run 147 blank-card bug).
	// synthesizeQuestionPrompt builds it with make(...) so it is non-nil.
	Options     []questionPromptOpt `json:"options"`
	Header      string              `json:"header,omitempty"`
	MultiSelect bool                `json:"multi_select"`
	// Answer is the user's resolved answer, set only on a reconstructed
	// ANSWERED card (issue1: transformMessages rebuilds an answered
	// question_prompt from the answer turn's embedded question_answer). Empty on
	// a pending (still-waiting) card synthesized by synthesizeQuestionPrompt.
	Answer string `json:"answer,omitempty"`
}

// synthesizeQuestionPrompt builds a question_prompt agentMessage from a run's
// pending_question_json. Returns ok=false when the run is not waiting or the
// payload is missing/invalid. The synthesized card carries answer_status
// 'pending' so the UI renders it interactively after a reload.
func synthesizeQuestionPrompt(run *model.AgentRun) (agentMessage, bool) {
	if run.StateReason != string(TerminalWaitingForUserChoice) {
		return agentMessage{}, false
	}
	if len(run.PendingQuestionJSON) == 0 || string(run.PendingQuestionJSON) == "null" {
		return agentMessage{}, false
	}
	// ParsePendingQuestion wraps legacy single-question rows into a one-element
	// array, so both old and new pending_question_json reload uniformly.
	payload, err := ParsePendingQuestion(run.PendingQuestionJSON)
	if err != nil || len(payload.Questions) == 0 {
		return agentMessage{}, false
	}
	items := make([]questionPromptItem, 0, len(payload.Questions))
	for _, q := range payload.Questions {
		// Skip a malformed/blank question rather than render an empty card. The
		// write path validates every question non-empty, so this only guards
		// against a corrupt/manually-edited row.
		if q.Question == "" {
			continue
		}
		items = append(items, questionPromptItem{
			Question:    q.Question,
			Options:     projectYieldOptions(q.Options),
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
		})
	}
	if len(items) == 0 {
		return agentMessage{}, false
	}
	ts := run.StartedAt.UTC().Format(time.RFC3339)
	if run.PendingQuestionAt != nil {
		ts = run.PendingQuestionAt.UTC().Format(time.RFC3339)
	}
	return agentMessage{
		ID:           "q-" + strconv.FormatUint(run.ID, 10),
		Type:         "question_prompt",
		RunID:        run.ID,
		Questions:    items,
		AnswerStatus: "pending",
		Timestamp:    ts,
		// feishu-integration: carry the pause classification onto the reloaded card.
		PauseType: payload.PauseType,
		AuthURL:   payload.AuthURL,
	}, true
}

// transformMessages converts the raw [{role,content}] turn array stored in
// agent_run.messages into the frontend AgentMessage discriminated union:
//   - role='user'      → {type:'user', text:content}
//   - role='assistant' (non-last or non-terminal) → {type:'assistant', markdown:content}
//   - role='assistant' last turn AND run is terminal-with-output → {type:'final_answer', markdown:content, run_id}
//
// Timestamps are all set to startedAt (pragmatic v1; frontend doesn't need precision here).
// Returns an empty slice (not nil) when there are no turns.
func transformMessages(raw []byte, runID uint64, startedAt time.Time, endedAt *time.Time, status, stateReason string) []agentMessage {
	if len(raw) == 0 {
		return []agentMessage{}
	}
	var turns []map[string]any
	if err := json.Unmarshal(raw, &turns); err != nil {
		return []agentMessage{}
	}
	if len(turns) == 0 {
		return []agentMessage{}
	}

	// Determine if the run is terminal-with-output (for final_answer promotion).
	isTerminalSuccess := status == "terminated" && stateReason == "completed"

	// Find the index of the last assistant turn.
	lastAssistantIdx := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if role, _ := turns[i]["role"].(string); role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	ts := startedAt.UTC().Format(time.RFC3339)

	msgs := make([]agentMessage, 0, len(turns))
	for i, turn := range turns {
		role, _ := turn["role"].(string)
		content, _ := turn["content"].(string)

		msg := agentMessage{
			ID:        uuid.New().String(),
			Timestamp: ts,
		}

		switch role {
		case "user":
			// issue1: an answered ask_user_question turn embeds a question_answer
			// structure (AnswerAndClear). Reconstruct it as an answered
			// question_prompt card so a reloaded session keeps the card form
			// instead of an orphan "用户已回答…" user bubble. An ordinary user
			// turn (no question_answer) stays a plain bubble.
			if items, ok := reconstructAnsweredQuestions(turn["question_answer"]); ok {
				msg.Type = "question_prompt"
				msg.RunID = runID
				msg.Questions = items
				msg.AnswerStatus = "answered"
			} else {
				msg.Type = "user"
				msg.Text = content
				// 问题二: a user turn may carry uploaded attachments {url, filename}
				// (persisted by setUserTurnAttachments) → render as chips. URLs are
				// re-signed in the caller's pass (where the request ctx is available).
				if rawAtts, ok := turn["attachments"].([]any); ok {
					for _, it := range rawAtts {
						m, ok := it.(map[string]any)
						if !ok {
							continue
						}
						u, _ := m["url"].(string)
						if u == "" {
							continue
						}
						fn, _ := m["filename"].(string)
						msg.Attachments = append(msg.Attachments, messageAttachment{URL: u, Filename: fn})
					}
				}
			}
		case "assistant":
			reasoning, _ := turn["reasoning"].(string)
			if i == lastAssistantIdx && isTerminalSuccess && content != "" {
				msg.Type = "final_answer"
				msg.Markdown = content
				msg.Reasoning = reasoning
				msg.RunID = runID
			} else {
				// Option C transcript: an intermediate step with neither text nor
				// reasoning is nothing to render — skip it (the stepCollector
				// already drops these, but guard the read path too).
				if content == "" && reasoning == "" {
					continue
				}
				msg.Type = "assistant"
				msg.Markdown = content
				msg.Reasoning = reasoning
			}
		case "tool_group":
			// Replay the persisted tool-call timeline. Re-marshal the generic
			// turn["tool_calls"] then decode into the typed slice; the persisted
			// JSON is 1:1 with the frontend ToolCallAggregate shape.
			msg.Type = "tool_group"
			if rawTC, ok := turn["tool_calls"]; ok {
				if b, mErr := json.Marshal(rawTC); mErr == nil {
					var tcs []persistedToolCall
					if uErr := json.Unmarshal(b, &tcs); uErr == nil {
						msg.ToolCalls = tcs
					} else {
						// Observability: a decode failure here means the tool-call
						// process silently vanishes on reload — log so operators can
						// catch schema drift instead of debugging a blank timeline.
						log.Warnw("transformMessages: failed to decode tool_group turn",
							"run_id", runID, "error", uErr)
					}
				}
			}
			if len(msg.ToolCalls) == 0 {
				continue // nothing renderable
			}
		default:
			// Skip system / other turns — frontend doesn't render them.
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

// projectYieldOptions maps backend YieldOptions to the frontend questionPromptOpt
// shape, dropping the machine `key` (the client identifies options by label). The
// result is always a non-nil slice so the rendered card never serializes
// "options":null (dev run 147 blank-card bug). Shared by synthesizeQuestionPrompt
// (pending card) and buildAnswerTurn (answered card).
func projectYieldOptions(opts []YieldOption) []questionPromptOpt {
	out := make([]questionPromptOpt, 0, len(opts))
	for _, o := range opts {
		out = append(out, questionPromptOpt{Label: o.Label, Description: o.Description})
	}
	return out
}

// reconstructAnsweredQuestions rebuilds the answered question_prompt items from a
// user turn's embedded question_answer field (written by buildAnswerTurn /
// AnswerAndClear on the answer path). The turn is a generic map, so re-marshal +
// decode into the typed []questionPromptItem (same pattern as the tool_group
// replay). Returns ok=false when the field is absent/empty/corrupt so an ordinary
// user turn (and any legacy pre-issue1 answer turn) stays a plain bubble.
func reconstructAnsweredQuestions(raw any) ([]questionPromptItem, bool) {
	if raw == nil {
		return nil, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var qa struct {
		Questions []questionPromptItem `json:"questions"`
	}
	if err := json.Unmarshal(b, &qa); err != nil || len(qa.Questions) == 0 {
		return nil, false
	}
	// Options must ALWAYS serialize as an array (frontend reads options.length
	// unguarded — dev run 147 blank-card bug); a question with a nil Options gets
	// an empty slice, never null.
	for i := range qa.Questions {
		if qa.Questions[i].Options == nil {
			qa.Questions[i].Options = []questionPromptOpt{}
		}
	}
	return qa.Questions, true
}

// PinSession logic-pins the whole session for the user.
func (s *StudentQueryService) PinSession(ctx context.Context, userID uint, sessionID string, isPinned bool) error {
	if err := s.verifySessionOwnership(ctx, userID, sessionID); err != nil {
		return err
	}
	return s.runStore.UpdateSessionPinned(ctx, sessionID, isPinned)
}

// RenameSession updates the session display name for the user.
func (s *StudentQueryService) RenameSession(ctx context.Context, userID uint, sessionID string, name string) error {
	if err := s.verifySessionOwnership(ctx, userID, sessionID); err != nil {
		return err
	}
	return s.runStore.UpdateSessionName(ctx, sessionID, name)
}

// GenerateSessionTitle 即时从 prompt 生成 agent 会话标题（instant-title-ux 发送时路径）。
// 验证属主；仅当 session 仍未命名(session_name=="")时调小模型生成并用 CAS 写回。
// 返回新标题或 ""（已命名/生成失败时空，best-effort）。系统内部调用不扣用户积分。
// 注意（设计 review B-2）：前端必须在 createRun 落库后才调本端点，否则 ListBySession 取不到
// run → 返回 ErrAgentRunNotFound，秒标题落空（回复后兜底 maybeGenerateSessionTitle 仍会补）。
func (s *StudentQueryService) GenerateSessionTitle(ctx context.Context, userID uint, sessionID, prompt string) (string, error) {
	runs, _, err := s.runStore.ListBySession(ctx, sessionID, 0, 1)
	if err != nil {
		return "", fmt.Errorf("StudentQueryService.GenerateSessionTitle: %w", err)
	}
	if len(runs) == 0 {
		return "", errno.ErrAgentRunNotFound
	}
	if runs[0].UserID != userID {
		return "", errno.ErrForbidden.SetMessage("access to another user's session is not allowed")
	}
	if runs[0].SessionName != "" {
		return "", nil // 已命名（手动 rename / 上轮自动）— 不覆盖
	}
	title, gerr := agentGenTitleFn(ctx, prompt, "")
	if gerr != nil {
		// best-effort（与 chatbot 路径一致）：生成失败仅 log, 返回 ("", nil) 不让前端报错。
		log.C(ctx).Warnw("GenerateSessionTitle: generate failed", "error", gerr, "session_id", sessionID)
		return "", nil
	}
	if title == "" {
		return "", nil
	}
	if _, uerr := s.runStore.UpdateSessionNameIfEmpty(ctx, sessionID, title); uerr != nil {
		return "", fmt.Errorf("StudentQueryService.GenerateSessionTitle update: %w", uerr)
	}
	return title, nil
}

// DeleteSession logical-deletes the whole session (and all its runs) for the user.
func (s *StudentQueryService) DeleteSession(ctx context.Context, userID uint, sessionID string) error {
	const pageSize = 200
	var runs []model.AgentRun
	for offset := 0; ; {
		batch, total, err := s.runStore.ListBySession(ctx, sessionID, offset, pageSize)
		if err != nil {
			return fmt.Errorf("StudentQueryService.DeleteSession list: %w", err)
		}
		runs = append(runs, batch...)
		if len(batch) == 0 || int64(len(runs)) >= total {
			break
		}
		offset += len(batch)
	}
	if len(runs) == 0 {
		return errno.ErrAgentRunNotFound
	}
	if runs[0].UserID != userID {
		return errno.ErrForbidden.SetMessage("access to another user's session is not allowed")
	}
	// Persist deletion first. A concurrently arriving first-model gate observes
	// is_deleted and refuses entry before the provider call.
	if err := s.runStore.UpdateSessionDeleted(ctx, sessionID, true); err != nil {
		return err
	}
	if s.runCanceller != nil {
		for i := range runs {
			if runs[i].Status == "running" {
				s.runCanceller.Cancel(runs[i].ID)
			}
		}
	}
	return nil
}

// verifySessionOwnership checks if the first run in this session belongs to the userID.
func (s *StudentQueryService) verifySessionOwnership(ctx context.Context, userID uint, sessionID string) error {
	runs, _, err := s.runStore.ListBySession(ctx, sessionID, 0, 1)
	if err != nil {
		return fmt.Errorf("StudentQueryService.verifySessionOwnership check list: %w", err)
	}
	if len(runs) == 0 {
		return errno.ErrAgentRunNotFound
	}
	if runs[0].UserID != userID {
		return errno.ErrForbidden.SetMessage("access to another user's session is not allowed")
	}
	return nil
}
