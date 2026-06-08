// Package sop — sop_fragments.go
//
// Task 9: SOP Gateway Producer Migration helpers.
//
// buildSOPNodeFragments and buildSOPChatFragments produce generic
// ContextFragment slices that the Gateway middleware (ContextBudgetCredits)
// uses for budget planning and compression. The Gateway then renders them into
// ChatMessage entries via aiservice.RenderContextFragments.
//
// Fragment taxonomy (spec §9.1):
//
//	system/node instruction   → RoleImmutable, SourceSystem,    CompressNone,       Critical=true
//	current user input        → RoleRecent,    SourceUser,      CompressNone,       Critical=true
//	previous assistant output → RoleDurable,   SourceAssistant, CompressSummarize,  Critical=false
//	previous user turns       → RoleDurable,   SourceUser,      CompressSummarize,  Critical=false
//	file/attachment content   → RoleEvidence,  SourceFile,      CompressReference,  Critical=false
//	image URL attachment      → RoleEvidence,  SourceFile,      CompressReference,  Critical=false
//
// P2-2 (reasoning fragments) deferred: historical reasoning content from prior
// node runs is NOT stored in conversationHistory (only nodeRun.Output is
// appended, not the thinking/reasoning field). There is therefore no reasoning
// data flowing through buildSOPNodeFragments or buildSOPGatewayFragments at this
// time. When reasoning history is added to conversation context, map it to
// RoleWorking/SourceAssistant/ContentReasoning/CompressDrop per spec §9.1.
// TODO(spec §9.1 reasoning mapping): wire reasoning history → RoleWorking fragments
// when SOP conversation history starts carrying reasoning content.
package sop

