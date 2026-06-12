# Spec — tool-soft-error-sweep

## 1. 共享 helper（新增）

文件：`internal/numind/biz/agent/tool_soft_error.go`

```go
// softToolError returns an LLM-readable error payload with a nil Go error.
// Eino v0.8.13 has no tool-error→tool-message hook: a non-nil Go error becomes
// a NodeRunError that TERMINATES the whole agent run. Input-derived and
// recoverable runtime failures must therefore be returned as a successful
// ToolResult whose JSON body carries the error, so the LLM can self-correct
// (same contract as web_search.returnSoftError / Claude Code's is_error
// tool_result). Reserve non-nil errors for ctx cancellation and yield only.
func softToolError(tool, format string, args ...any) (ToolResult, error) {
	msg := fmt.Sprintf(format, args...)
	out, _ := json.Marshal(map[string]string{"error": "ERROR: " + tool + ": " + msg})
	return ToolResult(out), nil
}
```

错误 JSON 形状与既有约定一致：`{"error": "ERROR: <tool>: <msg>"}`。

## 2. 逐文件修复规格（基于 2026-06-12 主 session 亲读源码核实）

| # | 文件 | 行为变更 | helper |
|---|---|---|---|
| 1 | tool_image_gen.go | Execute 入口：unmarshal 失败（L74-75）、prompt 空（L77-78）由 hard 改 soft | 复用文件内已有 `t.returnSoftError` |
| 2 | tool_web_fetch.go | Execute 入口：unmarshal 失败（L96-97，现 `errno.ErrBind` hard）改 soft | 复用文件内已有 `t.returnSoftError`（title 传 ""） |
| 3 | tool_create_csv.go | unmarshal（L52）、data 空（L56）、csv write/flush（L74/85/90）、上传失败改 soft | `softToolError("create_csv", ...)` |
| 4 | tool_create_json.go | unmarshal（L49）、marshal data（L67）、上传失败改 soft | `softToolError("create_json", ...)` |
| 5 | tool_create_html.go | unmarshal（L80）、renderHTML 错误（L90，模板 parse/render 系输入驱动）、上传失败（L93）改 soft | `softToolError("create_html", ...)` |
| 6 | tool_create_text.go | unmarshal（L47）、上传失败（L56）改 soft；content 校验如有同改 | `softToolError("create_text", ...)` |
| 7 | tool_create_png_chart.go | 仅上传失败路径 `return chartFriendlyError(...), err` 改为 `, nil`（入口已 soft） | 既有 `chartFriendlyError` |
| 8 | tool_kb_search.go | unmarshal（L87）、Retrieve 失败（L111）改 soft；输出 marshal（L127）为内部不变量可保留 hard | `softToolError("kb_search", ...)` |

> **ctx.Err 澄清（S3 reviewer P1）**：ctx 取消**不在工具层检测**——Eino 框架在 ToolsNode 入口处理 ctx 取消，runner 的 yield 检测走 `errors.As(runErr, &yieldErr)` 独立通道。工具层对 Retrieve/store/upload 等可恢复错误**一律无条件 soft**（与 web_search L243-245 的既有模式一致），禁止实现时自作主张加 `errors.Is(err, context.Canceled)` 之类的 hard 分支。
| 9 | tool_memory_write.go | unmarshal（L68）、ErrMemoryUserRequired（L73）、store 写失败（L88）改 soft（user required 注明"系统未注入用户"措辞） | `softToolError("memory_write", ...)` |
| 10 | tool_memory_read.go | unmarshal（L80）、user required（L90）、store 读失败（L97/105）改 soft | `softToolError("memory_read", ...)` |
| 11 | tool_document_generate.go | 三条 hard 路径（L66/69/71）全改 soft（stub 工具，误启用也不应杀 run）；"task not registered" 的 soft 消息必须含"此工具当前不可用，请勿重试"指示，防止 LLM 反复调用 stub | `softToolError("document_generate", ...)` |

## 3. 不变量（reviewer 必查）

- I1: 所有改动路径返回 `(ToolResult 非 nil, nil)`；ToolResult 必须是合法 JSON
- I2: `ctx.Err()` / yield 相关路径不得被改动（grep 确认 0 触碰）
- I3: 已加固工具（web_search/ask_user_question/run_python/bash_exec/file_read/load_skill/analyze_image/annotate_image）零 diff
- I4: 错误消息含工具名前缀，便于 Langfuse/日志排查；不泄漏内部敏感信息（API key、内网地址）
- I5: image_gen 计费路径语义不变：reserve 失败已 soft，generateImage 失败仍走 refund

## 4. 测试规格

复现测试文件：`internal/numind/biz/agent/tool_soft_error_sweep_test.go`

- 表驱动：每个修复工具 × 两类输入（非法 JSON 类型错如 `{"prompt": true}`、缺必填字段如 `{}`），断言 `err == nil` 且 result JSON 含 `error` 字段
- Rule 11：该测试先于修复 commit（RED），修复后 GREEN，永久保留
- 运行期错误路径（检索/上传/存储失败）依赖外部服务的，用注入失败的轻量桩覆盖；难以注入的（COS 上传）以代码审查 + I1 静态核对兜底，不强造集成环境
