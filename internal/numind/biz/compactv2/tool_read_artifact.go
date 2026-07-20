// Package compactv2 — task 2.2 read_tool_artifact Eino 工具实现。
//
// 该工具是 V2 路径下的 system-level always-available 工具：LLM 通过它读取
// 写盘的大 tool result。Eino agent 在 ProcessToolResult 把超过 16KB 的输出替换
// 为 <persisted-output ref="UUID" ...> 后，下一轮 LLM 看到 ref 就可以调用此
// 工具按 (offset, limit) 翻页读全文。
//
// 安全规则：
//   - **ownership check**：通过 RunStore.Get(art.AgentRunID).UserID 与 ctx user 比对。
//     跨用户访问返回 "artifact not accessible"（不暴露存在性）。
//   - **expired**：is_expired=true → 返回 "[Artifact expired and content unavailable...]"
//   - **磁盘文件丢失**：自动 MarkExpired + 返回 unavailable（自愈，避免下次再读）
//   - **offset 越界**：content=""，has_more=false（不报错）
//   - **limit clamp**：>ToolArtifactReadMaxLimit 时强制 clamp；<=0 时用默认 16KB
//   - **UTF-8 切片**：用 safePreviewUTF8 兜底 rune 边界
//
// 参考 spec：/Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/02-context/task-02-tool-artifact.md
// §read_tool_artifact 工具 / §设计要点 边界 case
package compactv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"

	"numind-server/internal/pkg/log"
)

// ReadArtifactInput 是 read_tool_artifact 工具的 JSON 输入结构。
type ReadArtifactInput struct {
	ArtifactID string `json:"artifact_id"`      // 必填，artifact UUID
	Offset     int    `json:"offset,omitempty"` // 字节 offset，默认 0
	Limit      int    `json:"limit,omitempty"`  // 字节 limit，默认 16384，上限 16384
}

// ReadArtifactOutput 是 read_tool_artifact 工具的 JSON 输出结构。
type ReadArtifactOutput struct {
	Content   string `json:"content"`        // 切片后的内容
	Offset    int    `json:"offset"`         // 实际生效的 offset
	Returned  int    `json:"returned"`       // 实际返回字节数
	TotalSize int    `json:"total_size"`     // 文件总字节数
	HasMore   bool   `json:"has_more"`       // offset+returned < total_size
	ToolName  string `json:"tool_name"`      // 原 tool 名（便于 LLM 上下文）
	Note      string `json:"note,omitempty"` // 异常说明（expired / not_accessible / disk_missing 等）
}

// ReadArtifactToolName 是工具的固定名称（LLM 在 tool_calls 里看到此名）。
const ReadArtifactToolName = "read_tool_artifact"

// ReadArtifactTool 实现 Eino InvokableTool 接口；由 runner 在 V2 路径 always-inject。
type ReadArtifactTool struct {
	store         ArtifactStore
	runStore      AgentRunReader
	dataDir       string
	userIDFromCtx UserIDExtractor
}

// NewReadArtifactTool 构造一个 read_tool_artifact 工具实例。
// runStore 用于 ownership check；store 用于 artifact 元数据；dataDir 用于拼绝对路径。
// userIDFromCtx 必须由 caller 注入（typically middleware.UserIDFromCtx）——
// compactv2 不能直接 import middleware（import cycle）。nil → 一律拒绝（视为 not accessible）。
// 参数 s / rs 是结构性接口；store.IAgentToolArtifactStore / store.IAgentRunStore 自动满足。
func NewReadArtifactTool(s ArtifactStore, rs AgentRunReader, dataDir string, userIDFromCtx UserIDExtractor) *ReadArtifactTool {
	return &ReadArtifactTool{
		store:         s,
		runStore:      rs,
		dataDir:       dataDir,
		userIDFromCtx: userIDFromCtx,
	}
}

// 编译期断言：必须是 Eino InvokableTool。
var _ einotool.InvokableTool = (*ReadArtifactTool)(nil)

