// Package profile provides Task Profile constants, capability schemas, and
// the Capability Matching algorithm used by the AI Gateway.
package profile

// Task ID constants for all supported task profiles. The canonical, always-current
// set is allTaskIDsList below (and AllTaskIDs()); add new IDs to both.
// Business layers should reference these constants (e.g. profile.SopText) rather
// than raw string literals to gain IDE completion and compile-time typo detection.
const (
	// SopText is the SOP text-only execution task (LLM, tool_use required).
	SopText = "sop.text"
	// SopVision is the SOP execution task that accepts image input (LLM, vision required).
	SopVision = "sop.vision"
	// ChatbotStream is the real-time chatbot streaming task (LLM, streaming required).
	ChatbotStream = "chatbot.stream"
	// SalesragIntent is the SalesRAG intent classification task (LLM, json_mode required).
	SalesragIntent = "salesrag.intent"
	// SalesragChat is the SalesRAG conversational answer task (LLM, streaming required).
	SalesragChat = "salesrag.chat"
	// SalesragRerank is the SalesRAG document re-ranking task (LLM / rerank capability).
	SalesragRerank = "salesrag.rerank"
	// SalesragEmbed is the SalesRAG text embedding task (LLM / embedding, dimension=2048).
	// Dimension is locked to 2048 by the prod DashVector collection `sales_rag_prod`.
	SalesragEmbed = "salesrag.embed"
	// SalesragTagging is the SalesRAG entity tagging task (LLM, json_mode required).
	SalesragTagging = "salesrag.tagging"
	// SalesragProfile is the SalesRAG customer profile generation task (LLM + vision).
	SalesragProfile = "salesrag.profile"
	// SalesragChatstyle is the SalesRAG chat-style analysis task (LLM + vision).
	SalesragChatstyle = "salesrag.chatstyle"
	// MonitorBriefing is the monitor daily briefing task (LLM, large context).
	MonitorBriefing = "monitor.briefing"
	// MonitorAnalyze is the monitor data analysis task (LLM, json_mode required).
	MonitorAnalyze = "monitor.analyze"
	// MonitorTranscribe is the monitor audio transcription task (ASR).
	MonitorTranscribe = "monitor.transcribe"
	// OcrBaidu is the Baidu OCR high-accuracy task (OCR).
	OcrBaidu = "ocr.baidu"
	// AgentRun is the Agent ReAct main LLM call (#14 e2e rollout).
	AgentRun = "agent.run"
	// AgentEmbed is the Agent memory L1/L2 retrieval embedder (#14).
	AgentEmbed = "agent.embed"
	// AgentSyncTurn is the Agent memory turn summary extraction (#14).
	AgentSyncTurn = "agent.sync_turn"
	// AgentCompact is the Agent context compaction (#14).
	AgentCompact = "agent.compact"
	// AgentNarrationFallback is the Agent narration LLM dynamic generation (#14).
	AgentNarrationFallback = "agent.narration_fallback"
	// AgentInjectionCheck is the Agent compliance injection classifier (#14).
	AgentInjectionCheck = "agent.injection_check"
	// AgentPermissionCheck is the Agent permission L3 auto-mode classifier (#14).
	AgentPermissionCheck = "agent.permission_check"
	// AgentImageGen is the Agent text-to-image generation tool task
	// (agent-imagegen-via-aiservice). The image_gen tool routes its provider call
	// through aiservice.ImageGen(ctx, profile.AgentImageGen, ...) so the call gets
	// Langfuse tracing + routing/fallback + a UsageRecord (analytics). Its
	// service_type is "image_gen". IMPORTANT: this profile MUST NOT carry a
	// ChargeUser context_budget policy — the tool already performs the single
	// credit deduction via its flat Reserve/Reconcile, and ImageGenRequest never
	// reaches the ContextBudgetCredits chat path anyway. Requires a DB-registered
	// ai_service route → gpt-image-2 (dmxapi) in dev/prod; missing
	// route returns an error the tool maps to a soft tool error.
	AgentImageGen = "agent.image_gen"

	// ── V1.5 attachment profiles (task 1.2 / board 3) ────────────────────────

	// AttachmentVisionDescribe is the VLM task that generates a textual description
	// of an uploaded image for use as a text fallback when the active model is
	// single-modal. Routed to qwen3-vl-flash (D2 decision). (V1.5 task 1.2)
	AttachmentVisionDescribe = "attachment.vision_describe"

	// AttachmentPDFExtract is the LLM task that extracts full text from a PDF
	// document using qwen-long's file URL API. Separate from agent.run to avoid
	// misattributing PDF extraction costs to the ReAct agent budget (P1 #4 fix,
	// task 1.2 review). (V1.5 task 1.2)
	AttachmentPDFExtract = "attachment.pdf_extract"

	// ── V1.5 memory profiles (Layer A — Task 3.x) ────────────────────────────

	// AgentMemoryExtract is the Agent V1.5 memory async extraction task (Task 3.3).
	// Reads last 5-10 turns from agent_run.messages, identifies long-term-useful
	// facts about the agent's *user* (Layer A — sales rep, analyst, SOP operator,
	// PPT clerk, etc. — never the customer/subject the user discusses), filters
	// confidence ≥ 0.7, dedups by content hash, persists to user_memory_facts.
	// Recommended route: deepseek-v3-2 / qwen-turbo (cheap async background job).
	AgentMemoryExtract = "agent.memory_extract"
	// AgentMemorySelect is the Agent V1.5 memory side-query selector (Task 3.4).
	// Per-turn pre-LLM selector that picks ≤5 most-relevant facts from up to 50
	// candidates and returns just their ext IDs as a JSON array. Fast + cheap
	// (small LLM, MaxTokens=100, Temperature=0.2). Layer A: facts are always
	// about the agent's *user themselves* — never the customer/subject they
	// discuss. Recommended route: qwen-turbo (cost-efficient + good Chinese) →
	// deepseek-v3-2 (fallback). Spec: 03-memory/task-04-top5-selector.md.
	AgentMemorySelect = "agent.memory_select"
	// AgentDialectic is the Agent V1.5 dialectic Layer A reasoning task (Task 3.7).
	// Background goroutine reads top-N (≤20) Layer A facts (subject_id IS NULL —
	// always about the agent's *user themselves*: sales rep, SOP operator, data
	// analyst, PPT clerk, etc., NEVER about a customer/dataset/subject they
	// discuss) and produces a 100–800-rune Chinese narrative insight describing
	// (1) who the user is, (2) how to interact with them, (3) any personalised
	// guidance for the current session. Cached in user_memory_profile.cached_insight
	// + read at user-turn start to inject the Memories segment of the system prompt.
	// Run gating is delegated to CadenceService (Task 3.6) — this profile is only
	// hit when ShouldRunDialectic returns true. Recommended route: qwen-plus
	// primary + deepseek-v3-2 fallback (D4: NO thinking models — output must be
	// stable + consistent, not divergent). Spec: 03-memory/task-07-dialectic.md.
	AgentDialectic = "agent.dialectic"
	// AgentDigest is the Agent V1.5 temporal-tree digest task (Task 3.8).
	// Shared by 4 cron jobs (daily / weekly / monthly / quarterly), each with
	// its own prompt template:
	//   - daily:     summarises yesterday's agent_runs + messages (cron 04:00 daily)
	//   - weekly:    aggregates the 7 daily digests covering an ISO week (Mon 04:30)
	//   - monthly:   aggregates the weekly digests covering a calendar month (1st 04:30)
	//   - quarterly: aggregates the 3 monthly digests covering Q1-Q4 (quarter start 04:30)
	// Output is strict JSON {"summary":"...","key_topics":[...]}; the digest_generator
	// retries once on parse failure then falls back to a canned "（LLM 解析失败）" summary.
	// Layer A only — the digest summarises the agent user themselves (cross-session
	// aggregate), not any customer/subject they discuss.
	// Recommended route: qwen-plus primary + deepseek-v3-2 fallback (D4: NO thinking
	// model — digest is structured aggregation, not divergent reasoning).
	// Spec: 03-memory/task-08-temporal-tree.md.
	AgentDigest = "agent.digest"
	// SkillMarketplaceSanitize is the agent-mode-v2-skill-marketplace (T3) sanitize task.
	// Used when a publisher requests preview-then-publish on the marketplace; the LLM
	// strips PII / org / product names that the deterministic regex stage cannot detect.
	// Spec: numind-server/docs/superpowers/specs/2026-05-24-agent-mode-v2-skill-marketplace-design.md §3.2.
	// Recommended route: qwen-turbo (cheap; <5KB body; latency budget < 3s).
	// **Requires DB seed migration to register ai_service route** before sanitize succeeds
	// in dev/prod; missing route returns error and surfaces as ErrSanitizeUnavailable to
	// the user. Seed migration bundled with T7 errno (agent-mode-v2-skill-marketplace plan).
	SkillMarketplaceSanitize = "skill.marketplace.sanitize"

	// SessionTitle is the chatbot/agent session auto-title generation task
	// (adaptive-session-titles). After the first conversation turn completes,
	// biz/sessiontitle.Generate calls this to summarise the exchange into a
	// short 6-12 char title. It is a system-internal, non-user-billed call:
	// the request carries no ContextFragments and Generate strips the billing
	// context, so the gateway takes its pass-through branch and never reserves.
	// Recommended route: qwen-turbo (cheap; MaxTokens=32; 3s timeout). Requires
	// a DB-registered ai_service route → qwen-turbo in dev/prod; if missing,
	// Generate degrades gracefully (best-effort no-op, no error to the user).
	SessionTitle = "session.title"
)

