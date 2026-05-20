# Design Spec: Agent Mode Narration Layer (#8/14)

**Status**: S2 → S3 transition
**Feature ID**: `agent-mode-narration-layer`
**Proposal**: `numind-server/proposals/agent-mode-narration-layer-proposal.md`
**Blueprint**: `docs/agent-mode/architecture-v1.md` §4.7

This spec converts the S1 proposal into exact Go signatures, file contents (or skeletons), and test fixtures ready for S3 plan & S4 implementation. Every API mentioned here is final; deviations require a manifest decision.

---

## 1. Package Layout

```
internal/numind/biz/narration/
├── event.go                       (~80 LOC)
├── display.go                     (~180 LOC)
├── error_translate.go             (~70 LOC)
├── translator.go                  (~120 LOC)
├── streamer.go                    (~180 LOC)
├── provider.go                    (~130 LOC)
├── event_test.go                  (~30 LOC)
├── display_test.go                (~200 LOC)
├── error_translate_test.go        (~80 LOC)
├── translator_test.go             (~140 LOC)
├── streamer_test.go               (~200 LOC)
└── provider_test.go               (~130 LOC)

configs/
└── tool-display.yaml              (~80 LOC)

internal/numind/biz/agent/         (MODIFY only)
├── hooks.go                       (+2 fields)
├── adapter_full_to_eino.go        (+1 helper, +3 emit sites, +1 import)
├── adapter_full_to_eino_test.go   (+4 fixtures)
├── runner.go                      (+1 option, +3 lines in Run, +1 field, +1 import)
├── runner_test.go                 (+1 fixture)
└── hooks_test.go                  (+1 test)

internal/numind/biz/biz.go         (+4 lines wire, +1 import)
```

---

## 2. event.go — exact contents

```go
// Package narration implements the learner-facing narration layer for Agent
// Runtime (blueprint §4.7). See proposals/agent-mode-narration-layer-proposal.md
// for architecture and ADRs.
package narration

import (
	"encoding/json"
	"time"
)

// State is the lifecycle phase of a tool call as visible to the learner.
// Blueprint §4.7.4 defines 6 values; v1 actively emits use/result/error/rejected.
// queued is reserved for #14 (ReAct loop reification); progress for #13 (sandbox push).
type State string

const (
	StateQueued   State = "queued"
	StateUse      State = "use"
	StateProgress State = "progress"
	StateResult   State = "result"
	StateError    State = "error"
	StateRejected State = "rejected"
)

// IsTerminal returns true if the state is a terminal-emit for a tool call.
// (Used by streamer for housekeeping and by tests for completeness assertions.)
func (s State) IsTerminal() bool {
	return s == StateResult || s == StateError || s == StateRejected
}

// Event is the wire-format struct surfaced via Provider.Subscribe(runID).
// JSON tags target the #11 student-ux SSE consumer.
type Event struct {
	RunID      uint64    `json:"run_id"`
	ToolCallID string    `json:"tool_call_id"`           // "<runID>-<seq>"; set by Provider (S1-D8)
	ToolName   string    `json:"tool_name"`              // matches FullTool.Name()
	State      State     `json:"state"`
	Verb       string    `json:"verb,omitempty"`         // "正在执行"
	Detail     string    `json:"detail,omitempty"`       // "你的脚本"
	Icon       string    `json:"icon,omitempty"`         // "⋯" / "✓" / "⚠️" / "✕"
	Message    string    `json:"message"`                // rendered Chinese
	Reason     string    `json:"reason,omitempty"`       // StateRejected reason; v1 always ""
	Timestamp  time.Time `json:"timestamp"`
}

// EmitPayload is what callers (adapter) pass to Provider.Emit.
// Provider fills in computed fields (Icon, Message, Timestamp, ToolCallID)
// before fanning out to streamer.
type EmitPayload struct {
	RunID           uint64
	ToolCallID      string          // optional; auto-generated if empty
	Input           json.RawMessage // observable input (post-BackfillObservableInput)
	Result          json.RawMessage // for StateResult
	Err             error           // for StateError
	Reason          string          // for StateRejected; v1 always "" (S1-D21)
	OverrideMessage string          // reserved for #14 LLM-supplied narration (S1-D17); v1 always ""
}

// iconForState is the canonical State→icon mapping (blueprint §4.7.6).
func iconForState(s State) string {
	switch s {
	case StateResult:
		return "✓"
	case StateError:
		return "⚠️"
	case StateRejected:
		return "✕"
	default: // queued, use, progress
		return "⋯"
	}
}
```

---

## 3. display.go — exact API

