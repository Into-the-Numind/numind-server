# QA Report — 飞书技能引用简称安全解析

## 验证环境

- 后端：本地 feature worktree，Go 单元/集成测试使用受控 fake `lark-cli`
- 前端：N/A（本次没有前端或 HTTP API 改动）
- 浏览器：N/A

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `PATH="$(go env GOPATH)/bin:$PATH" task lint` | PASS | `go vet` 与 `golangci-lint` 均通过；首次执行仅因本机 PATH 未包含 Go bin 而找不到刚安装的 lint 二进制 |
| Go test | `go test ./... -count=1` | PASS | 全仓普通测试通过 |
| 目标重复测试 | `go test ./internal/numind/biz/feishu -run 'TestSkillReader_(ResolvesDeclaredReferenceBasename|ReferenceBasenameResolutionFailsClosed|ReferenceBasenameCursorBindsCanonicalResource|InvalidRequestDoesNotStartCLI)' -count=20` | PASS | 简称、歧义、cursor 与非法输入重复验证 |
| 目标 race | `go test -race ./internal/numind/biz/feishu -run 'TestSkillReader|TestDeclaredSkillReferences' -count=1` | PASS | 本次改动范围无 race |
| 仓库任务 | `task test` | KNOWN_FAILURE | 普通阶段通过；全仓 race 仅命中已登记的 `internal/numind/biz/sandbox/pool_skill_test.go` 测试夹具竞争。该目录未被本分支修改，飞书包在同轮 race 中通过 |
| Vue/Admin/E2E | N/A | N/A | 无前端、管理端或路由变更 |

## 范围内验收

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| Agent 可用一个安全简称读取当前技能声明的参考文件 | PASS | 简称唯一命中后规范化为原有 `references/...` 标准路径再调用受控 CLI |
| 不要求 Agent 知道或拼写完整内部引用地址 | PASS | 允许单个 ASCII basename；标准路径继续兼容 |
| 不扩大当前技能声明白名单 | PASS | 唯一命中仍必须属于受 64 项/16 KiB 限制的公开引用集合 |
| 零命中、跨技能、同名歧义均 fail closed | PASS | 完整声明内容参与歧义检测；不泄露候选路径 |
| 路径穿越、绝对路径、反斜杠、NUL、Unicode、超长输入被拒绝 | PASS | 非法请求在启动 CLI 前拒绝 |
| cursor 继续绑定标准化后的同一资源 | PASS | 简称与其标准路径可续页，其他资源或技能不能复用 |
| receipt、TTL、受控 CLI、OS 文件隔离和错误脱敏不变 | PASS | 双独立审查均为 PASS，P0/P1/P2=0 |

## 浏览器 QA

- N/A：这是后端内部技能资源解析，不改变浏览器可见 UI；真实产品验收在 Dev 部署后由 Agent 飞书任务覆盖。

## 可观测性验证

- N/A：不新增 LLM、外部网络请求、trace 或指标路径。

## 结论

范围内 `ALL_PASS`。仓库级 `task test` 的唯一失败为历史已登记且与本分支无关的 sandbox 测试夹具 race，不阻断本次安全边界修复进入 S6。
