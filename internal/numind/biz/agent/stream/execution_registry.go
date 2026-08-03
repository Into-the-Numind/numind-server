package stream

import (
	"context"
	"sync"
	"time"
)

// StreamExecution records one active supervised stream execution in this process.
type StreamExecution struct {
	RunID     uint64
	StartedAt time.Time
	Cancel    context.CancelFunc
	Done      <-chan struct{}
}

// StreamExecutionRegistry tracks process-local supervised stream executions.
type StreamExecutionRegistry struct {
	mu     sync.Mutex
	active map[uint64]*StreamExecution
}

// NewStreamExecutionRegistry creates an empty process-local execution registry.
func NewStreamExecutionRegistry() *StreamExecutionRegistry {
	return &StreamExecutionRegistry{
		active: make(map[uint64]*StreamExecution),
	}
}

// Start records an active execution and rejects duplicate starts for the same run.
func (r *StreamExecutionRegistry) Start(runID uint64, cancel context.CancelFunc, done <-chan struct{}) bool {
	if r == nil || runID == 0 || cancel == nil || done == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.active == nil {
		r.active = make(map[uint64]*StreamExecution)
	}
	if _, exists := r.active[runID]; exists {
		return false
	}
	r.active[runID] = &StreamExecution{
		RunID:     runID,
		StartedAt: time.Now(),
		Cancel:    cancel,
		Done:      done,
	}
	return true
}

// Cancel cancels an active execution by run ID.
func (r *StreamExecutionRegistry) Cancel(runID uint64) bool {
	if r == nil || runID == 0 {
		return false
	}

	r.mu.Lock()
	execution, exists := r.active[runID]
	if !exists || execution == nil || execution.Cancel == nil {
		r.mu.Unlock()
		return false
	}
	cancel := execution.Cancel
	r.mu.Unlock()

	cancel()
	return true
}

// Finish removes an execution from the active registry.
func (r *StreamExecutionRegistry) Finish(runID uint64) {
	if r == nil || runID == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, runID)
}

// IsActive reports whether a run currently has an active execution.
func (r *StreamExecutionRegistry) IsActive(runID uint64) bool {
	if r == nil || runID == 0 {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.active[runID]
	return exists
}