// Info 返回 Eino ToolInfo（LLM 看到的 schema）。
func (t *ReadArtifactTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: ReadArtifactToolName,
		Desc: "Read the full content of a persisted tool output by its UUID reference. " +
			"Use this when you see <persisted-output ref=\"UUID\" .../> in a previous tool message " +
			"and need to read more than the 1KB preview. Paginate with offset+limit; " +
			"max 16384 bytes per call. Expired artifacts (>30 days old) return an error.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"artifact_id": {
				Type:     schema.String,
				Desc:     "The UUID from <persisted-output ref=\"UUID\" .../>",
				Required: true,
			},
			"offset": {
				Type: schema.Integer,
				Desc: "Byte offset to start reading (default 0)",
			},
			"limit": {
				Type: schema.Integer,
				Desc: "Max bytes to return (default 16384, max 16384)",
			},
		}),
	}, nil
}

// InvokableRun 是 read_tool_artifact 的核心执行入口。
//
// 错误返回策略（important — 决定 LLM 上下文 vs reAct 错误处理）：
//   - 输入 JSON 无效 / artifact_id 缺失 → 返回 error（reAct 视为工具失败，让 LLM 改）
//   - artifact 暂时/永久不存在 → 返回**正常 output**（note=not_found），允许模型至多重试一次；
//     Eino 会把普通 tool error 升级成整个 run 的 model_error，因此这里不能返回 Go error
//   - artifact 过期 / 跨用户 / 磁盘丢失 → 返回**正常 output**（content 字段是说明文本）
//     这是为了让 LLM 把它当成正常 tool result 上下文里有"为什么读不到"，而不是终止整个 run。
func (t *ReadArtifactTool) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
	var in ReadArtifactInput
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return "", fmt.Errorf("read_tool_artifact: invalid input JSON: %w", err)
	}
	if in.ArtifactID == "" {
		return "", fmt.Errorf("read_tool_artifact: artifact_id is required")
	}

	// 1. 取元数据
	if t.store == nil {
		return "", fmt.Errorf("read_tool_artifact: artifact store not configured")
	}
	art, err := t.store.Get(ctx, in.ArtifactID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return marshalReadOutput(ReadArtifactOutput{
				Content: "[Artifact is not available yet. Retry this exact read once; if it remains unavailable, continue from the persisted-output preview without assuming unseen content.]",
				Note:    "not_found",
			}), nil
		}
		return "", fmt.Errorf("read_tool_artifact: store.Get failed: %w", err)
	}

	// 2. 过期检查
	if art.IsExpired {
		out := ReadArtifactOutput{
			Content:   "[Artifact expired and content unavailable, file was cleaned 30 days after creation]",
			TotalSize: int(art.SizeBytes),
			ToolName:  art.ToolName,
			Note:      "expired",
		}
		return marshalReadOutput(out), nil
	}

	// 3. ownership check：从 RunStore 拿 user_id 比对 ctx user
	//    不暴露存在性 — 跨用户 / runStore 不可用 / 未注入 userIDFromCtx 一律按 "not accessible" 处理。
	var (
		ctxUserID uint
		hasUser   bool
	)
	if t.userIDFromCtx != nil {
		ctxUserID, hasUser = t.userIDFromCtx(ctx)
	}
	if t.runStore == nil || !hasUser || ctxUserID == 0 {
		log.Warnw("read_tool_artifact: missing runStore or ctx user; rejecting as not accessible",
			"uuid", in.ArtifactID, "has_runStore", t.runStore != nil, "has_user", hasUser)
		out := ReadArtifactOutput{
			Content:  "[Artifact not accessible]",
			ToolName: art.ToolName,
			Note:     "not_accessible",
		}
		return marshalReadOutput(out), nil
	}
	run, runErr := t.runStore.Get(ctx, art.AgentRunID)
	if runErr != nil || run == nil || run.UserID != ctxUserID {
		// log 内部，对 LLM 不暴露具体原因
		log.Warnw("read_tool_artifact: ownership check failed",
			"uuid", in.ArtifactID, "ctx_user", ctxUserID,
			"run_user", func() uint {
				if run == nil {
					return 0
				}
				return run.UserID
			}(),
			"run_err", runErr,
		)
		out := ReadArtifactOutput{
			Content:  "[Artifact not accessible]",
			ToolName: art.ToolName,
			Note:     "not_accessible",
		}
		return marshalReadOutput(out), nil
	}

	// 4. 读磁盘文件；失败 → MarkExpired + 返回 unavailable
	absPath := ArtifactAbsPath(t.dataDir, art.AgentRunID, art.UUID)
	body, readErr := os.ReadFile(absPath)
	if readErr != nil {
		log.Warnw("read_tool_artifact: ReadFile failed; marking artifact expired",
			"uuid", in.ArtifactID, "path", absPath, "error", readErr)
		if mErr := t.store.MarkExpired(ctx, art.UUID); mErr != nil {
			// MarkExpired 失败只是 metric 损失，不影响本次返回。
			log.Warnw("read_tool_artifact: MarkExpired after ReadFile failure also failed",
				"uuid", in.ArtifactID, "error", mErr)
		}
		out := ReadArtifactOutput{
			Content:   "[Artifact expired and content unavailable, file was cleaned 30 days after creation]",
			TotalSize: int(art.SizeBytes),
			ToolName:  art.ToolName,
			Note:      "disk_missing",
		}
		return marshalReadOutput(out), nil
	}

	// 5. clamp + 切片
	total := len(body)
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	limit := in.Limit
	if limit <= 0 {
		limit = ToolArtifactReadMaxLimit
	}
	if limit > ToolArtifactReadMaxLimit {
		limit = ToolArtifactReadMaxLimit
	}
	// offset 超出 → 空返回
	if offset >= total {
		out := ReadArtifactOutput{
			Content:   "",
			Offset:    offset,
			Returned:  0,
			TotalSize: total,
			HasMore:   false,
			ToolName:  art.ToolName,
		}
		return marshalReadOutput(out), nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	chunk := body[offset:end]
	content := safePreviewUTF8(string(chunk), limit) // rune-safe（base64 字符不会切坏，但保留兜底）
	if content == "" && len(chunk) > 0 {
		// 极端边缘：safePreviewUTF8 兜底返回 ""；至少返回原 bytes 当 string 让 LLM 看到。
		content = string(chunk)
	}
	returned := len(content)
	out := ReadArtifactOutput{
		Content:   content,
		Offset:    offset,
		Returned:  returned,
		TotalSize: total,
		HasMore:   (offset + returned) < total,
		ToolName:  art.ToolName,
	}
	return marshalReadOutput(out), nil
}