```go
package narration

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// YAMLConfig is the structure of configs/tool-display.yaml.
type YAMLConfig struct {
	Tools    map[string]ToolTemplates `yaml:"tools"`
	Defaults ToolTemplates            `yaml:"defaults"`
}

// ToolTemplates holds the 4 v1-emitted template strings + verb/detail.
type ToolTemplates struct {
	Verb              string `yaml:"verb"`
	DetailTemplate    string `yaml:"detail_template"`
	UseTemplate       string `yaml:"use_template"`
	ResultTemplate    string `yaml:"result_template"`
	ErrorTemplate     string `yaml:"error_template"`
	RejectedTemplate  string `yaml:"rejected_template"`
}

// Renderer holds parsed templates ready for execution. Created by NewRenderer.
// Once created, Renderer is read-only and safe for concurrent use.
type Renderer struct {
	tools    map[string]*compiledTemplates  // keyed by tool name
	defaults *compiledTemplates
}

type compiledTemplates struct {
	verb     string
	detail   *template.Template // nil if empty source
	use      *template.Template
	result   *template.Template
	err      *template.Template
	rejected *template.Template
}

// NewRendererFromPath loads YAML from disk and compiles all templates.
// Returns error if file missing, YAML invalid, or ANY template fails to parse.
// Per S1-D9 + S1-D16, all parse-time failures are fail-fast.
func NewRendererFromPath(path string) (*Renderer, error) { ... }

// NewRendererFromBytes is the test-friendly variant; same fail-fast semantics.
func NewRendererFromBytes(data []byte) (*Renderer, error) { ... }

// ValidateToolNames warns (NOT errors) on FullTool names registered in the agent
// registry that have no yaml entry. Per S1-D10, missing keys fall back to defaults
// — this is a startup observability call, not a hard check.
// Caller (provider.NewProvider) logs via zap.
func (r *Renderer) ValidateToolNames(names []string) (missing []string) { ... }

// renderRequest is the internal struct passed to render methods.
type renderRequest struct {
	ToolName        string
	State           State
	Input           map[string]any  // unmarshaled EmitPayload.Input
	Result          map[string]any  // unmarshaled EmitPayload.Result
	ReasonFriendly  string          // from error_translate.ClassifyError
}

// Render is the entry point: dispatches to the right template by State,
// falls back to defaults block if tool not in yaml.
// NEVER returns error; on template execution panic, returns ("","") and the
// caller (Translator) falls through to LLMFallback. (S1-D9 panic recovery)
func (r *Renderer) Render(req renderRequest) (verb, detail, message string) { ... }

// renderTemplate executes a single template with defer-recover.
// Returns ("", true) if no template configured for this slot (caller falls back).
// Returns ("", false) on panic — caller logs and continues.
func renderTemplate(tmpl *template.Template, data any) (out string, ok bool) {
	if tmpl == nil {
		return "", true // empty source = no message for this slot; not an error
	}
	defer func() {
		if r := recover(); r != nil {
			out = ""
			ok = false
		}
	}()
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", false
	}
	return buf.String(), true
}

// templateFuncs is the FuncMap registered on all templates.
// NOTE (S2 P0-2 fix): `len` is a text/template BUILTIN — re-registering panics
// "function len already defined". Keep this map free of any builtin name.
// Builtins available without registration: and, call, html, index, slice, js,
// len, not, or, print, printf, println, urlquery, eq, ne, lt, le, gt, ge.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"truncate": func(n int, s string) string {
			if len(s) <= n { return s }
			return s[:n] + "..."
		},
		"default": func(fallback, val string) string {
			if val == "" { return fallback }
			return val
		},
	}
}
```

### 3.1 Compilation algorithm (S1-D9 fail-fast + S2-D1 missingkey)

`NewRendererFromBytes`:

```
1. Unmarshal YAML → YAMLConfig
2. If config.Defaults is zero-value → error "defaults block required"
3. Compile defaults: parse all 4 template strings via
   template.New(key).Funcs(templateFuncs()).Option("missingkey=zero").Parse(src)
   - Each parse error → return wrapped error with field name (e.g., "defaults.use_template parse: ...")
4. For each (toolName, templates) in config.Tools:
   - Compile all 4 templates; each error wrapped with tool name
5. Return *Renderer with populated maps
```

**S2-D1 (S2 P0-3 fix)**: Every `template.New(...)` call MUST chain `.Option("missingkey=zero")` before `.Parse(...)`. Default text/template behavior emits the literal string `<no value>` when a map key is absent — this would render verbatim in user-facing narration (e.g., `default "文档" .input.format` returns `<no value>` instead of `"文档"`). Setting `missingkey=zero` makes missing keys evaluate to the zero value (empty string for string fields), letting `default` work correctly.

Empty template strings yield `nil` `*template.Template` and `renderTemplate` returns `("",true)` — i.e., "this state has no message for this tool" is valid (caller decides what to do).

**Note on `buildTemplateData` placement** (S2 P0-1 clarification): `buildTemplateData` lives in `translator.go` (see §5), NOT in `display.go`. `display.go::Renderer.Render` accepts a pre-built `renderRequest` struct; the caller (Translator) is responsible for constructing it via `buildTemplateData`. M3 (display.go) and M5 (translator.go) own different files; no overlap.

---

## 4. error_translate.go — exact contents

