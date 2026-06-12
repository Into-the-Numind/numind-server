# Requirement Card — tool-soft-error-sweep

- **ID**: tool-soft-error-sweep
- **Track**: Standard（影响文件 >3；无 DB schema / 新 API / 新外部服务；非支付权限逻辑）
- **Date**: 2026-06-12
- **起因（Bug-from-Customer，Rule 11 适用）**: 用户在 dev 实测定位调研助手（agent 100008），多个 run 被工具硬错误杀死。

## 事故证据（dev 库 agent_run）

| run | 时间 | 死因（terminal_metadata.error_detail） |
|---|---|---|
| 136 | 06-11 22:18 | `web_fetch: json: cannot unmarshal bool into Go struct field webFetchInput.prompt of type string` → NodeRunError 杀 run |
| 137 | 06-11 22:37 | `image_gen: prompt is required for image generation` → NodeRunError 杀 run |
| 132 | 06-11 13:40 | `web_search: cannot unmarshal string into ... max_results`（已修，ad007b10） |
| 133 | 06-11 18:37 | `ask_user_question: unexpected end of JSON input`（已修，816a43fe） |

背景：deepseek-v4-pro 路由切到 aihubmix 第三方部署后模型频繁产出烂参数（bool/string 类型错、缺字段、截断、空 `{}`）。路由已切回 dmxapi-guan 止血，但**任何 provider 都可能偶发烂参数**，工具层必须容错。

## 问题本质

Eino v0.8.13 无 tool-error→tool-message 钩子：工具 Execute 返回非 nil Go error → NodeRunError → **整个 agent run 终止**（用户看到"服务暂时不可用"）。对照组证据：已加固的 web_search/ask_user_question 在 run 145/143 收到烂参数后软错误回传，**模型自我纠正、run 存活**。

业界基准（Claude Code 源码实证，2026-06-12 调查）：所有工具参数校验失败一律包成 `is_error: true` 的 tool_result 喂回模型（toolExecution.ts:614-679），**任何参数错误都不会终止会话**。

## 需求

internal/numind/biz/agent/ 下所有 agent 工具：**凡是模型输入导致的错误（unmarshal 失败、缺字段、越界、空值）以及可恢复的运行期错误（检索失败、上传失败、存储读写失败），一律返回 soft error（ToolResult + nil Go error），让模型看到错误并自我纠正**。仅 context 取消与 yield 暂停机制保留非 nil error 语义。

## 验收标准

- AC1: 复现测试——bool prompt 打 web_fetch、缺 prompt / bool prompt 打 image_gen，Execute 返回 (ToolResult 含 error 字段, nil)，run 不死（先 RED 后 GREEN，测试永久保留）
- AC2: 修复清单内全部工具的输入错误路径返回 soft error
- AC3: `go test ./...` 全仓 0 FAIL + `task lint` 0 issue
- AC4: 既有已加固工具（web_search/ask_user_question/run_python/bash_exec/file_read/load_skill/analyze_image/annotate_image/create_png_chart 入口）行为零回归
