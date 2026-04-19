package langfuse

import (
	"sync"
	"time"

	"numind-server/internal/pkg/log"
)

const (
	channelSize   = 2000
	batchSize     = 50
	flushInterval = 3 * time.Second
)

// Client Langfuse 异步客户端 — channel + batch + HTTP POST
type Client struct {
	ch        chan *IngestionEvent
	done      chan struct{}
	wg        sync.WaitGroup
	enabled   bool
	baseURL   string
	publicKey string
	secretKey string

	mu      sync.Mutex
	onEvent func(*IngestionEvent) // test hook; nil in production
}

// C 全局 Langfuse 客户端单例
var C *Client

// Init 初始化全局 Langfuse 客户端
func Init(cfg *Config) {
	if cfg == nil || !cfg.Enabled {
		C = &Client{enabled: false}
		log.Infow("Langfuse client disabled")
		return
	}
	if cfg.PublicKey == "" || cfg.SecretKey == "" {
		C = &Client{enabled: false}
		log.Warnw("Langfuse enabled but public_key/secret_key is empty, auto-disabling")
		return
	}

	C = &Client{
		ch:        make(chan *IngestionEvent, channelSize),
		done:      make(chan struct{}),
		enabled:   true,
		baseURL:   cfg.BaseURL,
		publicKey: cfg.PublicKey,
		secretKey: cfg.SecretKey,
	}
	C.wg.Add(1)
	go C.processLoop()
	log.Infow("Langfuse client initialized", "base_url", cfg.BaseURL)
}

// Enqueue 非阻塞入队（参考 billing.Record）
//
// The onEvent hook (see InstallEventHook) is invoked synchronously BEFORE
// normal channel dispatch. Used by test code to capture span/trace events
// deterministically without spinning up an httptest server. Production uses
// do not install a hook; hook is nil by default.
func (c *Client) Enqueue(event *IngestionEvent) {
	if c == nil || !c.enabled || event == nil {
		return
	}
	if c.onEvent != nil {
		c.onEvent(event)
	}
	select {
	case c.ch <- event:
	default:
		log.Warnw("langfuse: channel full, dropping event", "type", event.Type)
	}
}

// InstallEventHook registers a callback invoked synchronously on each Enqueue
// call. Primary use: tests that want to assert on the exact events emitted
// without network round-tripping. Pass nil to clear.
//
// Safe to call at any time; calls serialize on the mu mutex. The hook runs
// inside Enqueue, so keep it lightweight — expensive work should be deferred
// to a goroutine.
func (c *Client) InstallEventHook(hook func(*IngestionEvent)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.onEvent = hook
	c.mu.Unlock()
}

// NewTestClient returns an "enabled" client that never dispatches to HTTP.
// It creates a buffered channel but does NOT spawn the processLoop, so
// tests stay deterministic and don't need Stop() to flush. Install a hook
// via InstallEventHook to capture events. Replaces the global C for the
// duration of the test; caller is responsible for restoring if needed.
//
// Example:
//
//	captured := []*IngestionEvent{}
//	prev := langfuse.C
//	langfuse.C = langfuse.NewTestClient()
//	langfuse.C.InstallEventHook(func(e *IngestionEvent) { captured = append(captured, e) })
//	defer func() { langfuse.C = prev }()
func NewTestClient() *Client {
	return &Client{
		ch:      make(chan *IngestionEvent, channelSize),
		enabled: true,
	}
}

// Stop 优雅关闭：关闭 channel 并等待所有事件处理完毕
func (c *Client) Stop() {
	if c == nil || !c.enabled {
		return
	}
	close(c.ch)
	c.wg.Wait()
	close(c.done)
	log.Infow("Langfuse client stopped")
}

// processLoop 后台批量处理事件
func (c *Client) processLoop() {
	defer c.wg.Done()

	batch := make([]*IngestionEvent, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-c.ch:
			if !ok {
				if len(batch) > 0 {
					c.flush(batch)
				}
				return
			}
			batch = append(batch, event)
			if len(batch) >= batchSize {
				c.flush(batch)
				batch = make([]*IngestionEvent, 0, batchSize)
			}
		case <-ticker.C:
			if len(batch) > 0 {
				c.flush(batch)
				batch = make([]*IngestionEvent, 0, batchSize)
			}
		}
	}
}
