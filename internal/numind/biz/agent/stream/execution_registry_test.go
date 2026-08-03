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

func TestStreamExecutionRegistry_ConcurrentStartOnlyOneWins(t *testing.T) {
	registry := stream.NewStreamExecutionRegistry()
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if registry.Start(42, func() {}, make(chan struct{})) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), wins.Load())
}
