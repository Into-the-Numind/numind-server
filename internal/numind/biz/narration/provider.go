package narration

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"numind-server/internal/pkg/log"
)

// Provider is the singleton narration entry point.
// Wired into AgentRunner via agent.WithNarrationProvider(prov).
type Provider struct {
	translator *Translator
	streamer   Streamer
	callSeq    sync.Map // map[uint64]*atomic.Int64 — see nextCallID (S1-D18)
}

// Config drives NewProvider construction.
type Config struct {
	YAMLPath    string      // if non-empty, NewProvider loads from disk
	YAMLBytes   []byte      // alternative for tests; ignored if YAMLPath is set
	BufferSize  int         // per-Run channel cap; 0 → defaultBufferSize
	LLMFallback LLMFallback // nil → stubLLMFallback
	ToolNames   []string    // optional; if non-nil, NewProvider warns on missing yaml keys
}

// NewProvider constructs the singleton.
// Fails fast on YAML file missing, parse error, or template parse error (S1-D9 + S1-D16).
// Warn-only on missing yaml keys for ToolNames (S1-D10).
func NewProvider(cfg Config) (*Provider, error) {
	var renderer *Renderer
	var err error
	switch {
	case cfg.YAMLPath != "":
		renderer, err = NewRendererFromPath(cfg.YAMLPath)
	case len(cfg.YAMLBytes) > 0:
		renderer, err = NewRendererFromBytes(cfg.YAMLBytes)
	default:
		return nil, fmt.Errorf("narration.NewProvider: either YAMLPath or YAMLBytes required")
	}
	if err != nil {
		return nil, fmt.Errorf("narration.NewProvider: %w", err)
	}

	if len(cfg.ToolNames) > 0 {
		if missing := renderer.ValidateToolNames(cfg.ToolNames); len(missing) > 0 {
			log.Warnw("narration: tool names missing from yaml (will use defaults block)",
				"missing", missing)
		}
	}

	return &Provider{
		translator: NewTranslator(renderer, cfg.LLMFallback),
		streamer:   newMemStreamer(cfg.BufferSize),
	}, nil
}

// Emit is the adapter's single entry point.
// Fire-and-forget: never blocks, never returns error (errors logged at warn).
// Provider fills in ToolCallID if empty and computes Event via Translator,
// then hands to Streamer.
func (p *Provider) Emit(ctx context.Context, runID uint64, toolName string, state State, payload EmitPayload) {
	payload.RunID = runID
	if payload.ToolCallID == "" {
		payload.ToolCallID = p.nextCallID(runID)
	}
	ev := p.translator.Translate(ctx, payload, toolName, state)
	p.streamer.Send(ev)
}

// Subscribe re-exports streamer for #11 student-ux consumer.
func (p *Provider) Subscribe(runID uint64) (<-chan Event, func()) {
	return p.streamer.Subscribe(runID)
}

// CloseRun re-exports streamer for runner.Run defer (S1-D20).
// Also deletes the per-runID callSeq counter to bound memory.
func (p *Provider) CloseRun(runID uint64) {
	p.streamer.CloseRun(runID)
	p.callSeq.Delete(runID)
}

// nextCallID returns "<runID>-<seq>" with seq monotonically incrementing per runID.
// MUST use sync.Map.LoadOrStore (S1-D18 / S1 P0-2 fix) to avoid TOCTOU race
// under concurrent InvokableRun calls within the same Run.
func (p *Provider) nextCallID(runID uint64) string {
	v, _ := p.callSeq.LoadOrStore(runID, &atomic.Int64{})
	seq := v.(*atomic.Int64).Add(1)
	return fmt.Sprintf("%d-%d", runID, seq)
}