import (
	"fmt"

	bizctx "numind-server/internal/numind/biz/contextbudget"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// buildSOPNodeFragments converts the raw SOP node execution inputs (template
// system prompt, node prompt, historical conversation turns, current user input)
// into an ordered slice of ContextFragment values for the Gateway producer path.
//
// NOTE: this function is used only in tests today. The production path is
// executeViaGateway → buildSOPGatewayFragments which works on pre-assembled
// llmMessages. This helper exists as a reusable scaffold for future SOP
// entrypoints (or test fixtures) that build fragments from raw NodeRun /
// SopRun structs without the prompt-assembly step.
//
// Ordering:
//  1. Template system prompt (if any) — immutable system fragment (Order=0).
//  2. Node prompt (if any) — immutable system fragment.
//  3. Historical turns (user/assistant pairs from previous nodes) — durable.
//  4. Current user input — critical user fragment (never compressible).
func buildSOPNodeFragments(
	template *model.SopTemplate,
	node *model.SopNode,
	history []LLMMessage,
	currentInput string,
) []contextbudget.ContextFragment {
	fragments := make([]contextbudget.ContextFragment, 0, len(history)+3)
	order := 0

	// 1. Template-level system prompt.
	// Immutable system fragment is intentionally placed at Order=0 — system prompts
	// must always render first, and the planner uses Order ascending for deterministic
	// rendering. Subsequent fragments use ascending Order from 1.
	if template != nil && template.Prompt != "" {
		fragments = append(fragments, bizctx.NewImmutableSystemFragment(
			fmt.Sprintf("tmpl-sys-%d", template.ID),
			template.Prompt,
		))
		order++
	}

	// 2. Node-level prompt (instruction / persona).
	// Node prompt is typically merged with user input in the legacy path
	// (fmt.Sprintf("%s\n\n%s", node.Prompt, input)).  In the fragment path
	// we keep them separate so the planner can treat the instruction as
	// immutable and the user input as critical-recent.
	if node != nil && node.Prompt != "" {
		fragments = append(fragments, bizctx.NewImmutableSystemFragment(
			fmt.Sprintf("node-sys-%d", node.ID),
			node.Prompt,
		))
		order++
	}

	// 3. Historical conversation turns (previous node inputs/outputs).
	// Each user+assistant pair is treated as durable — compressible under
	// budget pressure, but preserved if budget allows.
	for i, msg := range history {
		id := fmt.Sprintf("hist-%d", i)
		switch msg.Role {
		case "system":
			// Any system messages from history become immutable fragments.
			fragments = append(fragments, bizctx.NewImmutableSystemFragment(id, msg.Content))
		case "user":
			fragments = append(fragments, bizctx.NewDurableUserFragment(id, msg.Content, order, 5))
			// P2-1 (spec §9.1): emit RoleEvidence/SourceFile fragments for any
			// file-attachment blocks embedded in this historical user message.
			fragments = append(fragments, attachmentEvidenceFragments(id, msg.Content, order)...)
		case "assistant":
			fragments = append(fragments, bizctx.NewDurableAssistantFragment(id, msg.Content, order, 5))
		}
		order++
	}

	// 4. Current user input — CRITICAL, never compressed or dropped.
	if currentInput != "" {
		var nodeID uint
		if node != nil {
			nodeID = node.ID
		}
		fragments = append(fragments, bizctx.NewCriticalUserFragment(
			fmt.Sprintf("current-input-%d", nodeID),
			currentInput,
		))
	}

	return fragments
}

// buildSOPGatewayFragments converts the inputs already assembled inside
// executeViaGateway (via buildGatewayMessages) — a pre-built ordered LLMMessage
// slice of: the template system prompt (if any) and prior-step turns, with the
// current turn appended as the last user message. For node execution the current
// node's instruction is merged into that final user message (it is NOT a separate
// system message at index 0); only the chat scenario keeps node.Prompt as a
// leading system message. Converts into ContextFragment values for the Gateway
// producer path.
//
// Rules:
//   - The last "user" role message in msgs is the current turn → Critical.
//   - All other messages are durable / compressible.
//   - "system" role → RoleImmutable.
//   - "assistant" role → RoleDurable / SourceAssistant.
//   - "user" role (non-last) → RoleDurable / SourceUser.
func buildSOPGatewayFragments(msgs []LLMMessage) []contextbudget.ContextFragment {
	if len(msgs) == 0 {
		return nil
	}

	// Find the index of the last user message — it becomes the critical fragment.
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	fragments := make([]contextbudget.ContextFragment, 0, len(msgs))
	for i, msg := range msgs {
		id := fmt.Sprintf("gw-%d", i)
		switch msg.Role {
		case "system":
			fragments = append(fragments, bizctx.NewImmutableSystemFragment(id, msg.Content))
		case "user":
			if i == lastUserIdx {
				// Current turn — critical, incompressible.
				fragments = append(fragments, bizctx.NewCriticalUserFragment(id, msg.Content))
			} else {
				// Historical user message — durable, compressible.
				fragments = append(fragments, bizctx.NewDurableUserFragment(id, msg.Content, i, 5))
				// P2-1 (spec §9.1): emit RoleEvidence/SourceFile fragments for any
				// file-attachment blocks embedded in this historical user message.
				fragments = append(fragments, attachmentEvidenceFragments(id, msg.Content, i)...)
			}
		case "assistant":
			fragments = append(fragments, bizctx.NewDurableAssistantFragment(id, msg.Content, i, 5))
		}
	}

	return fragments
}

// buildSOPChatFragments converts a SOP trailing-chat turn into ContextFragment
// values for the Gateway producer path.
//
// NOTE: this function is used only in tests today. The production path is
// executeViaGateway → buildSOPGatewayFragments which works on pre-assembled
// llmMessages. This helper exists as a reusable scaffold for future SOP
// chat entrypoints (or test fixtures) that build fragments from raw chat
// history slices without the prompt-assembly step.
//
// The history slice contains ALL messages up to (but not including) the current
// question — including both node execution messages (treated as durable run
// facts) and previous chat turns. The current question is always the last
// element and is marked Critical (non-compressible).
func buildSOPChatFragments(
	history []LLMMessage,
	currentQuestion string,
) []contextbudget.ContextFragment {
	fragments := make([]contextbudget.ContextFragment, 0, len(history)+1)
	order := 0

	for i, msg := range history {
		id := fmt.Sprintf("chat-hist-%d", i)
		switch msg.Role {
		case "system":
			fragments = append(fragments, bizctx.NewImmutableSystemFragment(id, msg.Content))
		case "user":
			fragments = append(fragments, bizctx.NewDurableUserFragment(id, msg.Content, order, 5))
			// P2-1 (spec §9.1): emit RoleEvidence/SourceFile fragments for any
			// file-attachment blocks embedded in this historical user message.
			fragments = append(fragments, attachmentEvidenceFragments(id, msg.Content, order)...)
		case "assistant":
			fragments = append(fragments, bizctx.NewDurableAssistantFragment(id, msg.Content, order, 5))
		}
		order++
	}

	// Current question is always critical — it defines what the assistant must answer.
	if currentQuestion != "" {
		fragments = append(fragments, bizctx.NewCriticalUserFragment(
			fmt.Sprintf("chat-current-%d", order),
			currentQuestion,
		))
	}

	return fragments
}

// attachmentEvidenceFragments scans a user message content for file-attachment
// blocks (format: `=== filename.ext ===\n<content>`) and emits one
// RoleEvidence/SourceFile fragment per attachment.
//
// P2-1 (spec §9.1): previous attachments → evidence + file + reference|summarize.
// The attachment block content is used as the fragment body; the filename is
// stored as SourceReference to allow reference compression.
//
// If no attachment markers are found (normal text-only message), the function
// returns nil.
func attachmentEvidenceFragments(msgID string, content string, order int) []contextbudget.ContextFragment {
	locs := attachmentMarkerRegex.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return nil
	}

	var result []contextbudget.ContextFragment
	for i, loc := range locs {
		filename := content[loc[2]:loc[3]]

		// Determine the block body: from end of marker line to next marker or end of content.
		blockStart := loc[1] // byte after the marker line (includes the newline terminator)
		var blockEnd int
		if i+1 < len(locs) {
			blockEnd = locs[i+1][0] // start of next marker
		} else {
			blockEnd = len(content)
		}

		body := ""
		if blockStart < blockEnd {
			body = content[blockStart:blockEnd]
		}

		result = append(result, contextbudget.ContextFragment{
			ID:              fmt.Sprintf("%s-attach-%d", msgID, i),
			Role:            contextbudget.RoleEvidence,
			Source:          contextbudget.SourceFile,
			ContentType:     contextbudget.ContentAttachment,
			Content:         body,
			Importance:      50,
			Order:           order,
			Compressibility: contextbudget.CompressReference,
			SourceReference: filename,
			Critical:        false,
		})
	}
	return result
}

// shouldSkipDirectReserveForGateway returns true when the caller has chosen the
// Gateway path (modelKey != ""). In that case the ContextBudgetCredits
// middleware owns the budget reservation via ReserveBudget, so the legacy
// creditSvc.Reserve (R2 char-based) call in sop.go must be skipped to prevent
// double-reservation.
//
// This function is intentionally a small named helper so it can be tested
// directly in context_fragments_test.go without depending on sop.go internals.
func shouldSkipDirectReserveForGateway(modelKey string) bool {
	return modelKey != ""
}