```go
package narration

import (
	"context"
	"errors"
)

type ErrorCategory string

const (
	ErrCatContextCanceled  ErrorCategory = "context_canceled"
	ErrCatDeadlineExceeded ErrorCategory = "deadline_exceeded"
	ErrCatPermissionDenied ErrorCategory = "permission_denied"
	ErrCatSandboxKilled    ErrorCategory = "sandbox_killed"
	ErrCatGeneric          ErrorCategory = "generic"
)

// friendlyReasons is the locked Chinese mapping (S1-D11).
// Modification of these strings requires a manifest decision update.
var friendlyReasons = map[ErrorCategory]string{
	ErrCatContextCanceled:  "操作被中断",
	ErrCatDeadlineExceeded: "超过时间限制",
	ErrCatPermissionDenied: "这个操作没有权限",
	ErrCatSandboxKilled:    "运行环境被回收",
	ErrCatGeneric:          "稍后再试一下",
}

// Sentinel errors that v1 cannot distinguish but reserves API for #6/#13:
// these match nothing in v1; future packages errors.Is against them via wrap.
var (
	ErrPermissionDenied = errors.New("narration: permission denied")
	ErrSandboxKilled    = errors.New("narration: sandbox killed")
)

// ClassifyError takes a (possibly nil) error and returns (category, friendlyReason).
// nil err → (ErrCatGeneric, friendlyReasons[ErrCatGeneric]) — defensive; never called for nil.
//
// Classification order (first match wins):
//  1. errors.Is(err, context.Canceled)         → context_canceled
//  2. errors.Is(err, context.DeadlineExceeded) → deadline_exceeded
//  3. errors.Is(err, ErrPermissionDenied)      → permission_denied  (#6 placeholder)
//  4. errors.Is(err, ErrSandboxKilled)         → sandbox_killed     (#13 placeholder)
//  5. default                                  → generic
func ClassifyError(err error) (ErrorCategory, string) {
	if err == nil {
		return ErrCatGeneric, friendlyReasons[ErrCatGeneric]
	}
	switch {
	case errors.Is(err, context.Canceled):
		return ErrCatContextCanceled, friendlyReasons[ErrCatContextCanceled]
	case errors.Is(err, context.DeadlineExceeded):
		return ErrCatDeadlineExceeded, friendlyReasons[ErrCatDeadlineExceeded]
	case errors.Is(err, ErrPermissionDenied):
		return ErrCatPermissionDenied, friendlyReasons[ErrCatPermissionDenied]
	case errors.Is(err, ErrSandboxKilled):
		return ErrCatSandboxKilled, friendlyReasons[ErrCatSandboxKilled]
	default:
		return ErrCatGeneric, friendlyReasons[ErrCatGeneric]
	}
}
```

---

## 5. translator.go — exact API

```go
package narration

import (
	"context"
	"encoding/json"
	"time"
)

// LLMFallback is the v1 abstraction point for #14 to plug aiservice.Chat.
// Implementations MUST handle their own errors and return safe defaults
// (verb, detail) — never propagate error out. (S1-D12)
type LLMFallback interface {
	Render(ctx context.Context, toolName string, state State, payload EmitPayload) (verb, detail string)
}

// stubLLMFallback is the deterministic v1 default impl (no LLM call).
type stubLLMFallback struct{}

func (stubLLMFallback) Render(_ context.Context, toolName string, state State, _ EmitPayload) (string, string) {
	switch state {
	case StateUse, StateQueued:
		return "正在执行", toolName
	case StateResult:
		return "完成", toolName
	case StateError:
		return "执行出错", toolName
	case StateRejected:
		return "操作被拦截", toolName
	default: // StateProgress and any future
		return "处理中", toolName
	}
}

// Translator combines the yaml Renderer + LLM fallback.
type Translator struct {
	renderer *Renderer
	fallback LLMFallback
}

func NewTranslator(r *Renderer, f LLMFallback) *Translator {
	if f == nil {
		f = stubLLMFallback{}
	}
	return &Translator{renderer: r, fallback: f}
}

// Translate is the core synchronous render call. Always returns a usable Event.
// Provider.Emit calls this then hands the Event to streamer.Send.
func (t *Translator) Translate(ctx context.Context, payload EmitPayload, toolName string, state State) Event {
	// 1. Compute reasonFriendly (for error state; benign for others)
	_, reasonFriendly := ClassifyError(payload.Err)

	// 2. Build template data (defensive unmarshal — nil-safe)
	data := buildTemplateData(payload, reasonFriendly)

	// 3. Try yaml renderer (handles fallback to defaults block internally)
	verb, detail, message := t.renderer.Render(renderRequest{
		ToolName:       toolName,
		State:          state,
		Input:          data["input"].(map[string]any),
		Result:         data["result"].(map[string]any),
		ReasonFriendly: reasonFriendly,
	})

	// 4. If renderer produced empty for the chosen state, fall back to LLM
	if message == "" {
		verb, detail = t.fallback.Render(ctx, toolName, state, payload)
		message = verb + " " + detail
	}

	// 5. v1 OverrideMessage is always "" (reserved for #14); honor if set
	if payload.OverrideMessage != "" {
		message = payload.OverrideMessage
	}

	return Event{
		RunID:      payload.RunID,
		ToolCallID: payload.ToolCallID,
		ToolName:   toolName,
		State:      state,
		Verb:       verb,
		Detail:     detail,
		Icon:       iconForState(state),
		Message:    message,
		Reason:     payload.Reason,
		Timestamp:  nowFunc(),
	}
}

// buildTemplateData wraps payload fields in maps suitable for text/template access.
// Inputs that fail to JSON-unmarshal become empty maps (NEVER nil — templates blow up
// on nil map deref).
func buildTemplateData(p EmitPayload, reasonFriendly string) map[string]any {
	inputMap := map[string]any{}
	if len(p.Input) > 0 {
		_ = json.Unmarshal(p.Input, &inputMap) // best effort
	}
	resultMap := map[string]any{}
	if len(p.Result) > 0 {
		_ = json.Unmarshal(p.Result, &resultMap)
	}
	return map[string]any{
		"input":           inputMap,
		"result":          resultMap,
		"reason_friendly": reasonFriendly,
		"verb":            "",
		"detail":          "",
	}
}

// nowFunc allows tests to inject a deterministic timestamp.
var nowFunc = time.Now
```

