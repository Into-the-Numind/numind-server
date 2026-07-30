package sandboxreconcile

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

var (
	ErrInvalidConfig = errors.New("invalid sandbox reconcile config")
	ErrRunFailed     = errors.New("sandbox reconcile run failed")
)

type Config struct {
	Apply  bool
	Limit  int
	Broker BrokerClient
	Store  Store
	Logger Logger
}

type BrokerClient interface {
	ListRecoveryPendingLeases(context.Context, int) ([]LeaseRef, error)
	MarkLeaseReconciled(context.Context, string) error
}

type Store interface {
	ListPendingSessions(context.Context, []LeaseRef, int) ([]SessionRef, error)
	ListPendingRuns(context.Context, []LeaseRef, int) ([]RunRef, error)
	ListPendingReservations(context.Context, []RunRef, int) ([]ReservationRef, error)
	ReconcileSession(context.Context, SessionRef) error
	ReconcileRun(context.Context, RunRef) error
	ReconcileReservation(context.Context, ReservationRef) error
}

type Logger interface {
	Printf(string, ...any)
}

type LeaseRef struct {
	LeaseID           string
	AgentRunID        uint64
	SandboxSessionID  uint64
	State             string
	TerminationReason string
}

type SessionRef struct {
	ID      uint64
	LeaseID string
}

type RunRef struct {
	ID            uint64
	LeaseID       string
	ReservationID uint64
}

type ReservationRef struct {
	ID         uint64
	AgentRunID uint64
	Reason     string
}

type Result struct {
	Scanned    int
	WouldApply int
	Applied    int
	Skipped    int
	Failed     int
}

type Service struct {
	cfg Config
}

func New(cfg Config) (*Service, error) {
	limit := cfg.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit <= 0 || limit > MaxLimit || cfg.Broker == nil || cfg.Store == nil {
		return nil, ErrInvalidConfig
	}
	cfg.Limit = limit
	if cfg.Logger == nil {
		cfg.Logger = discardLogger{}
	}
	return &Service{cfg: cfg}, nil
}

func (s *Service) Run(ctx context.Context) (Result, error) {
	if s == nil || ctx == nil {
		return Result{}, ErrInvalidConfig
	}
	leases, err := s.cfg.Broker.ListRecoveryPendingLeases(ctx, s.cfg.Limit)
	if err != nil {
		return Result{}, fmt.Errorf("%w: broker unavailable", err)
	}
	if len(leases) == 0 {
		return Result{}, nil
	}
	sessions, err := s.cfg.Store.ListPendingSessions(ctx, leases, s.cfg.Limit)
	if err != nil {
		return Result{}, fmt.Errorf("%w: store sessions unavailable", err)
	}
	runs, err := s.cfg.Store.ListPendingRuns(ctx, leases, s.cfg.Limit)
	if err != nil {
		return Result{}, fmt.Errorf("%w: store runs unavailable", err)
	}
	reservations, err := s.cfg.Store.ListPendingReservations(ctx, runs, s.cfg.Limit)
	if err != nil {
		return Result{}, fmt.Errorf("%w: store reservations unavailable", err)
	}

	result := Result{
		Scanned: len(leases) + len(sessions) + len(runs) + len(reservations),
	}
	if !s.cfg.Apply {
		result.WouldApply = result.Scanned
		s.cfg.Logger.Printf(
			"sandbox reconcile dry-run scanned=%d would_apply=%d",
			result.Scanned,
			result.WouldApply,
		)
		return result, nil
	}

	var runErr error
	for _, session := range sessions {
		result.apply(s.cfg.Store.ReconcileSession(ctx, session), &runErr)
	}
	for _, run := range runs {
		result.apply(s.cfg.Store.ReconcileRun(ctx, run), &runErr)
	}
	for _, reservation := range reservations {
		result.apply(s.cfg.Store.ReconcileReservation(ctx, reservation), &runErr)
	}
	if runErr != nil {
		return result, errors.Join(ErrRunFailed, runErr)
	}
	for _, lease := range leases {
		if lease.LeaseID == "" {
			result.Skipped++
			continue
		}
		result.apply(
			s.cfg.Broker.MarkLeaseReconciled(ctx, lease.LeaseID),
			&runErr,
		)
	}
	if runErr != nil {
		return result, errors.Join(ErrRunFailed, runErr)
	}
	s.cfg.Logger.Printf(
		"sandbox reconcile apply scanned=%d applied=%d skipped=%d",
		result.Scanned,
		result.Applied,
		result.Skipped,
	)
	return result, nil
}

func (r *Result) apply(err error, runErr *error) {
	if err != nil {
		r.Failed++
		*runErr = errors.Join(*runErr, err)
		return
	}
	r.Applied++
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

type writerLogger struct {
	writer io.Writer
}

func NewWriterLogger(writer io.Writer) Logger {
	if writer == nil {
		return discardLogger{}
	}
	return writerLogger{writer: writer}
}

func (l writerLogger) Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(l.writer, format+"\n", args...)
}
