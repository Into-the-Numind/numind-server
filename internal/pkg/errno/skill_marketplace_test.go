package errno

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarketplaceErrnos_Defined verifies each of the 10 codes is wired with
// the documented HTTP status + namespaced Code.
func TestMarketplaceErrnos_Defined(t *testing.T) {
	cases := []struct {
		name string
		e    *Errno
		http int
		code string
	}{
		{"ChildAccountForbidden", ErrChildAccountCannotAccessMarketplace, 403, "Marketplace.ChildAccountForbidden"},
		{"SkillNotOwned", ErrSkillNotOwned, 403, "Marketplace.SkillNotOwned"},
		{"SkillAlreadyPublished", ErrSkillAlreadyPublished, 409, "Marketplace.SkillAlreadyPublished"},
		{"SelfSubscribeForbidden", ErrSelfSubscribeForbidden, 409, "Marketplace.SelfSubscribeForbidden"},
		{"AlreadySubscribed", ErrAlreadySubscribed, 409, "Marketplace.AlreadySubscribed"},
		{"MarketplaceNotFound", ErrMarketplaceNotFound, 404, "Marketplace.NotFound"},
		{"SubscriptionNotFound", ErrSubscriptionNotFound, 404, "Marketplace.SubscriptionNotFound"},
		{"SanitizeUnavailable", ErrSanitizeUnavailable, 503, "Marketplace.SanitizeUnavailable"},
		{"SanitizeConfirmationMismatch", ErrSanitizeConfirmationMismatch, 422, "Marketplace.SanitizeConfirmationMismatch"},
		{"SkillBodyEmpty", ErrSkillBodyEmpty, 400, "Marketplace.SkillBodyEmpty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NotNil(t, tc.e, "errno %s not defined", tc.name)
			assert.Equal(t, tc.http, tc.e.HTTP, "%s: HTTP mismatch", tc.name)
			assert.Equal(t, tc.code, tc.e.Code, "%s: Code mismatch", tc.name)
			assert.NotEmpty(t, tc.e.Message, "%s: Message must be non-empty", tc.name)
		})
	}
}

// TestMarketplaceErrnos_UniqueCodes guards against accidental duplicate Code
// strings within the marketplace block (or collision with other packages).
func TestMarketplaceErrnos_UniqueCodes(t *testing.T) {
	all := []*Errno{
		ErrChildAccountCannotAccessMarketplace,
		ErrSkillNotOwned,
		ErrSkillAlreadyPublished,
		ErrSelfSubscribeForbidden,
		ErrAlreadySubscribed,
		ErrMarketplaceNotFound,
		ErrSubscriptionNotFound,
		ErrSanitizeUnavailable,
		ErrSanitizeConfirmationMismatch,
		ErrSkillBodyEmpty,
	}
	seen := map[string]string{}
	for _, e := range all {
		if prior, ok := seen[e.Code]; ok {
			t.Fatalf("duplicate errno Code %q (HTTP %d, Message %q) — already used by HTTP %d / Message %q",
				e.Code, e.HTTP, e.Message, all[0].HTTP, prior)
		}
		seen[e.Code] = e.Message
	}
	// Spot-check namespace.
	for _, e := range all {
		assert.True(t, len(e.Code) > len("Marketplace.") &&
			e.Code[:len("Marketplace.")] == "Marketplace.",
			"Code %q must start with 'Marketplace.' namespace", e.Code)
	}
}