---

## 6. streamer.go — exact API

```go
package narration

import (
	"sync"
	"sync/atomic"

	"numind-server/internal/pkg/log"
)

// Streamer is the per-runID event channel registry.
// v1 has a single in-memory impl; #11 may wrap.
type Streamer interface {
	Send(ev Event)                                     // adapter side; non-blocking
	Subscribe(runID uint64) (<-chan Event, func())     // consumer side; cleanup callback signals abandon
	CloseRun(runID uint64)                              // adapter side; final teardown
}

const defaultBufferSize = 256

// memStreamer is the v1 impl.
type memStreamer struct {
	mu       sync.RWMutex
	runs     map[uint64]*runChannel
	bufferSz int
}

func newMemStreamer(bufferSz int) *memStreamer {
	if bufferSz <= 0 {
		bufferSz = defaultBufferSize
	}
	return &memStreamer{
		runs:     make(map[uint64]*runChannel),
		bufferSz: bufferSz,
	}
}

type runChannel struct {
	ch     chan Event
	closed atomic.Bool
}

// Send routes event to the per-runID channel. Lazy-creates the channel if absent.
func (s *memStreamer) Send(ev Event) {
	rc := s.getOrCreate(ev.RunID)
	dropped := rc.send(ev)
	if dropped {
		log.Warnw("narration stream buffer full; dropping oldest",
			"run_id", ev.RunID,
			"tool_call_id", ev.ToolCallID,
			"state", string(ev.State))
	}
}

func (s *memStreamer) Subscribe(runID uint64) (<-chan Event, func()) {
	rc := s.getOrCreate(runID)
	cleanup := func() {
		// v1: cleanup is a no-op signal (subscriber abandoned).
		// Multi-subscriber fan-out is #11's concern (S1-D13).
		// Channel close is owned by CloseRun (ADR-10).
	}
	return rc.ch, cleanup
}

func (s *memStreamer) CloseRun(runID uint64) {
	s.mu.Lock()
	rc, ok := s.runs[runID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.runs, runID)
	s.mu.Unlock()
	rc.close()
}

func (s *memStreamer) getOrCreate(runID uint64) *runChannel {
	s.mu.RLock()
	if rc, ok := s.runs[runID]; ok {
		s.mu.RUnlock()
		return rc
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-check under write lock
	if rc, ok := s.runs[runID]; ok {
		return rc
	}
	rc := &runChannel{ch: make(chan Event, s.bufferSz)}
	s.runs[runID] = rc
	return rc
}

// send is the race-safe push (S1-D19 / S1 P1-1 fix).
// Returns true if the event was dropped (channel full OR already closed).
func (rc *runChannel) send(ev Event) (dropped bool) {
	if rc.closed.Load() {
		return true
	}
	defer func() {
		if r := recover(); r != nil {
			// Race: closed between Load() and channel write. Benign — drop.
			dropped = true
		}
	}()
	select {
	case rc.ch <- ev:
		return false
	default:
		// Buffer full — drain one (drop oldest) then push.
		select {
		case <-rc.ch:
		default:
		}
		select {
		case rc.ch <- ev:
			return false
		default:
			return true
		}
	}
}

// close is idempotent (S1-D19).
func (rc *runChannel) close() {
	if rc.closed.CompareAndSwap(false, true) {
		close(rc.ch)
	}
}
```

---

## 7. provider.go — exact API

```go
package narration

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"numind-server/internal/pkg/log"
)

// Provider is the singleton entry point. Wire via agent.WithNarrationProvider.
type Provider struct {
	translator *Translator
	streamer   Streamer
	callSeq    sync.Map // map[uint64]*atomic.Int64
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
// Fails fast on:
//   - YAMLPath set but file missing or unreadable
//   - YAMLPath unset and YAMLBytes nil
//   - YAML parse error
//   - ANY template parse error (S1-D9)
// Warn-only on:
//   - Missing yaml keys for known ToolNames (S1-D10)
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
func (p *Provider) Emit(ctx context.Context, runID uint64, toolName string, state State, payload EmitPayload) {
	payload.RunID = runID
	if payload.ToolCallID == "" {
		payload.ToolCallID = p.nextCallID(runID)
	}
	ev := p.translator.Translate(ctx, payload, toolName, state)
	p.streamer.Send(ev)
}

// Subscribe re-exports streamer for #11.
func (p *Provider) Subscribe(runID uint64) (<-chan Event, func()) {
	return p.streamer.Subscribe(runID)
}

// CloseRun re-exports streamer for runner.Run defer (S1-D20).
func (p *Provider) CloseRun(runID uint64) {
	p.streamer.CloseRun(runID)
	p.callSeq.Delete(runID) // bound memory: drop counter when Run terminates
}

// nextCallID — MUST use sync.Map.LoadOrStore (S1-D18 / S1 P0-2 fix).
func (p *Provider) nextCallID(runID uint64) string {
	v, _ := p.callSeq.LoadOrStore(runID, &atomic.Int64{})
	seq := v.(*atomic.Int64).Add(1)
	return fmt.Sprintf("%d-%d", runID, seq)
}
```

