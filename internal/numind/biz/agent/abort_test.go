package agent

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestAbortController_CancelPropagation: queryCancel 触发后 toolCtx 必须立即 Done。
func TestAbortController_CancelPropagation(t *testing.T) {
	parent := context.Background()
	queryCtx, queryCancel := DeriveQueryCtx(parent)
	batchCtx, batchCancel := DeriveBatchCtx(queryCtx)
	defer batchCancel()
	toolCtx, toolCancel := DeriveToolCtx(batchCtx)
	defer toolCancel()

	done := make(chan struct{})
	go func() {
		<-toolCtx.Done()
		close(done)
	}()

	queryCancel()
	select {
	case <-done:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("queryCancel did not propagate to toolCtx")
	}
}

// TestAbortController_BatchCancelIsolatesBatch: batchCancel 不影响 queryCtx 兄弟 batch。
func TestAbortController_BatchCancelIsolatesBatch(t *testing.T) {
	queryCtx, queryCancel := DeriveQueryCtx(context.Background())
	defer queryCancel()

	batch1, batch1Cancel := DeriveBatchCtx(queryCtx)
	batch2, batch2Cancel := DeriveBatchCtx(queryCtx)
	defer batch2Cancel()
	_ = batch2

	batch1Cancel()
	select {
	case <-batch1.Done():
		// expected
	default:
		t.Fatal("batch1Cancel didn't trigger batch1.Done")
	}
	select {
	case <-batch2.Done():
		t.Fatal("batch1Cancel must NOT affect batch2")
	default:
		// expected: batch2 still alive
	}
	select {
	case <-queryCtx.Done():
		t.Fatal("batch1Cancel must NOT affect queryCtx")
	default:
		// expected
	}
}

// TestAbortController_ToolCancelIsolatesTool: toolCancel 不影响 batchCtx 兄弟 tool。
func TestAbortController_ToolCancelIsolatesTool(t *testing.T) {
	queryCtx, queryCancel := DeriveQueryCtx(context.Background())
	defer queryCancel()
	batchCtx, batchCancel := DeriveBatchCtx(queryCtx)
	defer batchCancel()

	tool1, tool1Cancel := DeriveToolCtx(batchCtx)
	tool2, tool2Cancel := DeriveToolCtx(batchCtx)
	defer tool2Cancel()
	_ = tool2

	tool1Cancel()
	select {
	case <-tool1.Done():
	default:
		t.Fatal("tool1 not Done after tool1Cancel")
	}
	select {
	case <-tool2.Done():
		t.Fatal("tool2 must NOT cancel from tool1Cancel")
	default:
	}
}

// TestAbortController_ConcurrentDerivation: 多 goroutine 同时 derive + cancel，race detector 应该干净。
func TestAbortController_ConcurrentDerivation(t *testing.T) {
	queryCtx, queryCancel := DeriveQueryCtx(context.Background())
	defer queryCancel()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bc, bcancel := DeriveBatchCtx(queryCtx)
			defer bcancel()
			_, tcancel := DeriveToolCtx(bc)
			defer tcancel()
		}()
	}
	wg.Wait()
}
