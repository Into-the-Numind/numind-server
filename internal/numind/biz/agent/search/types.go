// Package search provides Chinese full-text search over agent_run messages.
//
// Backend: MySQL 8 FULLTEXT INDEX with ngram parser (n=2). Equivalent to SQLite
// FTS5; MySQL is the project's primary DB.
//
// Architecture:
//   - agent_message_search table (one row per message, derived data)
//   - Async indexing on AgentRunStore.WriteTurn — diff by uuid; failure-tolerant
//   - HTTP endpoint GET /v1/agent-runs/search returns hits + snippet (<mark>...</mark>)
//   - User isolation enforced at SQL WHERE level — never serve cross-user hits
//
// D9 decision: stick with MySQL FULLTEXT until search latency / quality demands
// Elasticsearch (V2 scope, out of band).
//
// Spec: docs/specs/03-memory/task-05-fts5-search.md.
package search

import (
	"context"
	"time"

	"numind-server/internal/pkg/model"
)

// SearchOpts mirrors store.SearchOpts but lives in biz so callers don't depend
// on store package internals.
type SearchOpts struct {
	UserID    uint
	Query     string
	SessionID string
	DateFrom  *time.Time
	DateTo    *time.Time
	Limit     int // default 20, max 100
	Offset    int
}

// SearchResult is the public-facing search hit returned to HTTP / frontend.
// Snippet contains <mark>...</mark> spans — the field is HTML-safe because the
// underlying content is run through html.EscapeString before <mark> is inserted.
type SearchResult struct {
	MessageUUID string    `json:"message_uuid"`
	AgentRunID  uint64    `json:"agent_run_id"`
	SessionID   string    `json:"session_id"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	Snippet     string    `json:"snippet"`
	Score       float64   `json:"score"`
	CreatedAt   time.Time `json:"created_at"`
}

// Service is the search biz interface (the HTTP layer + the WriteTurn hook
// both depend on this).
type Service interface {
	// Search executes a FULLTEXT query with user isolation + snippet highlighting.
	Search(ctx context.Context, opts SearchOpts) ([]SearchResult, int64, error)
	// BulkInsert writes a batch of search rows; failure-tolerant (caller logs warn).
	BulkInsert(ctx context.Context, rows []model.AgentMessageSearch) error
	// BackfillFromAgentRun re-extracts and inserts search rows for one agent_run.
	// Used by backfill CLI on initial deploy + by repair routines.
	BackfillFromAgentRun(ctx context.Context, runID uint64) error
	// IndexAgentRun is the WriteTurn-adjacent hook called from runner.go. It
	// diffs against already-indexed UUIDs and inserts only new rows.
	// Failure-tolerant: errors are logged at warn level and swallowed.
	IndexAgentRun(ctx context.Context, run model.AgentRun)
}
