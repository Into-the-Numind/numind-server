package retrieve

import (
	"os"
	"testing"

	aiservice "numind-server/internal/pkg/aiservice"
)

// TestMain initialises a minimal aiservice singleton so that tests which
// exercise code paths reaching aiservice.Rerank() do not panic on Default().
// The gateway has no registry and no providers, so AI calls return an error
// rather than making real network requests — which is acceptable for unit
// tests that only exercise the retrieve plumbing (scope/queries/dedup) and the
// rerank fallback path (not the real rerank LLM result).
func TestMain(m *testing.M) {
	gw := aiservice.Build(aiservice.Deps{}) // no DB, no providers; AI calls return error, not panic
	aiservice.SetDefault(gw)
	os.Exit(m.Run())
}
