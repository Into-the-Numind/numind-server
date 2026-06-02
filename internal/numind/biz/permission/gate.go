package permission

import (
	"context"
	"sync"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
)

// AuditEntry 是异步 audit goroutine 消费的单条记录。
type AuditEntry struct {
	Req       PermissionRequest
	Result    PermissionResult
	LatencyMs int
}

// AuditLogger 是 audit 后端抽象。默认实现 dbAuditLogger 写 DB；测试可注入 mock。
type AuditLogger interface {
	Log(ctx context.Context, entry AuditEntry)
}

// PermissionGate 是 hook 路径调用的顶层入口。
//
// 用法：
//
//	gate := NewPermissionGate(WithStore(s), WithValidators(...))
//	defer gate.Close()
//	res := gate.Check(ctx, req)
//
// Check 同步返回 PermissionResult（验证决策）+ 异步入队审计；
// audit 失败仅 zap.Warn 不阻塞 Check。
type PermissionGate struct {
	pipeline   *PermissionPipeline
	enforce    bool
	audit      AuditLogger
	permStore  store.IAgentPermissionStore
	skillStore store.IAgentDefinitionStore

	auditChan chan AuditEntry
	auditWG   sync.WaitGroup
	closeOnce sync.Once
	closeCh   chan struct{}
	chanSize  int

	closed   bool
	closedMu sync.RWMutex
}

// Option 函数式 option。
type Option func(*PermissionGate)

// WithStore 注入 IAgentPermissionStore（用于 audit + TenantAdminRule 内部查规则）。
func WithStore(s store.IAgentPermissionStore) Option {
	return func(g *PermissionGate) { g.permStore = s }
}

// WithSkillStore 注入 IAgentDefinitionStore（用于 ToolFlag 验证；与 runner.WithSkillStore 一致）。
func WithSkillStore(s store.IAgentDefinitionStore) Option {
	return func(g *PermissionGate) { g.skillStore = s }
}

// WithValidators 覆盖默认 pipeline；biz.go wire 时通过此 option 注入 7 个 validator 实例。
func WithValidators(vs ...Validator) Option {
	return func(g *PermissionGate) { g.pipeline = NewPipeline(vs...) }
}

// WithEnforce 控制是否执行权限 pipeline，由 config agent.permission.enforce 驱动（默认 true）。
//
//	true（默认）  — 所有环境（含 dev/prod）跑真实 validator pipeline
//	false        — 显式全局 override：force-allow 所有工具调用
//
// enforce=false 是高危逃生舱，仅用于受控排障：NewPermissionGate 构造时会 loud-warn，
// 且每次放行都进 audit 落库可追溯。这取代了旧的 flag.Lookup("test.v") 环境嗅探后门
// （commit 14754a39）——权限策略不再耦合“是否在跑测试”。
func WithEnforce(enforce bool) Option {
	return func(g *PermissionGate) { g.enforce = enforce }
}

// WithAuditChannelSize 设置 buffered channel 容量（默认 1024）。
func WithAuditChannelSize(n int) Option {
	return func(g *PermissionGate) { g.chanSize = n }
}

// WithAuditLogger 注入自定义 AuditLogger（测试 mock 用）。
func WithAuditLogger(l AuditLogger) Option {
	return func(g *PermissionGate) { g.audit = l }
}