---

## 8. configs/tool-display.yaml — exact contents

```yaml
# tool-display.yaml — Narration templates for built-in agent tools.
#
# Schema (see internal/numind/biz/narration/display.go::YAMLConfig):
#   tools.<tool_name>.{verb, detail_template, use_template, result_template,
#                      error_template, rejected_template}
#   defaults.{...}  (fallback when tool not in tools map)
#
# Template engine: Go text/template; available funcs: len/truncate/default.
# Template data accessible:
#   {{ .verb }}, {{ .detail }}                — pre-computed from this entry
#   {{ .input.<field> }}                       — observable input (per FullTool.BackfillObservableInput)
#   {{ .result.<field> }}                      — tool execution output
#   {{ .reason_friendly }}                     — error_translate.ClassifyError output
#
# Empty templates ("") = no message for that state; Translator falls back to LLMFallback.

tools:
  bash_exec:
    verb: "正在执行"
    detail_template: "命令"
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "命令执行完成"
    error_template:  "命令执行中断，{{ .reason_friendly }}"
    rejected_template: "这个命令被规则拦截了"

  document_generate:
    verb: "正在生成"
    detail_template: "{{ default \"文档\" .input.format }}"
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "文档已生成"
    error_template:  "生成中断，{{ .reason_friendly }}"
    rejected_template: "文档生成被规则拦截"

  image_gen:
    verb: "正在生成"
    detail_template: "图片"
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "图片已生成"
    error_template:  "图片生成中断，{{ .reason_friendly }}"
    rejected_template: "图片生成被规则拦截"

  kb_search:
    verb: "正在搜索"
    detail_template: "知识库"
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "已查到相关内容"
    error_template:  "搜索中断，{{ .reason_friendly }}"
    rejected_template: "知识库搜索被规则拦截"

  learner_data_query:
    verb: "正在查询"
    detail_template: "你的学习数据"
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "已查到学习数据"
    error_template:  "查询中断，{{ .reason_friendly }}"
    rejected_template: "数据查询被规则拦截"

  get_current_date:
    verb: "正在获取"
    detail_template: "当前日期"
    use_template:    "{{ .verb }} {{ .detail }}"
    result_template: "已获取当前日期"
    error_template:  "获取日期失败，{{ .reason_friendly }}"
    rejected_template: "日期获取被规则拦截"

defaults:
  verb: "正在处理"
  detail_template: "操作"
  use_template:    "{{ .verb }}{{ if .detail }} {{ .detail }}{{ end }}"
  result_template: "操作完成"
  error_template:  "操作失败，{{ .reason_friendly }}"
  rejected_template: "操作被规则拦截"
```

---

## 9. biz/agent integration — exact diffs

### 9.1 hooks.go

```go
// ADD to RunHooks struct:
type RunHooks struct {
	PreToolCall       func(ctx context.Context, t tool.BaseTool, input string) (HookAction, error)
	PostToolCall      func(ctx context.Context, t tool.BaseTool, output string, err error) (HookAction, error)
	Registry          *HookActionRegistry
	NarrationProvider *narration.Provider  // #8: nil = no narration emit (legacy compat)
	NarrationRunID    uint64                // #8: per-Run; set by runner.Run
}
```

### 9.2 adapter_full_to_eino.go — full InvokableRun body

```go
func (a *fullToolEinoAdapter) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
	input := ToolInput(args)

	if a.hooks != nil && a.hooks.PreToolCall != nil {
		action, err := a.hooks.PreToolCall(ctx, a, args)
		if err != nil {
			return "", fmt.Errorf("PreToolCall: %w", err)
		}
		if a.hooks.Registry != nil {
			a.hooks.Registry.Record(action)
		}
		if action != HookActionContinue {
			a.emitNarration(ctx, narration.StateRejected, input, nil, nil, "")
			return "", fmt.Errorf("tool execution stopped by hook: action=%d", action)
		}
	}

	a.emitNarration(ctx, narration.StateUse, input, nil, nil, "")

	result, execErr := a.ft.Execute(ctx, input)
	var output string
	if result != nil {
		output = string(result)
	}

	effectiveErr := execErr
	if a.hooks != nil && a.hooks.PostToolCall != nil {
		postAction, postErr := a.hooks.PostToolCall(ctx, a, output, execErr)
		if a.hooks.Registry != nil {
			a.hooks.Registry.Record(postAction)
		}
		if postErr != nil {
			log.Warnw("PostToolCall failed",
				"tool", a.ft.Name(),
				"post_err", postErr,
				"exec_err", execErr)
			if execErr == nil {
				effectiveErr = fmt.Errorf("PostToolCall: %w", postErr)
			}
		}
	}

	if effectiveErr != nil {
		a.emitNarration(ctx, narration.StateError, input, nil, effectiveErr, "")
	} else {
		a.emitNarration(ctx, narration.StateResult, input, result, nil, "")
	}

	if effectiveErr != nil {
		return output, effectiveErr
	}
	return output, nil
}

// emitNarration is fire-and-forget; no-op when NarrationProvider is nil.
func (a *fullToolEinoAdapter) emitNarration(ctx context.Context, st narration.State, input ToolInput, result ToolResult, execErr error, reason string) {
	if a.hooks == nil || a.hooks.NarrationProvider == nil {
		return
	}
	obsInput := a.ft.BackfillObservableInput(input)
	a.hooks.NarrationProvider.Emit(ctx, a.hooks.NarrationRunID, a.ft.Name(), st, narration.EmitPayload{
		Input:  json.RawMessage(obsInput),
		Result: json.RawMessage(result),
		Err:    execErr,
		Reason: reason,
	})
}
```

