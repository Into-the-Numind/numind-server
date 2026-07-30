# QA Report — Prod Sandbox Isolation

## 验证环境

- 后端：本地 feature worktree `/private/tmp/wt-prod-sandbox-isolation-numind-server`
- 构建机：`BUILD_SSH_HOST` 对应构建机临时目录，严格 Docker 构建验证
- 前端：N/A，本 feature 只改 `numind-server`
- 浏览器：N/A，本 feature 没有前端 UI 改动

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go lint | `GOPROXY=https://goproxy.cn,direct task lint` | PASS | 首次运行因 PATH 未包含 `$(go env GOPATH)/bin` 找不到刚安装的 `golangci-lint`；补 PATH 后通过 |
| Go test | `GOPROXY=https://goproxy.cn,direct task test` | PASS | 普通测试、race 阶段、coverage 阶段通过 |
| Sandbox capacity | `bash scripts/cicd/test-sandbox-capacity.sh` | PASS | 五并发容量/父级 ceiling 合同通过 |
| Sandbox provisioning | `bash scripts/cicd/test-sandbox-provisioning.sh` | PASS | fake-root provisioning、seccomp 安装、配置 ownership、fail-closed 检查通过 |
| Release preflight | `bash scripts/cicd/test-release-preflight.sh` | PASS | prod release 清洁 worktree、secret hygiene、broker socket/no Docker socket 合同通过 |
| Sandbox isolation | `bash scripts/cicd/test-sandbox-isolation.sh` | PASS | no Docker socket、admin disabled、config_prod 不变、镜像源合同通过 |
| Sandbox artifacts local | `bash scripts/cicd/test-sandbox-artifacts.sh` | PASS | 本地 DockerHub token 获取失败，按脚本降级为 static-only；构建机严格版已覆盖真实镜像构建 |
| Sandbox artifacts strict | `NUMIND_SANDBOX_ARTIFACTS_STRICT=1 bash scripts/cicd/test-sandbox-artifacts.sh` | PASS | 在构建机通过；生产 runtime 镜像可构建，含 Sandbox artifacts，且 `WITH_DOCKER_CLI=false` 时无 Docker CLI |
| Targeted Sandbox race | `go test -race ./internal/numind/biz/sandbox ./internal/numind/sandboxbroker ./internal/numind/sandboxreconcile -count=1` | PASS | 三个 Sandbox 相关包通过 |
| Vue lint (web-v3) | N/A | N/A | 本 feature 没有修改前端仓库 |
| Vue type-check (web-v3) | N/A | N/A | 本 feature 没有修改前端仓库 |
| Admin lint | N/A | N/A | 本 feature 没有修改管理端前端 |
| Admin type-check | N/A | N/A | 本 feature 没有修改管理端前端 |
| E2E | N/A | N/A | 浏览器产品走查留到 S6 Dev 部署后执行 |

## 浏览器 QA

- gstack /qa 输出路径：N/A
- AI 审查结论：N/A；本 feature 是后端 Sandbox/部署隔离能力，不含前端 UI 改动。S6 Dev 部署后需要真实验收 SOP、Agent、文档导出、插件/Skill、飞书连接和五并发队列。

## 可观测性验证（如功能涉及 AI/LLM 调用）

- [x] N/A：本 feature 不新增 LLM 调用，只改变 Sandbox backend 从直接 Docker 控制到 broker 控制。
- 结论：N/A

## PRD 验收标准核对

| 验收标准 | 结果 | 备注 |
|----------|------|------|
| 五并发、第六 FIFO、双 owner 全局 5 | PASS | `test-sandbox-capacity.sh` + sandboxbroker contract/race 覆盖 |
| 512MiB/1CPU/64PIDs 与父级动态 ceiling | PASS | capacity + isolation scripts 覆盖 |
| user API 不能访问两套 Docker；admin 不能访问 broker/两套 Docker | PASS | release preflight + isolation scripts 覆盖 |
| Rootless 用户不能读 Prod secrets/data/certs/uploads/main Docker | PASS | provisioning fail-closed checks 覆盖 |
| 危险 broker 字段/路径/tar/limit/慢连接拒绝 | PASS | sandboxbroker tests + race 覆盖 |
| daemon/broker/data-root/cgroup 故障时核心 API 健康或部署 fail-closed | PASS | provisioning + release preflight 覆盖 |
| journal crash/orphan/recovery/reconcile/rollback | PASS | reconcile/race/preflight 覆盖 |
| 生产镜像含 Sandbox artifacts 且不含 Docker CLI | PASS | 构建机 strict artifact verification 覆盖 |
| 不修改 Prod 配置和用户数据 | PASS | `git diff -- config_prod.yaml` 为空；未连接或写入 Prod 数据库 |

## 结论

ALL_PASS for S5 backend/Sandbox automatic verification.

S6 仍需：`ndf-done` 合入 develop、部署 Dev，并在 Dev 上做产品级真实验收：SOP 运行、AI 聊天机器人、AI 智能体、文档导出、插件/Skill、飞书连接、五并发 Sandbox 队列和 broker 故障降级。

## 失败项修复要求

无。