// NewPermissionGate 构造 gate 并启动 audit drainer goroutine。
func NewPermissionGate(opts ...Option) *PermissionGate {
	g := &PermissionGate{
		chanSize: 1024,
		closeCh:  make(chan struct{}),
		enforce:  true, // 默认 enforce：所有环境跑真实 pipeline；仅显式 WithEnforce(false) 全局放行。
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.pipeline == nil {
		g.pipeline = NewPipeline()
	}
	if g.audit == nil && g.permStore != nil {
		g.audit = newDBAuditLogger(g.permStore)
	}
	if !g.enforce {
		log.Warnw("PermissionGate: global enforcement DISABLED (agent.permission.enforce=false) — ALL agent tool calls will be force-allowed. Unsafe; intended only for controlled debugging.")
	}
	g.auditChan = make(chan AuditEntry, g.chanSize)
	if g.audit != nil {
		g.auditWG.Add(1)
		go g.drainAudit()
	}
	return g
}

// Check 同步执行 pipeline，返回决策；末尾异步入队审计（不阻塞）。
//
// enforce=true（默认，由 config agent.permission.enforce 驱动）在所有环境跑真实 pipeline；
// enforce=false 是显式全局 override，force-allow 所有工具调用（见 WithEnforce）。
//
// Close 后调 Check：跳过 channel 直接 zap.Warn 落地，确保不 send 到已关闭路径。
// 这是允许的 trade-off — 残留 in-flight 审计可能丢失，文档化于 Close 注释。
func (g *PermissionGate) Check(ctx context.Context, req PermissionRequest) PermissionResult {
	start := time.Now()
	var result PermissionResult
	if g.enforce {
		// 正常路径（所有环境，含 dev/prod）：执行真实 validator pipeline。
		result = g.pipeline.Check(ctx, req)
	} else {
		// 显式全局 override（agent.permission.enforce=false）：放行所有工具调用。
		// 高危逃生舱，仅用于受控排障；构造时已 loud-warn，每次放行经下方 audit 落库可追溯。
		result = PermissionResult{
			Behavior:       BehaviorAllow,
			DecisionReason: DecisionReasonOther,
			ValidatorID:    "ForceAllowAllGate",
			Message:        "Force allowed by explicit config override (agent.permission.enforce=false)",
		}
	}
	latency := int(time.Since(start) / time.Millisecond)

	g.closedMu.RLock()
	closed := g.closed
	g.closedMu.RUnlock()
	if closed {
		toolName := ""
		if req.Tool != nil {
			toolName = req.Tool.Name()
		}
		log.Warnw("PermissionGate.Check after Close: audit dropped",
			"agent_run_id", req.AgentRunID,
			"tool", toolName,
			"behavior", result.Behavior)
		return result
	}

	if g.audit != nil {
		entry := AuditEntry{Req: req, Result: result, LatencyMs: latency}
		select {
		case g.auditChan <- entry:
		default:
			toolName := ""
			if req.Tool != nil {
				toolName = req.Tool.Name()
			}
			log.Warnw("PermissionGate.Check: audit channel full, dropping entry",
				"agent_run_id", req.AgentRunID,
				"tool", toolName,
				"behavior", result.Behavior)
		}
	}
	return result
}

// drainAudit goroutine — 消费 channel + 调 AuditLogger.Log。
//
// 接收 closeCh 后进入 drain-and-exit 阶段：先消费 channel 中所有残留 entries 再退出；
// drain 期间不接受新 entry（Check 已标 closed=true 走 warn 路径）。
func (g *PermissionGate) drainAudit() {
	defer g.auditWG.Done()
	for {
		select {
		case entry := <-g.auditChan:
			g.audit.Log(context.Background(), entry)
		case <-g.closeCh:
			for {
				select {
				case entry := <-g.auditChan:
					g.audit.Log(context.Background(), entry)
				default:
					return
				}
			}
		}
	}
}

// Close 优雅停止 audit goroutine。
//
//	语义：
//	  1. 标记 closed=true（新 Check 改为 warn 不进 channel）
//	  2. close(closeCh) 触发 drainer 进入 drain-and-exit 分支
//	  3. WaitGroup.Wait() 阻塞至 drainer 退出；5s 超时强制返回（避免卡死 shutdown）
//
//	已知 close-race 语义（S2 P1 reviewer fix）：
//	  - close 与 Check 之间存在极小竞争窗口：Check goroutine 在 RLock 时读到 closed=false，
//	    继续 select 写 auditChan；此时另一线程 Close 已设 closed=true 且 close(closeCh)，
//	    drainer 进入内层 drain；若 Check 的 send 发生在内层 drain 之后则该 entry 丢失（无 warn）。
//	  - 本设计接受此 trade-off：Close 是一次性 shutdown 路径，残留 in-flight 审计允许丢失。
//	  - 运行时（非 Close）路径不丢失：buffered 1024 + warn on full 双重保护。
func (g *PermissionGate) Close() {
	g.closeOnce.Do(func() {
		g.closedMu.Lock()
		g.closed = true
		g.closedMu.Unlock()

		close(g.closeCh)

		done := make(chan struct{})
		go func() {
			g.auditWG.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Warnw("PermissionGate.Close: drain timed out after 5s, residual audit entries dropped")
		}
	})
}
