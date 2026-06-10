package credit

import (
	"errors"
	"testing"
)

// decideFreeModel is pure (no I/O) so the whole truth table is covered here
// without a DB. Integration of pricing+membership into the precheck entry points
// is covered by free_model_member_test.go (package credit_test).
func TestDecideFreeModel(t *testing.T) {
	dbErr := errors.New("boom")
	tests := []struct {
		name      string
		isFree    bool
		freeErr   error
		isMember  bool
		memberErr error
		want      freeModelAction
	}{
		{"not free → passthrough", false, nil, true, nil, freeModelPassThrough},
		{"free-lookup error → passthrough", true, dbErr, true, nil, freeModelPassThrough},
		{"free + member → skip", true, nil, true, nil, freeModelSkip},
		{"free + non-member → block", true, nil, false, nil, freeModelBlock},
		{"free + member-lookup error (member=false) → passthrough", true, nil, false, dbErr, freeModelPassThrough},
		{"free + member-lookup error (member=true) → passthrough", true, nil, true, dbErr, freeModelPassThrough},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideFreeModel(tc.isFree, tc.freeErr, tc.isMember, tc.memberErr); got != tc.want {
				t.Fatalf("decideFreeModel(%v,%v,%v,%v) = %d, want %d",
					tc.isFree, tc.freeErr, tc.isMember, tc.memberErr, got, tc.want)
			}
		})
	}
}