// marshalReadOutput 把 ReadArtifactOutput marshal 成 JSON 字符串。
// JSON marshal 失败极不可能（结构都是基础类型）；fallback 兜底成简单 string。
func marshalReadOutput(out ReadArtifactOutput) string {
	b, err := json.Marshal(out)
	if err != nil {
		// 极端兜底：避免完全空返回让 LLM 困惑。
		return fmt.Sprintf(`{"content":%q,"total_size":%d,"tool_name":%q,"note":"marshal_error"}`,
			out.Content, out.TotalSize, out.ToolName)
	}
	return string(b)
}

// ReadArtifactSystemPromptAddendum 是 V2 路径下追加到 system prompt tools 段尾的固定段落。
//
// 中英双语，方便不同 LLM 后端（DeepSeek 中文偏好 / Doubao 中英混合 / qwen-plus）都能看懂。
// runner 在装配 system prompt 时把它追加到 tools_section_placeholder。
const ReadArtifactSystemPromptAddendum = `

Large tool outputs (>16KB) are automatically persisted to disk. When you
see <persisted-output ref="UUID" tool="..." size="...">PREVIEW...</persisted-output>,
the full content is available via the read_tool_artifact tool. Pass the
ref UUID and use offset/limit (max 16384 bytes per call) to paginate.
Expired artifacts (>30 days) return an error; do not assume content
remains accessible across long-term sessions.

大型工具输出（>16KB）会自动写盘到 artifact，messages 里只保留引用。当你看到
<persisted-output ref="UUID" tool="..." size="...">PREVIEW...</persisted-output>
时，使用 read_tool_artifact 工具按 (artifact_id=UUID, offset, limit) 翻页读取
全文。单次最多 16384 字节。过期的 artifact（>30 天）会返回 expired 错误，
不要在长会话中假定 artifact 永远可访问。
`
