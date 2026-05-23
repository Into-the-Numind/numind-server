// Package profile provides Task Profile constants, capability schemas, and
// the Capability Matching algorithm used by the AI Gateway.
package profile

// Task ID constants for all 26 supported task profiles (21 existing + 5 new V1.5).
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

	// ── V1.5 new task profiles (task 1.2 / board 3) ──────────────────────────

	// AttachmentVisionDescribe is the VLM task that generates a textual description
	// of an uploaded image for use as a text fallback when the active model is
	// single-modal. Routed to qwen3-vl-flash (D2 decision). (V1.5 task 1.2)
	AttachmentVisionDescribe = "attachment.vision_describe"

	// AttachmentPDFExtract is the LLM task that extracts full text from a PDF
	// document using qwen-long's file URL API. Separate from agent.run to avoid
	// misattributing PDF extraction costs to the ReAct agent budget (P1 #4 fix,
	// task 1.2 review). (V1.5 task 1.2)
	AttachmentPDFExtract = "attachment.pdf_extract"
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
	// V1.5 additions (task 1.2 + board 3 profiles added as implemented)
	AttachmentVisionDescribe,
	AttachmentPDFExtract,
}

// AllTaskIDs returns all task ID strings in a stable order.
// Useful for validation, seeding, or iterating over all known profiles.
// Returns a copy — callers may not modify the returned slice.
func AllTaskIDs() []string {
	out := make([]string, len(allTaskIDsList))
	copy(out, allTaskIDsList)
	return out
}
