// Package compactv2 — task 2.2 artifact cleanup cron。
//
// 每日 03:00（北京时间）扫描 expires_at < now AND is_expired = false 的 artifact，
// 物理删文件 + DB 标 is_expired=true（不删 row，保留审计）。
//
// 设计取舍：
//   - **MarkExpired 不删 row**：保留 DB 行让运营 SQL 查"过去 30 天产出了多少 artifact"
//     这类统计；同时 read_tool_artifact 命中已标 expired 的 row 可以返回明确的
//     "expired" 错误（而不是 not found）。
//   - **物理删文件 vs DB row**：物理删文件释放磁盘 → 主要诉求；保留 row 几十字节
//     vs 文件可能 MB 级，性价比明显。
//   - **batch 限流**：单次 cron 最多 ArtifactCleanupBatchSize=10000 条，
//     剩余的留待下一轮（每日 cron，最坏情况积压一天 24h）。生产规模允许这种节奏。
//
// 注册时机：runtime 启动时 spawn goroutine 用 time.Ticker(24h) 触发。
// （robfig/cron 也可，但 server.go 现有 cron 都用 time.Ticker 简易模式；
// 保持一致。）
//
// 时区：当前实现是"启动后每隔 24h 跑一次"，第一次 run 取决于服务器启动时间。
// 如果要求严格 03:00 UTC+8 触发，需要算 sleep until next 03:00，本 task 不做。
// follow-up：见 numind-server/follow-ups/ 里记 TODO。

package compactv2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"numind-server/internal/pkg/log"
)

// CleanupMetrics 是一次 RunArtifactCleanup 调用的统计结果，方便 caller log。
type CleanupMetrics struct {
	Processed     int // 扫描到的待清理总数（<= BatchSize）
	FilesDeleted  int // 成功删除文件数（包括 ENOENT — 文件已不存在也视为成功）
	FileErrors    int // 删文件失败（非 ENOENT）的数量
	MarkedExpired int // 成功 MarkExpired 数
	MarkErrors    int // MarkExpired 失败数
}

// RunArtifactCleanup 扫描并清理过期 artifact。
//
// 行为：
//  1. ListExpiredBefore(now, BatchSize) 取一批
//  2. 对每条：os.Remove(absolutePath)（ENOENT 视为成功），MarkExpired(uuid)
//  3. 返回 CleanupMetrics + 整体错误（任何一条致命错才返回 err；单条文件 / DB 失败只 warn log）
//
// 调用方：
//   - cron 注册的 goroutine（每日 03:00 触发）
//   - 单测：注入 mock store + 临时 dataDir
//
// dataDir 应当与 ProcessToolResult 写盘时使用的相同 — 否则 os.Remove 永远找不到文件。
func RunArtifactCleanup(
	ctx context.Context,
	s ArtifactStore,
	dataDir string,
) (CleanupMetrics, error) {
	var m CleanupMetrics
	if s == nil {
		return m, errors.New("RunArtifactCleanup: store is nil")
	}
	now := time.Now()
	expired, err := s.ListExpiredBefore(ctx, now, ArtifactCleanupBatchSize)
	if err != nil {
		return m, fmt.Errorf("RunArtifactCleanup ListExpiredBefore: %w", err)
	}
	m.Processed = len(expired)
	if m.Processed == 0 {
		log.Infow("agent_artifact_cleanup: no expired artifacts", "scanned_at", now)
		return m, nil
	}
	for _, art := range expired {
		absPath := ArtifactAbsPath(dataDir, art.AgentRunID, art.UUID)
		rmErr := os.Remove(absPath)
		switch {
		case rmErr == nil:
			m.FilesDeleted++
		case os.IsNotExist(rmErr):
			// 文件已不存在 — 可能上轮 cron 跑过一半 / 手动 rm — 视为成功
			m.FilesDeleted++
		default:
			m.FileErrors++
			log.Warnw("agent_artifact_cleanup: os.Remove failed (continuing)",
				"uuid", art.UUID, "path", absPath, "error", rmErr)
		}
		// 不管文件删成功还是失败（除 ENOENT 外），都要 MarkExpired 以防下一轮 cron 反复尝试同一行。
		// 即使本次 os.Remove 失败，下次 ProcessToolResult / read 命中 is_expired=true 会自动按"不可用"处理。
		if mErr := s.MarkExpired(ctx, art.UUID); mErr != nil {
			m.MarkErrors++
			log.Warnw("agent_artifact_cleanup: MarkExpired failed",
				"uuid", art.UUID, "error", mErr)
		} else {
			m.MarkedExpired++
		}
	}
	log.Infow("agent_artifact_cleanup: batch done",
		"processed", m.Processed,
		"files_deleted", m.FilesDeleted,
		"file_errors", m.FileErrors,
		"marked_expired", m.MarkedExpired,
		"mark_errors", m.MarkErrors,
		"scanned_at", now,
	)
	return m, nil
}
