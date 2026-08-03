package agent

import (
	"context"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

const supervisedRunEventChanCap = 256

// StartPreparedStreamRun starts a prepared stream run on a detached supervised
// runner and publishes emitted events into the run event broker best-effort.
func (s *StudentRunService) StartPreparedStreamRun(prepared *PreparedStreamRun) bool {
	if s == nil || prepared == nil || prepared.RunID == 0 {
		return false
	}
	return s.startSupervisedRun(prepared.RunID, prepared.UserID, func(ctx context.Context, ch chan<- stream.Event) (*RunResult, error) {
		return s.RunStream(ctx, prepared.UserID, prepared.Request, prepared.RunID, ch)
	})
}

func (s *StudentRunService) startSupervisedRun(
	runID uint64,
	userID uint,
	run func(context.Context, chan<- stream.Event) (*RunResult, error),
) bool {
	if s == nil || s.streamExecutions == nil || runID == 0 || run == nil {
		return false
	}

	done := make(chan struct{})
	bgCtx := middleware.NewContextWithUserID(context.Background(), userID)
	runnerCtx, cancel := context.WithCancel(bgCtx)
	if !s.streamExecutions.Start(runID, cancel, done) {
		cancel()
		return false
	}

	go func() {
		defer close(done)
		defer s.streamExecutions.Finish(runID)
		defer cancel()

		events := make(chan stream.Event, supervisedRunEventChanCap)
		drained := make(chan struct{})
		observedTerminal := make(chan bool, 1)
		go func() {
			defer close(drained)
			observedTerminal <- s.publishDetachedRunEvents(bgCtx, runID, events)
		}()

		_, runErr := run(runnerCtx, events)
		close(events)
		<-drained
		terminalSeen := <-observedTerminal
		if runErr != nil && !terminalSeen {
			s.publishDetachedRunError(bgCtx, runID, runErr)
		}
	}()

	return true
}

func (s *StudentRunService) publishDetachedRunEvents(ctx context.Context, runID uint64, events <-chan stream.Event) bool {
	terminalSeen := false
	for ev := range events {
		if ev.Type == stream.EventTerminal || ev.Type == stream.EventError {
			terminalSeen = true
		}
		if _, err := s.PublishRunEvent(ctx, runID, ev); err != nil {
			log.Warnw("agent supervised event publish failed", "run_id", runID, "event_type", ev.Type, "error", err)
		}
	}
	return terminalSeen
}

func (s *StudentRunService) publishDetachedRunError(ctx context.Context, runID uint64, _ error) {
	ev, encodeErr := stream.Encode(stream.EventError, stream.ErrorPayload{
		Code:    "internal",
		Message: "Agent 运行中断，请稍后重试。",
	}, 0, runID, 0)
	if encodeErr != nil {
		return
	}
	_, _ = s.PublishRunEvent(ctx, runID, ev)
}
