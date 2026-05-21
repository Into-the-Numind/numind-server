package compliance

import (
	"context"
	"fmt"
	"sync/atomic"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// AuditLogger — 异步审计日志写入器（buffered channel + Start/Stop + flush-on-shutdown）
type AuditLogger struct {
	ch      chan *model.ComplianceAuditLog
	store   store.IComplianceStore
	stopCh  chan struct{}
	doneCh  chan struct{}
	dropCnt atomic.Uint64
}

const (
	auditChanCap = 1000
	// DropCountWarnThreshold is the drop count at which a single Warn log is
	// emitted (A9 log-based observability). Chosen to fire exactly once to
	// avoid log spam — the check is newCount == threshold, not >=.
	DropCountWarnThreshold uint64 = 10
)

// NewAuditLogger 构造但不启动 consumer；调用方须显式 Start()
func NewAuditLogger(s store.IComplianceStore) *AuditLogger {
	return &AuditLogger{
		ch:     make(chan *model.ComplianceAuditLog, auditChanCap),
		store:  s,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start 启动 consumer goroutine（biz.Init 阶段调用，每进程一次）
func (l *AuditLogger) Start() { go l.consumer() }

// Stop 优雅停机：close stopCh → consumer 排空 ch 剩余 entries → 返回
func (l *AuditLogger) Stop(ctx context.Context) error {
	close(l.stopCh)
	select {
	case <-l.doneCh:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("audit logger stop timeout: drop=%d", l.dropCnt.Load())
	}
}

// Write 非阻塞入队；队满即丢 + 计数 + warn
func (l *AuditLogger) Write(entry *model.ComplianceAuditLog) {
	if l == nil || entry == nil {
		return
	}
	select {
	case l.ch <- entry:
		// 入队成功
	default:
		newCount := l.dropCnt.Add(1)
		log.Warnw("compliance audit log queue full, dropping entry",
			"rule_layer", entry.RuleLayer, "decision", entry.Decision,
			"drop_total", newCount)
		// A9 log-based observability: emit exactly once when threshold is crossed
		// (newCount == threshold, not >=, to prevent log spam on every subsequent drop).
		if newCount == DropCountWarnThreshold {
			log.Warnw("compliance audit drop count exceeded threshold",
				"drop_count", newCount,
				"threshold", DropCountWarnThreshold)
		}
	}
}

// DropCount — 可观测性
func (l *AuditLogger) DropCount() uint64 { return l.dropCnt.Load() }

func (l *AuditLogger) consumer() {
	defer close(l.doneCh)
	bg := context.Background()
	for {
		select {
		case <-l.stopCh:
			for {
				select {
				case entry := <-l.ch:
					_ = l.store.WriteAuditLog(bg, entry)
				default:
					return
				}
			}
		case entry := <-l.ch:
			_ = l.store.WriteAuditLog(bg, entry)
		}
	}
}