(Adds `encoding/json` + `numind-server/internal/numind/biz/narration` imports.)

### 9.3 runner.go — exact insertions

```go
type agentRunner struct {
	runStore          store.IAgentRunStore
	registry          AgentToolRegistry
	cancels           map[uint64]context.CancelFunc
	mu                sync.Mutex
	defaultHooks      *RunHooks
	skillStore        store.IAgentDefinitionStore
	narrationProvider *narration.Provider // #8
}

func WithNarrationProvider(p *narration.Provider) RunnerOption {
	return func(r *agentRunner) { r.narrationProvider = p }
}

// inside Run, IMMEDIATELY after `if err := r.runStore.Create(ctx, run); err != nil { ... }`:
if r.narrationProvider != nil {
	defer r.narrationProvider.CloseRun(run.ID)
}

// at the existing hook-attachment block — IMPORTANT (S2 P1-2 fix): this attachment
// MUST come AFTER the existing `if effectiveHooks != nil && effectiveHooks.Registry == nil`
// auto-inject block, otherwise the auto-inject could overwrite a nil Registry while
// leaving the freshly-attached NarrationProvider intact (technically benign, but the
// ordering invariant is "all RunHooks fields wired before any tool call can fire"):
if effectiveHooks != nil && r.narrationProvider != nil {
	effectiveHooks.NarrationProvider = r.narrationProvider
	effectiveHooks.NarrationRunID = run.ID
}
```

### 9.4 biz/biz.go — exact insertion

```go
// In NewBiz, BEFORE `b.agentRunner = agent.NewAgentRunner(...)`:
narrationProv, err := narration.NewProvider(narration.Config{
	YAMLPath:   "configs/tool-display.yaml",
	BufferSize: 256,
	ToolNames:  agentToolNames(registry), // helper that lists registered tool names
})
if err != nil {
	return nil, fmt.Errorf("NewBiz narration: %w", err)
}

b.agentRunner = agent.NewAgentRunner(
	runStore,
	registry,
	agent.WithDefaultHooks(sandboxHookManager.AsRunHooks()),
	agent.WithSkillStore(ds.AgentDefinitions()),
	agent.WithNarrationProvider(narrationProv), // NEW
)

// helper (declared at file scope in biz.go):
func agentToolNames(reg agent.AgentToolRegistry) []string {
	if reg == nil {
		return nil
	}
	tools := reg.ListAllTools()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}
```

---

## 10. Test Fixtures (S3 plan input)

### 10.0 event_test.go — lock icon assignment (P2-1)

```go
func TestIconForState_AllValues(t *testing.T) {
	cases := map[State]string{
		StateQueued:   "⋯",
		StateUse:      "⋯",
		StateProgress: "⋯",
		StateResult:   "✓",
		StateError:    "⚠️",
		StateRejected: "✕",
	}
	for s, want := range cases {
		if got := iconForState(s); got != want {
			t.Errorf("State %q: want icon %q, got %q", s, want, got)
		}
	}
}
```

### 10.1 display_test.go — must-have fixtures

| Test | Input | Expected |
|---|---|---|
| `Test_NewRendererFromBytes_ValidYAML_AllToolsCompile` | full yaml from §8 | no error |
| `Test_NewRendererFromBytes_MissingDefaults_Errors` | yaml without `defaults:` | error contains "defaults block required" |
| `Test_NewRendererFromBytes_InvalidYAMLSyntax_Errors` | `{{` malformed yaml | error wraps `yaml.Unmarshal` err |
| `Test_NewRendererFromBytes_InvalidTemplateSyntax_Errors` | tools.bash_exec.use_template = `{{ .input.action } }` | error mentions "bash_exec" + "use_template" |
| `Test_Render_KnownTool_UsesYamlTemplate` | bash_exec + StateUse | message starts with "正在执行" |
| `Test_Render_UnknownTool_FallsBackToDefaults` | "unknown_tool" + StateResult | message = "操作完成" |
| `Test_Render_EmptyTemplate_ReturnsEmpty` | yaml entry with empty `result_template` | message == "" (caller falls through to LLMFallback) |
| `Test_Render_TemplatePanic_ReturnsEmpty` | template referencing `{{ .input.nonexistent.deep.field }}` | message == "" (defer recover, no crash) |
| `Test_ValidateToolNames_ReportsMissing` | yaml has [bash_exec], names=[bash_exec, unknown] | returns ["unknown"] |
| `Test_Render_MissingMapKey_UsesDefault` | yaml with `default "fallback" .input.missing`; input has no `missing` field | message contains "fallback" (NOT "<no value>") — proves missingkey=zero (S2-D1) |

### 10.2 error_translate_test.go — table-driven

