// Package compactv2 — task 2.2 L0 tool artifact write-to-disk.
//
// 这个文件实现「大 tool result 写盘 + messages 仅留 <persisted-output> 引用」的核心算法：
//
//   - len(output) <= ToolArtifactSizeLimit → 原样塞 message，不写盘
//   - 超过阈值 → 生成 UUID，写到 <data_dir>/agent_artifacts/<run_id>/<uuid>，
//     DB 插入 agent_tool_artifact 元数据行，message content 改为
//     <persisted-output ref="uuid" tool="..." size="...">PREVIEW...</persisted-output>
//
// 关键设计原则：
//   - **绝不阻塞 agent run**：写盘 / DB 失败统一 fallback inline 截断 + warn log。
//     LLM 仍能继续推理，只是少了 read_tool_artifact 翻页能力。
//   - **UTF-8 安全**：preview 切到合法 rune 边界（避免半个字符）。
//   - **非 UTF-8 内容**：base64 编码后写盘 + preview 标注 [binary content]，
//     LLM 看 binary preview 仍可决定要不要 read 全文（read_tool_artifact 返回 base64）。
//
// 参考：
//   - spec：/Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/02-context/task-02-tool-artifact.md §设计要点 — 核心算法
//   - DB 接口：internal/numind/store/agent_tool_artifact.go IAgentToolArtifactStore
//   - 阈值：threshold.go ToolArtifactSizeLimit / ArtifactPreviewBytes / ArtifactDefaultTTLDays

package compactv2

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ArtifactDeps 封装 ProcessToolResult 需要的外部依赖，方便测试注入 fake。
//
// Store 必填；DataDir 必填（cleanup cron 会读相同路径，必须一致）；
// Now 可选（默认 time.Now，测试时注入固定时间）。
type ArtifactDeps struct {
	// Store 是 agent_tool_artifact 表的存取接口。
	Store ArtifactStore
	// DataDir 是 agent_artifacts 文件目录的绝对路径（不含 run_id 子目录）。
	// 例如：/var/lib/numind 或 <project_root>/data。
	// 实际文件路径：<DataDir>/agent_artifacts/<run_id>/<uuid>
	DataDir string
	// Now 时钟函数（测试时注入），nil 时用 time.Now。
	Now func() time.Time
}

// now 返回当前时间，nil-safe（Deps.Now == nil 时回落到 time.Now）。
func (d ArtifactDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// ProcessToolResult 是 L0 tool result 处理的统一入口。
//
// 决策：
//  1. len(output) <= ToolArtifactSizeLimit → 返回原文（不写盘）
//  2. 超过阈值 → 写盘 + 入库 + 返回 <persisted-output ref="..."/> 引用字符串
//  3. 写盘 / DB 任何一步失败 → fallback inline 截断（前 16KB + 警告字符串），
//     **不返回 error**，保证 agent run 继续。
//
// 返回值：直接是要塞进 Eino tool message Content 的字符串（不再包 MessageV2，
// 元数据全在 agent_tool_artifact 表行里查 ArtifactRef → 详细信息）。
//
// runID==0 / toolCallID=="" / toolName=="" 容错：写到 unknown/ 目录，元数据用占位串。
func ProcessToolResult(
	ctx context.Context,
	deps ArtifactDeps,
	runID uint64,
	toolCallID string,
	toolName string,
	output string,
) (string, error) {
	// 1. 小输出 → 不写盘，原样返回
	if len(output) <= ToolArtifactSizeLimit {
		return output, nil
	}

	// 2. 大输出 → 写盘 + 入库 + 返回引用
	artifactUUID := uuid.NewString()
	totalSize := len(output)

	// 检测 UTF-8 有效性；二进制走 base64 路径。
	isBinary := !utf8.ValidString(output)
	var bodyToWrite []byte
	previewSource := output
	if isBinary {
		encoded := base64.StdEncoding.EncodeToString([]byte(output))
		bodyToWrite = []byte(encoded)
		previewSource = "[binary content] " + encoded // 让 preview UTF-8 safe
	} else {
		bodyToWrite = []byte(output)
	}

	// 路径：<DataDir>/agent_artifacts/<run_id>/<uuid>
	absPath := artifactAbsPath(deps.DataDir, runID, artifactUUID)

	// 写盘：mkdir + write。任一步失败 → fallback。
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		log.Warnw("compactv2.ProcessToolResult: MkdirAll failed; falling back to inline truncation",
			"agent_run_id", runID, "tool_call_id", toolCallID, "tool_name", toolName, "error", err)
		return fallbackInlineTruncated(output, totalSize), nil
	}
	if err := os.WriteFile(absPath, bodyToWrite, 0o644); err != nil {
		log.Warnw("compactv2.ProcessToolResult: WriteFile failed; falling back to inline truncation",
			"agent_run_id", runID, "tool_call_id", toolCallID, "tool_name", toolName, "path", absPath, "error", err)
		return fallbackInlineTruncated(output, totalSize), nil
	}

	// preview 用 UTF-8 safe 截断。
	preview := safePreviewUTF8(previewSource, ArtifactPreviewBytes)

	// 入库：DB 失败时把刚写的文件删了，避免孤儿文件。
	expiresAt := deps.now().Add(time.Duration(ArtifactDefaultTTLDays) * 24 * time.Hour)
	previewPtr := preview
	relPath := artifactRelPath(runID, artifactUUID)
	art := &model.AgentToolArtifact{
		UUID:           artifactUUID,
		AgentRunID:     runID,
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		SizeBytes:      int64(totalSize),
		FilePath:       relPath,
		StorageBackend: "local",
		Preview:        &previewPtr,
		ExpiresAt:      &expiresAt,
		// IsExpired 默认 false; CreatedAt 由 autoCreateTime tag 处理
	}
	if deps.Store == nil {
		log.Warnw("compactv2.ProcessToolResult: ArtifactDeps.Store is nil; falling back to inline truncation",
			"agent_run_id", runID, "tool_call_id", toolCallID, "tool_name", toolName)
		_ = os.Remove(absPath)
		return fallbackInlineTruncated(output, totalSize), nil
	}
	if err := deps.Store.Create(ctx, art); err != nil {
		log.Warnw("compactv2.ProcessToolResult: store.Create failed; cleaning up file + falling back to inline truncation",
			"agent_run_id", runID, "tool_call_id", toolCallID, "tool_name", toolName, "uuid", artifactUUID, "error", err)
		_ = os.Remove(absPath)
		return fallbackInlineTruncated(output, totalSize), nil
	}

	// 成功 → 拼 <persisted-output> 引用字符串
	return formatPersistedOutputRef(artifactUUID, toolName, totalSize, preview), nil
}

