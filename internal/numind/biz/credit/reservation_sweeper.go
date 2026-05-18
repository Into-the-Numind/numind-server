package credit

import (
	"context"
	"errors"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"

	"gorm.io/gorm"
)

// ReservationSweeperConfig configures the periodic sweep of zombie reservations.
type ReservationSweeperConfig struct {
	Interval   time.Duration // how often to run; default 5min
	StaleAfter time.Duration // how old a reserved row must be before sweep; default 1h
	BatchSize  int           // max rows per sweep; default 100
}

// DefaultReservationSweeperConfig provides production-safe defaults.
func DefaultReservationSweeperConfig() ReservationSweeperConfig {
	return ReservationSweeperConfig{
		Interval:   5 * time.Minute,
		StaleAfter: 1 * time.Hour,
		BatchSize:  100,
	}
}

// ReservationSweeper periodically refunds credit_reservation rows that have
// been stuck in status='reserved' for too long. This protects against credit
// loss when a server crash kills the defer FinalizeReservation chain mid-LLM.
//
// Audit reference: docs/superpowers/specs/2026-05-18-credits-system-audit.md P0-2.
//
// Background: Reserve deducts from credit_cycle/trial_grant/user_booster_balance
// immediately and writes a credit_reservation row with status='reserved'. The
// defer FinalizeReservation is supposed to Reconcile or Refund on the exit
// path. If the server crashes (OOM, kill, deploy mid-stream) between Reserve
// and Finalize, the row stays status='reserved' forever and the deducted
// credits are permanently lost. The sweeper detects these zombies and calls
// Refund(reason="expired_by_cron"), which reuses the existing
// RefundCreditsTx audit trail.
type ReservationSweeper struct {
	ds        store.IStore
	creditSvc ICreditService
	cfg       ReservationSweeperConfig
}

// NewReservationSweeper constructs a sweeper. Pass DefaultReservationSweeperConfig()
// for production-safe defaults. Zero-valued config fields are filled with defaults.
func NewReservationSweeper(ds store.IStore, svc ICreditService, cfg ReservationSweeperConfig) *ReservationSweeper {
	if cfg.Interval == 0 {
		cfg.Interval = 5 * time.Minute
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = 1 * time.Hour
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	return &ReservationSweeper{ds: ds, creditSvc: svc, cfg: cfg}
}

// Run starts the sweep loop. It returns when ctx is cancelled. Intended to be
// called in a goroutine at server startup.
func (s *ReservationSweeper) Run(ctx context.Context) {
	log.Infow("ReservationSweeper started",
		"interval", s.cfg.Interval,
		"stale_after", s.cfg.StaleAfter,
		"batch_size", s.cfg.BatchSize)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Infow("ReservationSweeper stopping")
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

// runOnce performs a single sweep pass. Exported as a method for tests and for
// callers that want to drive sweeps manually (e.g. admin endpoints).
func (s *ReservationSweeper) runOnce(ctx context.Context) {
	threshold := time.Now().UTC().Add(-s.cfg.StaleAfter)

	// Query stuck reservations. IDs only — Refund() loads the full row itself
	// with FOR UPDATE so concurrent FinalizeReservation paths cannot conflict.
	var ids []uint64
	err := s.ds.DB().WithContext(ctx).
		Table("credit_reservation").
		Where("status = ? AND created_at < ?", "reserved", threshold).
		Limit(s.cfg.BatchSize).
		Pluck("id", &ids).Error
	if err != nil {
		log.Errorw("ReservationSweeper: query failed", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	log.Infow("ReservationSweeper: refunding zombie reservations", "count", len(ids))
	var refunded, alreadyDone, errored int
	for _, id := range ids {
		// Respect ctx cancellation between rows so shutdown is responsive.
		if ctx.Err() != nil {
			log.Infow("ReservationSweeper: ctx cancelled mid-batch",
				"processed", refunded+alreadyDone+errored,
				"total", len(ids))
			return
		}
		err := s.creditSvc.Refund(ctx, id, "expired_by_cron")
		if err == nil {
			refunded++
			continue
		}
		// Already-finalized or vanished reservations are not actionable — they
		// were completed normally between the SELECT and the FOR UPDATE inside
		// Refund. Count them separately and don't escalate.
		if errors.Is(err, ErrAlreadyFinalized) ||
			errors.Is(err, ErrReservationNotFound) ||
			errors.Is(err, gorm.ErrRecordNotFound) {
			alreadyDone++
			continue
		}
		log.Warnw("ReservationSweeper: refund failed", "reservation_id", id, "err", err)
		errored++
	}
	log.Infow("ReservationSweeper: pass done",
		"refunded", refunded,
		"already_done", alreadyDone,
		"errored", errored)
}
