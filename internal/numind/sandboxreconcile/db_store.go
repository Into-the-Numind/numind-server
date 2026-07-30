package sandboxreconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

const (
	recoveredRunStatus         = "terminated"
	recoveredRunStateReason    = "model_error"
	recoveredSessionStatus     = "failed"
	recoveredReservationReason = "op_failed"
)

// ReservationFinalizer is the narrow production billing capability required by
// this recovery tool. Production wires the regular credit service so refunds go
// through the audited three-pool membership path instead of direct balance SQL.
type ReservationFinalizer interface {
	Refund(context.Context, uint64, string) error
}

type DBStore struct {
	db        *gorm.DB
	finalizer ReservationFinalizer
	now       func() time.Time
}

func NewDBStore(db *gorm.DB, finalizer ReservationFinalizer) (*DBStore, error) {
	if db == nil || finalizer == nil {
		return nil, ErrInvalidConfig
	}
	return &DBStore{
		db:        db,
		finalizer: finalizer,
		now:       time.Now,
	}, nil
}

func (s *DBStore) ListPendingSessions(
	ctx context.Context,
	leases []LeaseRef,
	limit int,
) ([]SessionRef, error) {
	ids := sandboxSessionIDs(leases)
	if len(ids) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > MaxLimit {
		return nil, ErrInvalidConfig
	}
	var rows []model.AgentSandboxSession
	if err := s.db.WithContext(ctx).
		Where("id IN ? AND status = ?", ids, "running").
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending sandbox sessions: %w", err)
	}
	refs := make([]SessionRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, SessionRef{
			ID:      row.ID,
			LeaseID: leaseIDForSession(leases, row.ID),
		})
	}
	return refs, nil
}

func (s *DBStore) ListPendingRuns(
	ctx context.Context,
	leases []LeaseRef,
	limit int,
) ([]RunRef, error) {
	ids := agentRunIDs(leases)
	if len(ids) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > MaxLimit {
		return nil, ErrInvalidConfig
	}
	var rows []model.AgentRun
	if err := s.db.WithContext(ctx).
		Where("id IN ?", ids).
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending agent runs: %w", err)
	}
	refs := make([]RunRef, 0, len(rows))
	for _, row := range rows {
		var reservationID uint64
		if row.ReservationID != nil {
			reservationID = *row.ReservationID
		}
		refs = append(refs, RunRef{
			ID:            row.ID,
			LeaseID:       leaseIDForRun(leases, row.ID),
			ReservationID: reservationID,
		})
	}
	return refs, nil
}

func (s *DBStore) ListPendingReservations(
	ctx context.Context,
	runs []RunRef,
	limit int,
) ([]ReservationRef, error) {
	ids := reservationIDs(runs)
	if len(ids) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > MaxLimit {
		return nil, ErrInvalidConfig
	}
	var rows []model.CreditReservation
	if err := s.db.WithContext(ctx).
		Where("id IN ? AND status = ?", ids, "reserved").
		Order("id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending reservations: %w", err)
	}
	refs := make([]ReservationRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, ReservationRef{
			ID:         row.ID,
			AgentRunID: runIDForReservation(runs, row.ID),
			Reason:     recoveredReservationReason,
		})
	}
	return refs, nil
}

func (s *DBStore) ReconcileSession(ctx context.Context, ref SessionRef) error {
	if ref.ID == 0 {
		return nil
	}
	endedAt := s.now()
	result := s.db.WithContext(ctx).
		Model(&model.AgentSandboxSession{}).
		Where("id = ? AND status = ?", ref.ID, "running").
		Updates(map[string]any{
			"status":    recoveredSessionStatus,
			"error_msg": "sandbox recovery reconciled",
			"ended_at":  endedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("reconcile sandbox session %d: %w", ref.ID, result.Error)
	}
	return nil
}

func (s *DBStore) ReconcileRun(ctx context.Context, ref RunRef) error {
	if ref.ID == 0 {
		return nil
	}
	endedAt := s.now()
	result := s.db.WithContext(ctx).
		Model(&model.AgentRun{}).
		Where("id = ? AND status = ?", ref.ID, "running").
		Updates(map[string]any{
			"status":       recoveredRunStatus,
			"state_reason": recoveredRunStateReason,
			"ended_at":     endedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("reconcile agent run %d: %w", ref.ID, result.Error)
	}
	return nil
}

func (s *DBStore) ReconcileReservation(
	ctx context.Context,
	ref ReservationRef,
) error {
	if ref.ID == 0 {
		return nil
	}
	reason := ref.Reason
	if reason == "" {
		reason = recoveredReservationReason
	}
	if err := s.finalizer.Refund(ctx, ref.ID, reason); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("refund reservation %d: %w", ref.ID, err)
	}
	return nil
}

func sandboxSessionIDs(leases []LeaseRef) []uint64 {
	seen := make(map[uint64]struct{}, len(leases))
	out := make([]uint64, 0, len(leases))
	for _, lease := range leases {
		if lease.SandboxSessionID == 0 {
			continue
		}
		if _, ok := seen[lease.SandboxSessionID]; ok {
			continue
		}
		seen[lease.SandboxSessionID] = struct{}{}
		out = append(out, lease.SandboxSessionID)
	}
	return out
}

func agentRunIDs(leases []LeaseRef) []uint64 {
	seen := make(map[uint64]struct{}, len(leases))
	out := make([]uint64, 0, len(leases))
	for _, lease := range leases {
		if lease.AgentRunID == 0 {
			continue
		}
		if _, ok := seen[lease.AgentRunID]; ok {
			continue
		}
		seen[lease.AgentRunID] = struct{}{}
		out = append(out, lease.AgentRunID)
	}
	return out
}

func reservationIDs(runs []RunRef) []uint64 {
	seen := make(map[uint64]struct{}, len(runs))
	out := make([]uint64, 0, len(runs))
	for _, run := range runs {
		if run.ReservationID == 0 {
			continue
		}
		if _, ok := seen[run.ReservationID]; ok {
			continue
		}
		seen[run.ReservationID] = struct{}{}
		out = append(out, run.ReservationID)
	}
	return out
}

func leaseIDForSession(leases []LeaseRef, sessionID uint64) string {
	for _, lease := range leases {
		if lease.SandboxSessionID == sessionID {
			return lease.LeaseID
		}
	}
	return ""
}

func leaseIDForRun(leases []LeaseRef, runID uint64) string {
	for _, lease := range leases {
		if lease.AgentRunID == runID {
			return lease.LeaseID
		}
	}
	return ""
}

func runIDForReservation(runs []RunRef, reservationID uint64) uint64 {
	for _, run := range runs {
		if run.ReservationID == reservationID {
			return run.ID
		}
	}
	return 0
}
