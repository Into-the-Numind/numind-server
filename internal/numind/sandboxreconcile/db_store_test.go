package sandboxreconcile

import (
	"context"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

func TestDBStoreOnlyReconcilesBrokerBoundRows(t *testing.T) {
	db := newReconcileTestDB(t)
	reservationID := uint64(3)
	otherReservationID := uint64(7)
	startedAt := time.Now().Add(-time.Minute)
	if err := db.Exec(`
INSERT INTO agent_sandbox_session
  (id, user_id, agent_run_id, container_id, image_tag, status, mem_limit_mb,
   cpu_quota, started_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 10, 2, "lease-1", "sandbox-skill:skills-v1.5.3",
		"running", 512, 1, startedAt, startedAt, startedAt,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
INSERT INTO agent_sandbox_session
  (id, user_id, agent_run_id, container_id, image_tag, status, mem_limit_mb,
   cpu_quota, started_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		9, 11, 8, "other", "sandbox-skill:skills-v1.5.3",
		"running", 512, 1, startedAt, startedAt, startedAt,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
INSERT INTO agent_run
  (id, user_id, session_id, status, state_reason, messages, reservation_id,
   started_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		2, 10, "session-1", "running", "", datatypes.JSON([]byte("[]")),
		reservationID, startedAt, startedAt, startedAt,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
INSERT INTO agent_run
  (id, user_id, session_id, status, state_reason, messages, reservation_id,
   started_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		8, 11, "session-2", "running", "", datatypes.JSON([]byte("[]")),
		otherReservationID, startedAt, startedAt, startedAt,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
INSERT INTO credit_reservation
  (id, user_id, reference_type, reference_id, operation, reserved_credits,
   status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		3, 10, "agent_run", "2", "agent_run", 100, "reserved",
		startedAt, startedAt,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
INSERT INTO credit_reservation
  (id, user_id, reference_type, reference_id, operation, reserved_credits,
   status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		7, 11, "agent_run", "8", "agent_run", 100, "reserved",
		startedAt, startedAt,
	).Error; err != nil {
		t.Fatal(err)
	}

	finalizer := &recordingFinalizer{}
	store, err := NewDBStore(db, finalizer)
	if err != nil {
		t.Fatal(err)
	}
	leases := []LeaseRef{
		{LeaseID: "lease-1", AgentRunID: 2, SandboxSessionID: 1},
	}
	sessions, err := store.ListPendingSessions(context.Background(), leases, 10)
	if err != nil || len(sessions) != 1 || sessions[0].ID != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	runs, err := store.ListPendingRuns(context.Background(), leases, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != 2 ||
		runs[0].ReservationID != reservationID {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
	reservations, err := store.ListPendingReservations(context.Background(), runs, 10)
	if err != nil || len(reservations) != 1 || reservations[0].ID != 3 {
		t.Fatalf("reservations=%#v err=%v", reservations, err)
	}
	if err := store.ReconcileSession(context.Background(), sessions[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileRun(context.Background(), runs[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileReservation(context.Background(), reservations[0]); err != nil {
		t.Fatal(err)
	}

	var session model.AgentSandboxSession
	if err := db.First(&session, 1).Error; err != nil {
		t.Fatal(err)
	}
	if session.Status != recoveredSessionStatus || session.EndedAt == nil {
		t.Fatalf("session = %#v", session)
	}
	var otherSession model.AgentSandboxSession
	if err := db.First(&otherSession, 9).Error; err != nil {
		t.Fatal(err)
	}
	if otherSession.Status != "running" {
		t.Fatalf("other session changed: %#v", otherSession)
	}

	var run model.AgentRun
	if err := db.First(&run, 2).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != recoveredRunStatus ||
		run.StateReason != recoveredRunStateReason ||
		run.EndedAt == nil {
		t.Fatalf("run = %#v", run)
	}
	var otherRun model.AgentRun
	if err := db.First(&otherRun, 8).Error; err != nil {
		t.Fatal(err)
	}
	if otherRun.Status != "running" {
		t.Fatalf("other run changed: %#v", otherRun)
	}
	if len(finalizer.refunds) != 1 || finalizer.refunds[0] != 3 {
		t.Fatalf("refunds=%v", finalizer.refunds)
	}
}

func newReconcileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/reconcile.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE agent_sandbox_session (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL,
			agent_run_id INTEGER,
			container_id TEXT NOT NULL,
			image_tag TEXT NOT NULL,
			status TEXT NOT NULL,
			mem_limit_mb INTEGER NOT NULL,
			cpu_quota REAL NOT NULL,
			exit_code INTEGER,
			error_msg TEXT,
			started_at DATETIME NOT NULL,
			ended_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE agent_run (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL,
			session_id TEXT,
			status TEXT NOT NULL,
			state_reason TEXT,
			terminal_metadata JSON,
			messages JSON NOT NULL,
			reservation_id INTEGER,
			started_at DATETIME NOT NULL,
			ended_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE credit_reservation (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL,
			reference_type TEXT NOT NULL,
			reference_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			reserved_credits INTEGER NOT NULL,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

type recordingFinalizer struct {
	refunds []uint64
}

func (f *recordingFinalizer) Refund(
	_ context.Context,
	reservationID uint64,
	reason string,
) error {
	if reason != recoveredReservationReason {
		return nil
	}
	f.refunds = append(f.refunds, reservationID)
	return nil
}
