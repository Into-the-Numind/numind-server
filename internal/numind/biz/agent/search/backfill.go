package search

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// BackfillResult records the outcome of a backfill run.
type BackfillResult struct {
	ScannedRuns  int
	InsertedRows int
	SkippedRuns  int
	Elapsed      time.Duration
}

// BackfillAll walks the agent_run table in id-cursor batches and indexes every
// message into agent_message_search. Idempotent: re-running only inserts new
// (uuid-diff) rows.
//
// batchSize defaults to 500 when <= 0.
//
// CLI entrypoint: cmd/agent-search-backfill/main.go.
func BackfillAll(ctx context.Context, db *gorm.DB, searchStore store.IAgentMessageSearchStore, batchSize int) (BackfillResult, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	if db == nil {
		return BackfillResult{}, fmt.Errorf("search.BackfillAll: db is nil")
	}
	if searchStore == nil {
		return BackfillResult{}, fmt.Errorf("search.BackfillAll: searchStore is nil")
	}

	start := time.Now()
	var lastID uint64
	var scanned, inserted, skipped int

	for {
		if err := ctx.Err(); err != nil {
			return BackfillResult{
				ScannedRuns: scanned, InsertedRows: inserted, SkippedRuns: skipped,
				Elapsed: time.Since(start),
			}, fmt.Errorf("search.BackfillAll: ctx cancelled: %w", err)
		}

		var runs []model.AgentRun
		if err := db.WithContext(ctx).
			Where("id > ?", lastID).
			Order("id ASC").
			Limit(batchSize).
			Find(&runs).Error; err != nil {
			return BackfillResult{}, fmt.Errorf("search.BackfillAll list runs (lastID=%d): %w", lastID, err)
		}
		if len(runs) == 0 {
			break
		}

		for _, run := range runs {
			scanned++
			rows := extractSearchRows(run)
			if len(rows) == 0 {
				skipped++
				lastID = run.ID
				continue
			}
			known, err := searchStore.GetMessageUUIDsByRun(ctx, run.ID)
			if err != nil {
				log.Warnw("search.BackfillAll GetMessageUUIDsByRun failed",
					"agent_run_id", run.ID, "error", err)
				skipped++
				lastID = run.ID
				continue
			}
			newRows := filterByNewUUID(rows, known)
			if len(newRows) == 0 {
				skipped++
				lastID = run.ID
				continue
			}
			if err := searchStore.BulkInsert(ctx, newRows); err != nil {
				log.Warnw("search.BackfillAll BulkInsert failed",
					"agent_run_id", run.ID, "rows", len(newRows), "error", err)
				// Continue scanning — failure of one row should not block the rest.
			} else {
				inserted += len(newRows)
			}
			lastID = run.ID
		}

		log.Infow("search.BackfillAll progress",
			"last_id", lastID, "scanned", scanned, "inserted", inserted, "skipped", skipped)
	}

	return BackfillResult{
		ScannedRuns: scanned, InsertedRows: inserted, SkippedRuns: skipped,
		Elapsed: time.Since(start),
	}, nil
}
