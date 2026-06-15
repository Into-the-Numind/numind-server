package agent

import (
	"context"

	"numind-server/internal/numind/biz/sessiontitle"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
)

// agentGenTitleFn is the adaptive-title generator, swappable in tests. Production
// uses sessiontitle.Generate (qwen-turbo, non-user-billed).
var agentGenTitleFn = sessiontitle.Generate

// maybeGenerateSessionTitle generates and persists an adaptive session_name from
// a completed run's content, when the session is still unnamed. Best-effort:
// every failure is logged and ignored so it never affects the run result.
//
// Guard: the session's current session_name is empty. session_name is a
// session-level attribute (UpdateSessionName/UpdateSessionNameIfEmpty write all
// runs of a session, and new runs inherit it), so the latest run's empty name
// means the whole session is unnamed. We deliberately do NOT require total==1:
// keying on "still unnamed" also (1) auto-titles a session whose first run
// yielded a question and only completed on resume, and (2) retries on a later
// turn if the first attempt failed — mirroring the chatbot path.
//
// The persist uses UpdateSessionNameIfEmpty (compare-and-set on session_name=”)
// so a manual rename racing the title LLM call is never clobbered (US3). The
// title LLM call is system-internal and does not bill the user (see sessiontitle).
func maybeGenerateSessionTitle(ctx context.Context, runStore store.IAgentRunStore, sessionID, userInput, finalText string) {
	if sessionID == "" {
		return
	}
	runs, _, err := runStore.ListBySession(ctx, sessionID, 0, 1)
	if err != nil {
		log.C(ctx).Warnw("finalizeRun: list session runs for title failed", "error", err, "session_id", sessionID)
		return
	}
	if len(runs) == 0 || runs[0].SessionName != "" {
		return // session already named (manual rename or prior auto-title)
	}
	title, gerr := agentGenTitleFn(ctx, userInput, finalText)
	if gerr != nil {
		log.C(ctx).Warnw("finalizeRun: generate session title failed", "error", gerr, "session_id", sessionID)
		return
	}
	if title == "" {
		log.C(ctx).Warnw("finalizeRun: generate session title returned empty", "session_id", sessionID)
		return
	}
	if _, uerr := runStore.UpdateSessionNameIfEmpty(ctx, sessionID, title); uerr != nil {
		log.C(ctx).Warnw("finalizeRun: update session name failed", "error", uerr, "session_id", sessionID)
	}
}
