package sandboxreconcile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestServiceDryRunDoesNotMutate(t *testing.T) {
	broker := &fakeBroker{
		leases: []LeaseRef{
			{LeaseID: "lease-1", AgentRunID: 2, SandboxSessionID: 1},
		},
	}
	store := &fakeStore{
		sessions:     []SessionRef{{ID: 1, LeaseID: "lease-1"}},
		runs:         []RunRef{{ID: 2, LeaseID: "lease-1", ReservationID: 3}},
		reservations: []ReservationRef{{ID: 3, AgentRunID: 2}},
	}
	service, err := New(Config{Broker: broker, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 4 || result.WouldApply != 4 || result.Applied != 0 {
		t.Fatalf("result = %#v", result)
	}
	if store.mutations != 0 || broker.mutations != 0 {
		t.Fatalf("dry-run mutated store=%d broker=%d", store.mutations, broker.mutations)
	}
}

func TestServiceApplyDoubleRunIsIdempotent(t *testing.T) {
	broker := &fakeBroker{
		leases: []LeaseRef{
			{LeaseID: "lease-1", AgentRunID: 2, SandboxSessionID: 1},
		},
	}
	store := &fakeStore{
		sessions:     []SessionRef{{ID: 1, LeaseID: "lease-1"}},
		runs:         []RunRef{{ID: 2, LeaseID: "lease-1", ReservationID: 3}},
		reservations: []ReservationRef{{ID: 3, AgentRunID: 2}},
	}
	service, err := New(Config{Apply: true, Broker: broker, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Scanned != 4 || first.Applied != 4 || second.Scanned != 0 ||
		second.Applied != 0 {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if store.mutations != 3 || broker.mutations != 1 {
		t.Fatalf("mutations store=%d broker=%d", store.mutations, broker.mutations)
	}
}

func TestServiceOnlyTouchesBrokerNamedRecords(t *testing.T) {
	broker := &fakeBroker{
		leases: []LeaseRef{
			{LeaseID: "lease-1", AgentRunID: 2, SandboxSessionID: 1},
		},
	}
	store := &fakeStore{
		sessions: []SessionRef{
			{ID: 1, LeaseID: "lease-1"},
			{ID: 9, LeaseID: "other-lease"},
		},
		runs: []RunRef{
			{ID: 2, LeaseID: "lease-1", ReservationID: 3},
			{ID: 8, LeaseID: "other-lease", ReservationID: 7},
		},
		reservations: []ReservationRef{
			{ID: 3, AgentRunID: 2},
			{ID: 7, AgentRunID: 8},
		},
	}
	service, err := New(Config{Apply: true, Broker: broker, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied != 4 {
		t.Fatalf("result = %#v", result)
	}
	if len(store.sessions) != 1 || store.sessions[0].ID != 9 ||
		len(store.runs) != 1 || store.runs[0].ID != 8 ||
		len(store.reservations) != 1 || store.reservations[0].ID != 7 {
		t.Fatalf(
			"unexpected leftover sessions=%v runs=%v reservations=%v",
			store.sessions,
			store.runs,
			store.reservations,
		)
	}
}

func TestServiceDoesNotMarkBrokerWhenStoreReconcileFails(t *testing.T) {
	broker := &fakeBroker{
		leases: []LeaseRef{
			{LeaseID: "lease-1", AgentRunID: 2, SandboxSessionID: 1},
		},
	}
	store := &fakeStore{
		sessions:     []SessionRef{{ID: 1, LeaseID: "lease-1"}},
		runs:         []RunRef{{ID: 2, LeaseID: "lease-1", ReservationID: 3}},
		reservations: []ReservationRef{{ID: 3, AgentRunID: 2}},
		reconcileErr: errors.New("reservation refund failed"),
	}
	service, err := New(Config{Apply: true, Broker: broker, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background())
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("err=%v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("result=%#v", result)
	}
	if broker.mutations != 0 || len(broker.leases) != 1 {
		t.Fatalf("broker marked despite store failure mutations=%d leases=%v", broker.mutations, broker.leases)
	}
}

func TestServiceRejectsMissingBrokerOrStore(t *testing.T) {
	if _, err := New(Config{Store: &fakeStore{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing broker err = %v", err)
	}
	if _, err := New(Config{Broker: &fakeBroker{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing store err = %v", err)
	}
	service, err := New(Config{
		Broker: &fakeBroker{err: errors.New("socket unavailable")},
		Store:  &fakeStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(context.Background()); err == nil ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf("broker error = %v", err)
	}
	service, err = New(Config{
		Broker: &fakeBroker{
			leases: []LeaseRef{
				{LeaseID: "lease-1", AgentRunID: 2, SandboxSessionID: 1},
			},
		},
		Store: &fakeStore{err: errors.New("db unavailable")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(context.Background()); err == nil ||
		strings.Contains(err.Error(), "password") {
		t.Fatalf("store error = %v", err)
	}
}

func TestServiceDoesNotContainDirectBalanceUpdates(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(file), "service.go"))
	if err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(string(source))
	for _, forbidden := range []string{
		"UPDATE USER",
		"UPDATE CREDIT_ACCOUNT",
		"UPDATE CREDIT_CYCLE",
		"UPDATE TRIAL_GRANT",
		"UPDATE USER_BOOSTER_BALANCE",
	} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("service contains direct balance SQL marker %q", forbidden)
		}
	}
}

type fakeBroker struct {
	leases    []LeaseRef
	err       error
	mutations int
}

func (b *fakeBroker) ListRecoveryPendingLeases(
	context.Context,
	int,
) ([]LeaseRef, error) {
	if b.err != nil {
		return nil, b.err
	}
	return append([]LeaseRef(nil), b.leases...), nil
}

func (b *fakeBroker) MarkLeaseReconciled(
	_ context.Context,
	leaseID string,
) error {
	if b.err != nil {
		return b.err
	}
	for index, lease := range b.leases {
		if lease.LeaseID == leaseID {
			b.leases = append(b.leases[:index], b.leases[index+1:]...)
			b.mutations++
			return nil
		}
	}
	return nil
}

type fakeStore struct {
	sessions     []SessionRef
	runs         []RunRef
	reservations []ReservationRef
	err          error
	reconcileErr error
	mutations    int
}

func (s *fakeStore) ListPendingSessions(
	_ context.Context,
	leases []LeaseRef,
	_ int,
) ([]SessionRef, error) {
	if s.err != nil {
		return nil, s.err
	}
	allowed := make(map[uint64]struct{})
	for _, lease := range leases {
		if lease.SandboxSessionID != 0 {
			allowed[lease.SandboxSessionID] = struct{}{}
		}
	}
	return append([]SessionRef(nil), s.filterSessions(allowed)...), nil
}

func (s *fakeStore) ListPendingRuns(
	_ context.Context,
	leases []LeaseRef,
	_ int,
) ([]RunRef, error) {
	if s.err != nil {
		return nil, s.err
	}
	allowed := make(map[uint64]struct{})
	for _, lease := range leases {
		if lease.AgentRunID != 0 {
			allowed[lease.AgentRunID] = struct{}{}
		}
	}
	return append([]RunRef(nil), s.filterRuns(allowed)...), nil
}

func (s *fakeStore) ListPendingReservations(
	_ context.Context,
	runs []RunRef,
	_ int,
) ([]ReservationRef, error) {
	if s.err != nil {
		return nil, s.err
	}
	allowed := make(map[uint64]struct{})
	for _, run := range runs {
		if run.ReservationID != 0 {
			allowed[run.ReservationID] = struct{}{}
		}
	}
	return append([]ReservationRef(nil), s.filterReservations(allowed)...), nil
}

func (s *fakeStore) ReconcileSession(_ context.Context, ref SessionRef) error {
	for index, session := range s.sessions {
		if session.ID == ref.ID {
			s.sessions = append(s.sessions[:index], s.sessions[index+1:]...)
			s.mutations++
			return nil
		}
	}
	return nil
}

func (s *fakeStore) filterSessions(allowed map[uint64]struct{}) []SessionRef {
	out := make([]SessionRef, 0, len(s.sessions))
	for _, session := range s.sessions {
		if _, ok := allowed[session.ID]; ok {
			out = append(out, session)
		}
	}
	return out
}

func (s *fakeStore) filterRuns(allowed map[uint64]struct{}) []RunRef {
	out := make([]RunRef, 0, len(s.runs))
	for _, run := range s.runs {
		if _, ok := allowed[run.ID]; ok {
			out = append(out, run)
		}
	}
	return out
}

func (s *fakeStore) filterReservations(allowed map[uint64]struct{}) []ReservationRef {
	out := make([]ReservationRef, 0, len(s.reservations))
	for _, reservation := range s.reservations {
		if _, ok := allowed[reservation.ID]; ok {
			out = append(out, reservation)
		}
	}
	return out
}

func (s *fakeStore) ReconcileRun(_ context.Context, ref RunRef) error {
	for index, run := range s.runs {
		if run.ID == ref.ID {
			s.runs = append(s.runs[:index], s.runs[index+1:]...)
			s.mutations++
			return nil
		}
	}
	return nil
}

func (s *fakeStore) ReconcileReservation(
	_ context.Context,
	ref ReservationRef,
) error {
	if s.reconcileErr != nil {
		return s.reconcileErr
	}
	for index, reservation := range s.reservations {
		if reservation.ID == ref.ID {
			s.reservations = append(
				s.reservations[:index],
				s.reservations[index+1:]...,
			)
			s.mutations++
			return nil
		}
	}
	return nil
}
