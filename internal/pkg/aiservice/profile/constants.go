// Package profile provides Task Profile constants, capability schemas, and
// the Capability Matching algorithm used by the AI Gateway.
package profile

// Task ID constants for all 14 supported task profiles.
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
	// SalesragEmbed is the SalesRAG text embedding task (LLM / embedding, dimension=1024).
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
)

// AllTaskIDs returns all 14 task ID strings in a stable order.
// Useful for validation, seeding, or iterating over all known profiles.
func AllTaskIDs() []string {
	return []string{
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
	}
}
