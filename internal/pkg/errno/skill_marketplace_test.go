package errno

import (
	"fmt"
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
	// Track full prior entry so duplicate diagnostics print the actual conflicting
	// HTTP code instead of all[0]'s HTTP (T7 P2 from code-quality reviewer).
	seen := map[string]*Errno{}
	for _, e := range all {
		if prior, ok := seen[e.Code]; ok {
			t.Fatalf("duplicate errno Code %q — current entry HTTP %d / Message %q — already used by HTTP %d / Message %q",
				e.Code, e.HTTP, e.Message, prior.HTTP, prior.Message)
		}
		seen[e.Code] = e
	}
	// Spot-check namespace.
	for _, e := range all {
		assert.True(t, len(e.Code) > len("Marketplace.") &&
			e.Code[:len("Marketplace.")] == "Marketplace.",
			"Code %q must start with 'Marketplace.' namespace", e.Code)
	}
}

// TestDecode_UnwrapsErrnoFromWrappedError verifies the project-wide Decode
// improvement (T7 P1 fix): fmt.Errorf("...: %w", errno.ErrXxx) chains must
// surface the *Errno's HTTP/Code/Message — previously the type-switch missed
// wrap targets and fell back to 500.
//
// Regression protection for the SanitizeUnavailable production path which
// wraps via biz/marketplace/sanitize.go's Sanitize() helper.
func TestDecode_UnwrapsErrnoFromWrappedError(t *testing.T) {
	wrapped := fmt.Errorf("sanitize call: %w", ErrSanitizeUnavailable)
	http, code, _ := Decode(wrapped)
	assert.Equal(t, ErrSanitizeUnavailable.HTTP, http, "wrapped *Errno HTTP must propagate")
	assert.Equal(t, ErrSanitizeUnavailable.Code, code, "wrapped *Errno Code must propagate")

	// Nested wrap (two levels deep) — errors.As walks the chain.
	nested := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrMarketplaceNotFound))
	http2, code2, _ := Decode(nested)
	assert.Equal(t, ErrMarketplaceNotFound.HTTP, http2, "nested wrap *Errno HTTP must propagate")
	assert.Equal(t, ErrMarketplaceNotFound.Code, code2, "nested wrap *Errno Code must propagate")
}