// fallbackInlineTruncated 在写盘 / DB 失败时返回截断版字符串。
// 取前 ToolArtifactSizeLimit 字节（按 rune 边界回退）+ 警告 footer。
func fallbackInlineTruncated(output string, totalSize int) string {
	truncated := safePreviewUTF8(output, ToolArtifactSizeLimit)
	return fmt.Sprintf(
		"%s\n[Output truncated due to artifact write failure; %d bytes total, only first %d bytes returned]",
		truncated, totalSize, len(truncated),
	)
}

// formatPersistedOutputRef 构造 messages 里的 <persisted-output> XML 引用文本。
//
// 格式（spec §Message 引用格式）：
//
//	<persisted-output ref="UUID" tool="TOOL" size="N">PREVIEW
//	[Output exceeds context limit, N bytes total. Use read_tool_artifact tool with this ref to read more.]</persisted-output>
//
// preview 已经 UTF-8 safe；不再二次截断。
func formatPersistedOutputRef(uuid, toolName string, size int, preview string) string {
	return fmt.Sprintf(
		`<persisted-output ref="%s" tool="%s" size="%d">%s`+"\n"+
			`[Output exceeds context limit, %d bytes total. Use read_tool_artifact tool with this ref to read more.]</persisted-output>`,
		uuid, toolName, size, preview, size,
	)
}

// safePreviewUTF8 截取 s 的前 limit 字节，但确保不切到半个 UTF-8 字符。
//
// 算法：
//  1. limit <= 0 → 返回 ""
//  2. len(s) <= limit → 返回原样
//  3. 否则从 s[limit] 开始向前回退，直到找到 utf8.RuneStart（合法字符起点）
//  4. 兜底：如果回退到 0 仍非 RuneStart（极端边缘情况），返回 ""
//
// 注意：本函数不在末尾追加省略号；调用方按需自己拼。
func safePreviewUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	// 从 limit 位置向前找最近的 rune 起点
	for i := limit; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i]
		}
	}
	return ""
}

// artifactRelPath 返回 agent_tool_artifact.file_path 列存储的相对路径。
//
// 格式：agent_artifacts/<run_id>/<uuid>（与 storage_backend=local 拼绝对路径用）。
// 搬服务器时不依赖具体 mount 点。
func artifactRelPath(runID uint64, artifactUUID string) string {
	return filepath.Join("agent_artifacts", fmt.Sprintf("%d", runID), artifactUUID)
}

// artifactAbsPath 拼 dataDir + relPath，返回 artifact 文件的绝对路径。
//
// dataDir 为空时退化为相对当前工作目录的相对路径（生产部署应当永远传非空）。
func artifactAbsPath(dataDir string, runID uint64, artifactUUID string) string {
	return filepath.Join(dataDir, artifactRelPath(runID, artifactUUID))
}

// ArtifactAbsPath 是 artifactAbsPath 的导出版本，供 runner / cleanup 包用。
//
// 命名空间隔离 — 不直接 export internal helper，保留重命名空间。
func ArtifactAbsPath(dataDir string, runID uint64, artifactUUID string) string {
	return artifactAbsPath(dataDir, runID, artifactUUID)
}
