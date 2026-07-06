package xhsscript

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
)

func TestAnalyticsErrorCategoryRecognizesDomainErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"note_not_found", errno.ErrXhsScriptNoteNotFound, "not_found"},
		{"video_only", errno.ErrXhsScriptVideoOnly, "video_only"},
		{"transcript_not_ready", fmt.Errorf("wrapped: %w", errno.ErrXhsScriptTranscriptNotReady), "transcript_not_ready"},
		{"profile_required", errno.ErrXhsScriptProfileRequired, "profile_required"},
		{"quota_errno", errno.ErrXhsScriptQuotaInsufficient, "quota_insufficient"},
		{"quota_store", store.ErrXhsScriptQuotaInsufficient, "quota_insufficient"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, analyticsErrorCategory(tc.err))
		})
	}
}
