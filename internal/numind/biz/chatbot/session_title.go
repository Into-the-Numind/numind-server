package chatbot

import (
	"context"

	"numind-server/internal/numind/biz/sessiontitle"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// genTitleFn is the adaptive-title generator, swappable in tests. Production
// uses sessiontitle.Generate (qwen-turbo, non-user-billed).
var genTitleFn = sessiontitle.Generate

// maybeGenerateTitle generates and persists an adaptive session title after the
// first conversation turn, returning the new title (or "" when not generated).
//
// Trigger guard is "the title is still the chatbot's default name"
// (session.Title == defaultName) rather than "no prior messages": a chatbot with
// a greeting enabled writes a greeting message (seq=1) at session creation, so a
// message-count/maxSeq guard would never fire for those bots. The default-name
// guard also (1) never overwrites a user's manual rename (US3) and (2) lets a
// failed first-turn generation retry on a later turn (title still default).
//
// The session.Title here is a snapshot read at the start of ChatStream, before a
// multi-second LLM stream; a user could rename the session mid-stream. So the
// snapshot check is only an early-out to skip the LLM call, and the actual write
// is an atomic compare-and-set (UpdateTitleIfCurrent) that overwrites ONLY while
// the title still equals defaultName — closing the rename race (US3).
//
// Best-effort: any failure is logged and yields "" so the caller leaves the
// existing title untouched and the conversation proceeds normally. The title
// LLM call is system-internal and does not bill the user (see sessiontitle).
func (b *chatbotBiz) maybeGenerateTitle(ctx context.Context, session *model.ChatbotSession, defaultName, userMsg, assistantContent string) string {
	if session == nil {
		return ""
	}
	// B-1: the passed-in session may be a pre-stream snapshot. The send-time /title
	// endpoint (instant-title-ux) could have set the title during the stream, so
	// re-read the current title to skip the wasted LLM call when it is already
	// non-default. The CAS write below still guards correctness either way.
	currentTitle := session.Title
	if fresh, ferr := b.ds.ChatbotSession().GetSession(ctx, session.ID); ferr == nil && fresh != nil {
		currentTitle = fresh.Title
	}
	if currentTitle != defaultName {
		return ""
	}
	title, err := genTitleFn(ctx, userMsg, assistantContent)
	if err != nil {
		log.C(ctx).Warnw("ChatStream: generate session title failed", "error", err, "session_id", session.ID)
		return ""
	}
	if title == "" {
		log.C(ctx).Warnw("ChatStream: generate session title returned empty", "session_id", session.ID)
		return ""
	}
	// Atomic compare-and-set: only overwrite while the title is still the default,
	// so a concurrent manual rename during generation is never clobbered (US3).
	updated, uerr := b.ds.ChatbotSession().UpdateTitleIfCurrent(ctx, session.ID, title, defaultName)
	if uerr != nil {
		log.C(ctx).Warnw("ChatStream: update session title failed", "error", uerr, "session_id", session.ID)
		return ""
	}
	if !updated {
		// Title changed concurrently (e.g. user renamed mid-stream) — leave it.
		return ""
	}
	return title
}
