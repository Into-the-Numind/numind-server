package stream_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"numind-server/internal/numind/biz/agent/stream"

	"github.com/stretchr/testify/require"
)

func TestStreamExecutionRegistry_StartCancelFinish(t *testing.T) {
	registry := stream.NewStreamExecutionRegistry()
	cancelled := false
	done := make(chan struct{})

	require.True(t, registry.Start(42, func() { cancelled = true }, done))
	require.True(t, registry.IsActive(42))
	require.True(t, registry.Cancel(42))
	require.True(t, cancelled)

	close(done)
	registry.Finish(42)
	require.False(t, registry.IsActive(42))
	require.False(t, registry.Cancel(42))
}

func TestStreamExecutionRegistry_StartIsSingleFlightPerRun(t *testing.T) {
	registry := stream.NewStreamExecutionRegistry()
	require.True(t, registry.Start(42, func() {}, make(chan struct{})))
	require.False(t, registry.Start(42, func() {}, make(chan struct{})))
	require.True(t, registry.Start(43, func() {}, make(chan struct{})))
}

func TestStreamExecutionRegistry_Guardrails(t *testing.T) {
	done := make(chan struct{})

	var nilRegistry *stream.StreamExecutionRegistry
	require.False(t, nilRegistry.Start(42, func() {}, done))
	require.False(t, nilRegistry.Cancel(42))
	require.False(t, nilRegistry.IsActive(42))
	require.NotPanics(t, func() {
		nilRegistry.Finish(42)
	})

	registry := stream.NewStreamExecutionRegistry()
	require.False(t, registry.Start(0, func() {}, done))
	require.False(t, registry.Start(42, nil, done))
	require.False(t, registry.Start(42, func() {}, nil))
	require.False(t, registry.Cancel(0))
	require.False(t, registry.Cancel(42))
	require.False(t, registry.IsActive(0))
}

func TestStreamExecutionRegistry_ZeroValueStartAndFinish(t *testing.T) {
	var registry stream.StreamExecutionRegistry

	require.True(t, registry.Start(42, func() {}, make(chan struct{})))
	require.True(t, registry.IsActive(42))

	require.NotPanics(t, func() {
		registry.Finish(42)
		registry.Finish(42)
		registry.Finish(0)
	})
	require.False(t, registry.IsActive(42))
	require.False(t, registry.Cancel(42))
}

func TestStreamExecutionRegistry_ConcurrentStartOnlyOneWins(t *testing.T) {
	registry := stream.NewStreamExecutionRegistry()
	var wins atomic.Int64
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	start := make(chan struct{})

	ready.Add(100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			if registry.Start(42, func() {}, make(chan struct{})) {
				wins.Add(1)
			}
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	require.Equal(t, int64(1), wins.Load())
}
