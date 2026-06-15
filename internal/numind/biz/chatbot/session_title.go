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
// Best-effort: any failure is logged and yields "" so the caller leaves the
// existing title untouched and the conversation proceeds normally. The title
// LLM call is system-internal and does not bill the user (see sessiontitle).
func (b *chatbotBiz) maybeGenerateTitle(ctx context.Context, session *model.ChatbotSession, defaultName, userMsg, assistantContent string) string {
	if session == nil || session.Title != defaultName {
		return ""
	}
	title, err := genTitleFn(ctx, userMsg, assistantContent)
	if err != nil || title == "" {
		if err != nil {
			log.C(ctx).Warnw("ChatStream: generate session title failed", "error", err, "session_id", session.ID)
		}
		return ""
	}
	if uerr := b.ds.ChatbotSession().UpdateTitle(ctx, session.ID, title); uerr != nil {
		log.C(ctx).Warnw("ChatStream: update session title failed", "error", uerr, "session_id", session.ID)
		return ""
	}
	return title
}