// allTaskIDsList is the canonical ordered list of all task IDs.
// IMPORTANT: if you add a new task ID constant above, you MUST also add it here.
// AllTaskIDs() returns a copy of this slice to prevent callers from modifying it.
var allTaskIDsList = []string{
	SopText,
	SopVision,
	ChatbotStream,
	SalesragIntent,
	SalesragChat,
	SalesragRerank,
	SalesragEmbed,
	SalesragTagging,
	SalesragProfile,
	SalesragChatstyle,
	MonitorBriefing,
	MonitorAnalyze,
	MonitorTranscribe,
	OcrBaidu,
	AgentRun,
	AgentEmbed,
	AgentSyncTurn,
	AgentCompact,
	AgentNarrationFallback,
	AgentInjectionCheck,
	AgentPermissionCheck,
	AgentImageGen,
	// V1.5 additions
	AttachmentVisionDescribe,
	AttachmentPDFExtract,
	AgentMemoryExtract,
	AgentMemorySelect,
	AgentDialectic,
	AgentDigest,
	// v2 additions
	SkillMarketplaceSanitize,
	// adaptive-session-titles
	SessionTitle,
}

// AllTaskIDs returns all task ID strings in a stable order.
// Useful for validation, seeding, or iterating over all known profiles.
// Returns a copy — callers may not modify the returned slice.
func AllTaskIDs() []string {
	out := make([]string, len(allTaskIDsList))
	copy(out, allTaskIDsList)
	return out
}
