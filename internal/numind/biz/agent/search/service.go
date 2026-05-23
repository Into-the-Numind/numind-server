package search

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// service is the concrete Service implementation. Constructed with NewService.
type service struct {
	searchStore store.IAgentMessageSearchStore
	runStore    store.IAgentRunStore
}

// NewService wires the search biz around the two store interfaces it needs.
//
// runStore is used by BackfillFromAgentRun to read the source AgentRun. Pass
// nil if the caller never invokes BackfillFromAgentRun (e.g. minimal HTTP-only
// path); the method then returns an error indicating dependency missing.
func NewService(searchStore store.IAgentMessageSearchStore, runStore store.IAgentRunStore) Service {
	return &service{searchStore: searchStore, runStore: runStore}
}

// Search runs the FULLTEXT query and wraps each hit with an HTML-safe snippet.
//
// User isolation: SearchOpts.UserID is forwarded to store.Search which hard
// filters WHERE user_id = ?. Callers MUST pass the authenticated user ID — the
// HTTP controller pulls it from middleware.GetCurrentUser.
func (s *service) Search(ctx context.Context, opts SearchOpts) ([]SearchResult, int64, error) {
	if opts.UserID == 0 {
		return nil, 0, fmt.Errorf("search.Service.Search: UserID required")
	}
	storeOpts := store.SearchOpts{
		UserID:    opts.UserID,
		Query:     opts.Query,
		SessionID: opts.SessionID,
		DateFrom:  opts.DateFrom,
		DateTo:    opts.DateTo,
		Limit:     opts.Limit,
		Offset:    opts.Offset,
	}
	hits, total, err := s.searchStore.Search(ctx, storeOpts)
	if err != nil {
		return nil, 0, fmt.Errorf("search.Service.Search store: %w", err)
	}
	out := make([]SearchResult, 0, len(hits))
	for _, h := range hits {
		out = append(out, SearchResult{
			MessageUUID: h.MessageUUID,
			AgentRunID:  h.AgentRunID,
			SessionID:   h.SessionID,
			Role:        h.Role,
			Content:     h.Content,
			Snippet:     makeSnippet(h.Content, opts.Query),
			Score:       h.Score,
			CreatedAt:   h.CreatedAt,
		})
	}
	return out, total, nil
}

// BulkInsert delegates to store; errors are wrapped but caller is expected to
// handle failure-tolerance (log warn + continue) since search rows are derived.
func (s *service) BulkInsert(ctx context.Context, rows []model.AgentMessageSearch) error {
	if err := s.searchStore.BulkInsert(ctx, rows); err != nil {
		return fmt.Errorf("search.Service.BulkInsert: %w", err)
	}
	return nil
}

// BackfillFromAgentRun re-extracts and writes search rows for one agent_run.
// Diff is applied: only previously-unseen message UUIDs are inserted, so the
// method is safe to call repeatedly (idempotent).
func (s *service) BackfillFromAgentRun(ctx context.Context, runID uint64) error {
	if s.runStore == nil {
		return fmt.Errorf("search.Service.BackfillFromAgentRun: runStore not configured")
	}
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("search.Service.BackfillFromAgentRun get(runID=%d): %w", runID, err)
	}
	rows := extractSearchRows(*run)
	if len(rows) == 0 {
		return nil
	}
	known, err := s.searchStore.GetMessageUUIDsByRun(ctx, runID)
	if err != nil {
		// Log + continue: GetMessageUUIDsByRun failure means we cannot dedupe
		// safely. Skip insert to avoid duplicates.
		log.Warnw("search.BackfillFromAgentRun GetMessageUUIDsByRun failed; skipping",
			"agent_run_id", runID, "error", err)
		return nil
	}
	newRows := filterByNewUUID(rows, known)
	if len(newRows) == 0 {
		return nil
	}
	if err := s.searchStore.BulkInsert(ctx, newRows); err != nil {
		log.Warnw("search.BackfillFromAgentRun BulkInsert failed",
			"agent_run_id", runID, "rows", len(newRows), "error", err)
		// Do not surface to caller — derived data, best-effort.
		return nil
	}
	return nil
}

// IndexAgentRun is the hook called by AgentRunStore.WriteTurn.
//
// Failure-tolerant: every error is logged at warn level and swallowed. Search
// rows are derived data — if indexing fails, the agent run still completes.
// A subsequent BackfillFromAgentRun (or the backfill CLI) can recover.
//
// Diff strategy: GetMessageUUIDsByRun returns already-indexed UUIDs; only new
// UUIDs are inserted. This makes the hook safe to call on every WriteTurn even
// though WriteTurn rewrites the full messages array.
func (s *service) IndexAgentRun(ctx context.Context, run model.AgentRun) {
	rows := extractSearchRows(run)
	if len(rows) == 0 {
		return
	}
	known, err := s.searchStore.GetMessageUUIDsByRun(ctx, run.ID)
	if err != nil {
		log.Warnw("search.IndexAgentRun GetMessageUUIDsByRun failed",
			"agent_run_id", run.ID, "error", err)
		return
	}
	newRows := filterByNewUUID(rows, known)
	if len(newRows) == 0 {
		return
	}
	if err := s.searchStore.BulkInsert(ctx, newRows); err != nil {
		log.Warnw("search.IndexAgentRun BulkInsert failed",
			"agent_run_id", run.ID, "user_id", run.UserID, "rows", len(newRows), "error", err)
	}
}