```go
func TestClassifyError(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantCat     ErrorCategory
		wantReason  string
	}{
		{"nil", nil, ErrCatGeneric, "稍后再试一下"},
		{"context.Canceled", context.Canceled, ErrCatContextCanceled, "操作被中断"},
		{"context.DeadlineExceeded", context.DeadlineExceeded, ErrCatDeadlineExceeded, "超过时间限制"},
		{"wrapped context.Canceled", fmt.Errorf("wrap: %w", context.Canceled), ErrCatContextCanceled, "操作被中断"},
		{"ErrPermissionDenied", ErrPermissionDenied, ErrCatPermissionDenied, "这个操作没有权限"},
		{"ErrSandboxKilled", ErrSandboxKilled, ErrCatSandboxKilled, "运行环境被回收"},
		{"random error", errors.New("boom"), ErrCatGeneric, "稍后再试一下"},
	}
	// for-each assert cat == wantCat && reason == wantReason
}

func TestClassifyError_NeverLeaksRawErrText(t *testing.T) {
	err := errors.New("secret token abc123")
	_, reason := ClassifyError(err)
	if strings.Contains(reason, "secret") || strings.Contains(reason, "abc123") {
		t.Fatalf("friendly reason leaked raw err: %q", reason)
	}
}
```

### 10.3 streamer_test.go — race-mandatory fixtures

| Test | Setup | Assertion |
|---|---|---|
| `TestMemStreamer_SendThenSubscribe_DeliversInOrder` | bufferSize=10, Send 3 events, then Subscribe | all 3 received in order |
| `TestMemStreamer_SubscribeThenSend_DeliversInOrder` | Subscribe first, then Send | all received |
| `TestMemStreamer_BufferOverflow_DropsOldest` | bufferSize=1, Send 3 events without subscriber draining | exactly 1 event survives in channel; warn logged |
| `TestMemStreamer_CloseRun_Idempotent` | CloseRun called 2x | no panic |
| `TestMemStreamer_SendAfterClose_NoPanic` | CloseRun, then Send | dropped silently; no panic |
| `TestMemStreamer_ConcurrentSendClose_RaceFree` | spawn 100 goroutines: half Send, half CloseRun (different runIDs); -race | no race detected |
| `TestMemStreamer_SubscribeUnknownRun_LazyCreates` | Subscribe(99) before any Send(99) | channel returned, ready to receive |

### 10.4 provider_test.go — must-have

| Test | Setup | Assertion |
|---|---|---|
| `TestProvider_Emit_HappyPath` | in-memory yaml, fake tool, Subscribe | receive 1 event with correct ToolCallID format |
| `TestProvider_NextCallID_Monotonic` | concurrent Emit × 100 same runID | exactly 100 unique IDs, all "1-1"..."1-100" |
| `TestProvider_NextCallID_LoadOrStoreRaceSafe` | concurrent Emit different runIDs + -race | no race; correct seq per runID |
| `TestProvider_CloseRun_CleansCounter` | Emit, CloseRun, Emit again same runID | second Emit produces seq=1 (counter reset) |
| `TestProvider_Subscribe_AfterCloseRun_GetsNewOpenChan` | CloseRun first, then Subscribe (lazy-create semantic, S2-D2) | non-nil channel; no events ever arrive; recv blocks (test uses 50ms timeout) |
| `TestProvider_NilFallback_UsesStub` | NewProvider with LLMFallback=nil, tool missing in yaml | message non-empty (stub kicks in) |

### 10.5 adapter_full_to_eino_test.go — 4 new fixtures

```go
// Helper: spin up Provider with in-memory yaml; subscribe; assert events.
func TestAdapter_NarrationEmits_UseResult(t *testing.T) {
	// fake tool returns no error; Pre/Post hooks return Continue
	// expect: 1 use + 1 result event
}

func TestAdapter_NarrationEmits_UseError(t *testing.T) {
	// fake tool returns errors.New("boom"); Pre/Post return Continue
	// expect: 1 use + 1 error event; message contains no "boom"
}

func TestAdapter_NarrationEmits_Rejected_NoUseEmitted(t *testing.T) {
	// PreToolCall returns HookActionStop
	// expect: 1 rejected event; NO use; NO result; NO error
}

func TestAdapter_NarrationEmits_PostErrUpgradesToError(t *testing.T) {
	// execErr == nil, postErr != nil
	// expect: 1 use + 1 ERROR (not result), because effectiveErr is set
}
```

### 10.6 runner_test.go — 1 new fixture (P2-2 concrete pattern)

```go
func TestRunner_WithNarrationProvider_AttachesAndDefersCloseRun(t *testing.T) {
	// Note: r.effectiveHooks is internal — observable only via real Provider behavior.
	// Pattern: use the REAL narration.Provider with an in-memory yaml fixture, then
	// Subscribe to assert the channel is created (proof of attachment) and closed
	// after Run returns (proof of CloseRun fired).

	prov, err := narration.NewProvider(narration.Config{
		YAMLBytes:  fixtureMinimalYAML, // 1-tool yaml fixture
		BufferSize: 8,
	})
	require.NoError(t, err)

	runStore := newFakeRunStore(t)
	runner := agent.NewAgentRunner(runStore, nil, agent.WithNarrationProvider(prov))

	// Subscribe BEFORE Run so we don't race on lazy channel creation
	// (use runID we know will be assigned: fakeRunStore.Create assigns 1)
	ch, cleanup := prov.Subscribe(1)
	defer cleanup()

	_, _ = runner.Run(context.Background(), agent.RunRequest{UserID: 1, SessionID: "test"})

	// After Run completes, the per-runID channel must be closed (CloseRun fired).
	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel should be closed after Run completes")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("channel did not close within 100ms — CloseRun defer not fired")
	}
}
```

