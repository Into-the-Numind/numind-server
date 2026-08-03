package agent

import (
	"context"
	"fmt"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

const supervisedRunEventChanCap = 256
const supervisedRunFallbackErrorMessage = "Agent 运行中断，请稍后重试。"

type supervisedRunEventObservation struct {
	errorSeen    bool
	terminalSeen bool
	lastSeq      uint64
}

type supervisedRunAdmission struct {
	runID     uint64
	bgCtx     context.Context
	runnerCtx context.Context
	cancel    context.CancelFunc
	done      chan struct{}
}

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

// StartPreparedAnswerStream atomically admits an answer-stream resume, validates
// and persists the user answer synchronously, then resumes the streaming run on
// a detached supervised runner.
func (s *StudentRunService) StartPreparedAnswerStream(ctx context.Context, userID uint, runID uint64, req AnswerRequest) (bool, error) {
	admission, ok := s.admitSupervisedRun(runID, userID)
	if !ok {
		return false, nil
	}

	runReq, err := s.validateAndPersistAnswer(ctx, userID, runID, req)
	if err != nil {
		s.releaseSupervisedRunAdmission(admission)
		return false, err
	}
	s.startAdmittedSupervisedRun(admission, func(runCtx context.Context, ch chan<- stream.Event) (*RunResult, error) {
		go s.forwardNarration(runID)
		return s.runner.RunStream(runCtx, runReq, runID, ch)
	})
	return true, nil
}

func (s *StudentRunService) startSupervisedRun(
	runID uint64,
	userID uint,
	run func(context.Context, chan<- stream.Event) (*RunResult, error),
) bool {
	if s == nil || s.streamExecutions == nil || runID == 0 || run == nil {
		return false
	}

	admission, ok := s.admitSupervisedRun(runID, userID)
	if !ok {
		return false
	}
	s.startAdmittedSupervisedRun(admission, run)
	return true
}

func (s *StudentRunService) admitSupervisedRun(runID uint64, userID uint) (*supervisedRunAdmission, bool) {
	if s == nil || s.streamExecutions == nil || runID == 0 {
		return nil, false
	}

	done := make(chan struct{})
	bgCtx := middleware.NewContextWithUserID(context.Background(), userID)
	runnerCtx, cancel := context.WithCancel(bgCtx)
	if !s.streamExecutions.Start(runID, cancel, done) {
		cancel()
		return nil, false
	}

	return &supervisedRunAdmission{
		runID:     runID,
		bgCtx:     bgCtx,
		runnerCtx: runnerCtx,
		cancel:    cancel,
		done:      done,
	}, true
}

func (s *StudentRunService) releaseSupervisedRunAdmission(admission *supervisedRunAdmission) {
	if s == nil || admission == nil {
		return
	}
	if admission.cancel != nil {
		admission.cancel()
	}
	if s.streamExecutions != nil {
		s.streamExecutions.Finish(admission.runID)
	}
	if admission.done != nil {
		close(admission.done)
	}
}

func (s *StudentRunService) startAdmittedSupervisedRun(
	admission *supervisedRunAdmission,
	run func(context.Context, chan<- stream.Event) (*RunResult, error),
) {
	go func() {
		defer close(admission.done)
		defer s.streamExecutions.Finish(admission.runID)
		defer admission.cancel()

		events := make(chan stream.Event, supervisedRunEventChanCap)
		drained := make(chan struct{})
		observedEvents := make(chan supervisedRunEventObservation, 1)
		go func() {
			defer close(drained)
			observedEvents <- s.publishDetachedRunEvents(admission.bgCtx, admission.runID, events)
		}()

		_, runErr := runSupervisedStream(admission.runnerCtx, events, run)
		close(events)
		<-drained
		observation := <-observedEvents
		if runErr != nil && !observation.terminalSeen {
			s.publishDetachedRunFailure(admission.bgCtx, admission.runID, observation, runErr)
		}
	}()
}

func runSupervisedStream(
	ctx context.Context,
	events chan<- stream.Event,
	run func(context.Context, chan<- stream.Event) (*RunResult, error),
) (result *RunResult, runErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("agent supervised stream run panic: %v", recovered)
		}
	}()
	return run(ctx, events)
}

func (s *StudentRunService) publishDetachedRunEvents(
	ctx context.Context,
	runID uint64,
	events <-chan stream.Event,
) supervisedRunEventObservation {
	var observation supervisedRunEventObservation
	for ev := range events {
		observation.lastSeq = ev.Seq
		switch ev.Type {
		case stream.EventError:
			observation.errorSeen = true
		case stream.EventTerminal:
			observation.terminalSeen = true
		}
		if _, err := s.PublishRunEvent(ctx, runID, ev); err != nil {
			log.Warnw("agent supervised event publish failed", "run_id", runID, "event_type", ev.Type, "error", err)
		}
	}
	return observation
}

func (s *StudentRunService) publishDetachedRunFailure(
	ctx context.Context,
	runID uint64,
	observation supervisedRunEventObservation,
	_ error,
) {
	if observation.terminalSeen {
		return
	}

	nextSeq := observation.lastSeq
	if !observation.errorSeen {
		nextSeq++
		errorEvent, encodeErr := stream.Encode(stream.EventError, stream.ErrorPayload{
			Code:    "internal",
			Message: supervisedRunFallbackErrorMessage,
		}, nextSeq, runID, 0)
		if encodeErr == nil {
			if _, err := s.PublishRunEvent(ctx, runID, errorEvent); err != nil {
				log.Warnw("agent supervised fallback error publish failed", "run_id", runID, "error", err)
			}
		}
	}

	nextSeq++
	terminalEvent, encodeErr := stream.Encode(stream.EventTerminal, stream.TerminalPayload{
		Reason:      string(TerminalModelError),
		UserMessage: UserFacingTerminalMessage(TerminalModelError),
	}, nextSeq, runID, 0)
	if encodeErr != nil {
		return
	}
	if _, err := s.PublishRunEvent(ctx, runID, terminalEvent); err != nil {
		log.Warnw("agent supervised fallback terminal publish failed", "run_id", runID, "error", err)
	}
}
