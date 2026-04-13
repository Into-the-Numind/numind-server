package langfuse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEnqueueAndFlush(t *testing.T) {
	var receivedCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch IngestionBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Errorf("failed to decode batch: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		receivedCount.Add(int32(len(batch.Batch)))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	Init(&Config{
		Enabled:   true,
		BaseURL:   server.URL,
		PublicKey: "pk-test",
		SecretKey: "sk-test",
	})
	defer C.Stop()

	// 入队 100 个事件
	for i := 0; i < 100; i++ {
		traceID := TraceID()
		CreateTrace(traceID, "test-trace")
	}

	// 等待 flush
	time.Sleep(4 * time.Second)

	count := receivedCount.Load()
	assert.Equal(t, int32(100), count, "should have received all 100 events")
}

func TestDisabledClient(t *testing.T) {
	Init(&Config{Enabled: false})

	// 不应 panic
	CreateTrace("test-id", "test")
	CreateGeneration("test-id", "gen-id", WithGenModel("gpt-4"))
	Score("test-id", "quality", 5.0, "good")
}

func TestContextPropagation(t *testing.T) {
	ctx := context.Background()

	// 未注入时返回 nil
	assert.Nil(t, FromContext(ctx))

	// 注入后可提取
	ctx = WithTrace(ctx, "trace-123")
	tc := FromContext(ctx)
	assert.NotNil(t, tc)
	assert.Equal(t, "trace-123", tc.TraceID)
	assert.Empty(t, tc.ParentObservationID)

	// 注入 parent
	ctx = WithTraceAndParent(ctx, "trace-123", "span-456")
	tc = FromContext(ctx)
	assert.Equal(t, "trace-123", tc.TraceID)
	assert.Equal(t, "span-456", tc.ParentObservationID)
}

func TestChannelFull(t *testing.T) {
	Init(&Config{
		Enabled:   true,
		BaseURL:   "http://localhost:99999", // 不可达
		PublicKey: "pk-test",
		SecretKey: "sk-test",
	})

	// 填满 channel — 不应阻塞
	for i := 0; i < channelSize+100; i++ {
		C.Enqueue(&IngestionEvent{
			ID:   TraceID(),
			Type: "trace-create",
			Body: &TraceBody{ID: TraceID(), Name: "overflow-test", Timestamp: time.Now()},
			Time: time.Now(),
		})
	}

	C.Stop()
}

func TestCompile(t *testing.T) {
	template := "Hello {{name}}, your query is: {{query}}"
	result := Compile(template, map[string]string{
		"name":  "Alice",
		"query": "how to sell",
	})
	assert.Equal(t, "Hello Alice, your query is: how to sell", result)
}
