package ingest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestEmbeddingSplitter_RetryOnTransient (T4): /split 第一次 5xx(瞬时)→ 重试一次成功。
func TestEmbeddingSplitter_RetryOnTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError) // 第一次瞬时失败
			return
		}
		_ = json.NewEncoder(w).Encode(SplitResponse{Success: true, Chunks: []EmbeddingChunk{{Content: "ok"}}})
	}))
	defer srv.Close()

	s := NewEmbeddingSplitter(EmbeddingSplitterConfig{ServerURL: srv.URL})
	chunks, err := s.Split("一些需要切分的文本内容")
	assert.NoError(t, err, "5xx 重试后应成功")
	assert.NotEmpty(t, chunks)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "5xx 应重试一次(共 2 次调用)")
}

// TestEmbeddingSplitter_NoRetryOn4xx (T4): 4xx 是请求问题,不重试。
func TestEmbeddingSplitter_NoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	s := NewEmbeddingSplitter(EmbeddingSplitterConfig{ServerURL: srv.URL})
	_, err := s.Split("一些文本")
	assert.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "4xx 不应重试(仅 1 次调用)")
}

// TestHybridSplitter_ReprobeAfterTTL (T4): 语义从不可用→可用,超过 TTL 后重探自动重新启用。
func TestHybridSplitter_ReprobeAfterTTL(t *testing.T) {
	var ready int32 // 0=down, 1=up
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if atomic.LoadInt32(&ready) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "model_ready": true})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	h := NewHybridSplitter(HybridSplitterConfig{SemanticConfig: EmbeddingSplitterConfig{ServerURL: srv.URL}})
	assert.False(t, h.IsSemanticAvailable(), "启动时语义服务 down → 不可用")

	// 服务恢复 + 强制 TTL 过期
	atomic.StoreInt32(&ready, 1)
	h.mu.Lock()
	h.lastProbeAt = time.Now().Add(-time.Hour)
	h.mu.Unlock()

	assert.True(t, h.refreshAvailability(), "TTL 过期 + 服务恢复后,重探应自动重新启用语义")
}