This pattern is mandatory; do NOT replace with a mock Provider that captures CloseRun calls separately — the real-channel-close path is the integration contract being verified.

---

## 11. Acceptance Trace (S5 fixture)

A single E2E walk that exercises every emit site is captured in S5:

1. Boot biz with in-memory yaml fixture containing 1 tool `fake_tool`
2. Build 4 RunHooks variants → 4 Runs:
   - HappyHooks (Continue/Continue), fake_tool returns `{"ok": true}` → expect `use + result`
   - ErrorHooks (Continue/Continue), fake_tool returns `errors.New("x")` → expect `use + error`
   - RejectHooks (PreStop), fake_tool unused → expect `rejected`
   - PostErrHooks (Continue/Stop), fake_tool returns nil → expect `use + error`
3. Subscribe to each run's channel BEFORE calling `Run`
4. Assert exact event sequence + message-content checks (no raw err text, correct Chinese friendlyReason)

---

## 12. Spec → Plan Mapping (preview)

S3 plan will decompose this spec into ≤ 12 M-tasks:

| M-Task | Files | LOC est. | Parallel-Tier (S4 disjoint) |
|---|---|---|---|
| M1 event.go + test | narration/event.go, narration/event_test.go | ~110 | Tier 3 (with M2) |
| M2 error_translate.go + test | narration/error_translate.go, ..._test.go | ~150 | Tier 3 (with M1) |
| M3 display.go + test | narration/display.go, ..._test.go | ~380 | Tier 3 (with M4) |
| M4 streamer.go + test | narration/streamer.go, ..._test.go | ~380 | Tier 3 (with M3) |
| M5 translator.go + test | narration/translator.go, ..._test.go | ~260 | depends on M1/M2/M3 (serial after) |
| M6 provider.go + test | narration/provider.go, ..._test.go | ~260 | depends on M3/M4/M5 |
| M7 configs/tool-display.yaml | configs/tool-display.yaml | ~80 | independent |
| M8 hooks.go field | biz/agent/hooks.go + hooks_test.go | ~30 | independent of M1-M7 |
| M9 adapter emit sites | biz/agent/adapter_full_to_eino.go + test | ~160 | depends on M6 + M8 |
| M10 runner option + defer | biz/agent/runner.go + test | ~80 | depends on M6 + M8 |
| M11 biz.go wire | biz/biz.go | ~10 | depends on M6 + M9 + M10 |
| M12 S5 acceptance run | docs/superpowers/qa/...-s5-acceptance.md | ~150 | depends on M1-M11 |

S3 plan will produce file-ownership tables for each Tier 3 wave + the `ndf-check-disjoint.sh` invocations.

---

## 12.5 S2-Locked Decisions

- **S2-D1** (S2 P0-3): every `template.New(...)` chain must include `.Option("missingkey=zero")` to make `{{ .input.missing }}` evaluate to empty string instead of literal `<no value>`. Without this, the `default` template func can't tell missing keys from empty values, and yaml templates referencing optional input fields would render broken text to learners.
- **S2-D2** (S2 P1-1): `Provider.Subscribe(runID)` on a runID that has already been `CloseRun`-d returns a **new open channel** (lazy-create semantic). The channel will never receive events (the Run is dead). Document this in `Subscribe` godoc; this matches the simplest implementation (no tombstone tracking) and is functionally equivalent to "the subscriber missed the events." #11 can layer a presence check via `Provider.IsRunActive(runID)` in a follow-up minor (not v1 scope).
- **S2-D3** (S2 P1-2): in `runner.Run`, the narration field attachment (`effectiveHooks.NarrationProvider = ...`) MUST come AFTER the existing Registry auto-inject block, preserving the invariant "all RunHooks fields wired before any tool call can fire."
- **S2-D4** (S2 P2-3): `agentToolNames(registry)` is called at `NewBiz` time; if registry is empty (lazy load not yet run), `ValidateToolNames` reports 0 missing names. This is best-effort startup observability, not a correctness gate. Operator runbook: if narration falls back to `defaults` for known tools, check registry-load timing first.
- **S2-D5**: `configs/tool-display.yaml` path is hardcoded relative to process CWD. numind-server boot CWD is the repo root (per deploy scripts and existing `config_dev.yaml` relative path). Spec accepts this fragility for v1; #10 may introduce a configurable narration template path.

## 13. Done Criteria (S5 gate)

- `go test ./internal/numind/biz/narration/... -race -cover` → ≥ 80% biz/narration package coverage; display.go + error_translate.go ≥ 95%
- `go test ./internal/numind/biz/agent/... -race` → all PASS (no regression on adapter/runner/hooks test baselines)
- `go vet ./...` clean
- `configs/tool-display.yaml` present, valid; `biz.NewBiz` succeeds at boot
- 4 integration fixtures in adapter_full_to_eino_test.go all PASS
- S5 acceptance trace recorded with all 4 emit-path counts matching expected
- 0 modification to `config_prod.yaml`, `migrations/`, `credit_*` tables
- 0 push to GitHub on `feature/*` branch (pre-push hook holds)
- 0 `git tag v*`, 0 `/deploy-prod` invocation
- manifest `progress.total_tasks == completed_tasks == reviewed_tasks` (12 == 12 == 12)
