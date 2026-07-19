# QA Report — 飞书技能读取容错与平台分页

## 验证环境

- 后端：本地 Standard feature worktree，Go 单元/集成/race/coverage 使用受控 fake `lark-cli`
- 前端：N/A（本次没有前端、HTTP API 或 SSE 协议改动；既有 `recoverable:true` 会继续显示中性进度）
- 浏览器：N/A

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="/Users/zhiyuchen/go/bin:$PATH" task lint` | PASS | `go vet` 与 `golangci-lint` 均通过 |
| Go 全仓门禁 | `GOCACHE=/private/tmp/numind-go-build task test` | PASS | 普通、全仓 race、coverage 三阶段及 HTML coverage 生成全部退出 0 |
| Go 全仓普通测试 | `GOCACHE=/private/tmp/numind-go-build go test ./...` | PASS | 全仓通过 |
| Go 全仓 race 复核 | `GOCACHE=/private/tmp/numind-go-build go test -tags sqlite_fts5 -race ./...` | PASS | 全仓通过，无 data race |
| Sandbox fixture race 回归 | `go test -tags sqlite_fts5 -race ./internal/numind/biz/sandbox -count=3` | PASS | 连续三轮通过；修复只改变测试夹具，生产代码不变 |
| Agent focused | `go test ./internal/numind/biz/agent -run 'TestLarkPersonalWorkspace_SkillRead|TestBoundedAtomicSkillTool' -count=1` | PASS | 参数兼容、自动分页、完整信封上限与错误语义通过 |
| Feishu focused | `go test ./internal/numind/biz/feishu -run 'TestSkillReader' -count=1` | PASS | Dev run 227、声明内引用、安全拒绝与真实 cursor 兼容通过 |
| Vue/Admin/E2E | N/A | N/A | 没有前端、管理端或浏览器界面改动 |

仅出现仓库既有的 macOS SQLite deprecated linker/compiler warning，不影响结果。

## 客户 RED 与回归保护

- 首个代码 commit `492a9818` 按客户 Dev run 227 复现：模型把精确 Markdown reference 放入 `cursor` 时被拒绝；新 schema 仍暴露 cursor；首个无效技能输入被错误分类为终止失败。
- `cfb5deef` 补齐审查发现的三项边界：wrapped sentinel 必须继续由 `errors.Is` 识别、超大 `References` 必须按完整 JSON 信封 fail closed、V2 原子 wrapper 必须具备独立 64 KiB 第二防线。
- 全仓门禁暴露的历史 sandbox fixture race 与本功能无关；`1f8f7a0e` 将 `SkillsRoot` 固定在 worker 启动前，纯测试改动，连续三轮 race 与最终全仓 race 均通过。

## 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| 保留 LLM 对 Docs/Base/Wiki/Drive 业务操作的判断 | PASS | LLM 继续读取官方技能并选择业务命令；平台没有接管意图规划 |
| 新模型不再填写内部分页 cursor | PASS | `lark_skill_read` schema 只暴露 `skill` 与可选 `reference`；旧 cursor wire 输入仍兼容滚动发布 |
| 平台自动读取完整技能说明 | PASS | 最多两页，平台内部续页，模型只收到一次完整结果 |
| 精确 reference 放错字段时可安全修正 | PASS | 先识别合法签名 cursor；仅修正安全 `.md` 形状，随后仍在当前技能声明集合内解析 |
| 不稳定或恶意分页 fail closed | PASS | Skill/Path/References 必须逐页完全一致；重复 cursor、第三页、第二页失败均不返回部分正文 |
| 完整模型可见信封受限 | PASS | 正文、Hosted Policy、References 等完整 JSON 总计不超过 64 KiB，tool 与 wrapper 两层独立防线 |
| 可修正首错不再显示红色执行失败 | PASS | 首次 `ErrSkillReadInvalid`（含 wrapped error）返回 `invalid_skill_input`、`recoverable:true`；依赖失败仍保持终止错误 |
| 安全边界不被扩大 | PASS | 五个官方技能、当前技能声明 reference、受控 CLI、当前用户身份、Docs/Base/Wiki/Drive 命令策略全部保持不变 |
| 双独立审查 | PASS | 规格与代码质量审查最终均 PASS，P0/P1/P2=0 |

## 可观测性与浏览器 QA

- N/A：本次不新增 LLM/provider 调用、HTTP 路由、数据库、trace、前端状态或外部服务。
- Dev 部署后使用真实 Agent 读取既有飞书文档，验证最终业务链路与用户可见状态。

## Dev 验收提示词

`请读取飞书文档「有数飞书二次连接测试」，告诉我文档标题和完整正文。不要创建或修改任何飞书内容。`

预期：Agent 可自行读取 Drive/Docs 技能并完成标题定位与读取；技能 reference/cursor 修正不出现红色“执行出错”。真实连接或权限缺失仍应按既有流程显示授权卡片。

## 结论

`ALL_PASS`。允许进入 S6 原子合并、推送和 Dev 部署；Prod 不在本次范围内。
