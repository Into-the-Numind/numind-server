# ADR-0002: Sandbox 最终决策 — Docker Pool（覆盖蓝本决策 #5）

**Status**: Accepted
**Date**: 2026-05-20
**Feature**: agent-mode-phase0-verification
**Task**: Task 5 — V5 沙箱方案最终决策
**⚠️ 覆盖 architecture-v1.md Decision #5**：蓝本默认选 Daytona OSS，本 ADR 将其替换为 Docker-pool。

---

## Context

ADR-0001（Task 4）通过 SSH 实测确认：dev 服务器 `49.233.219.254` 是 Tencent Cloud 标准 CVM，未暴露嵌套 KVM（`/dev/kvm` 不存在，`/proc/cpuinfo` 的 vmx/svm 标志位为 0），Daytona OSS 的 VM 工作空间无法启动。

与此同时，dev 服务器上 Docker 可用（`Docker version 0.0.0-20241223130549-3b49deb`），容器级沙箱具备直接落地条件。

本 ADR 作为 14-feature 分解中 **#4 sandbox-integration** 的上游输入依赖，确定 Phase 0 沙箱技术选型，锁定接口边界。

---

## Options Considered

### Option A — Daytona OSS（蓝本默认，**已否决**）

| 维度 | 评估 |
|------|------|
| Pros | 自托管 / 完全自控 / 安全边界清晰（VM 级隔离）/ OSS 零授权费 |
| Cons | 依赖嵌套 KVM；Tencent Cloud 标准 CVM 不支持；Daytona OSS 无现成 docker-compose，需手工组装多组件（**A1-2b 附加发现，独立于 KVM 缺失**） |
| 实测结果 | ADR-0001 Findings — **Rejected**：`/dev/kvm` 不存在，KVM = NO |
| V2 升级路径 | 采购 Tencent CPM 裸金属实例，或申请 nested-virt VM type，届时重评 |

**结论**：基础设施不满足，Phase 0 不可行。

---

### Option B — Docker pool（自建容器池，**本 ADR 决策**）

| 维度 | 评估 |
|------|------|
| Pros | dev 服务器 Docker 已就绪，Phase 1 W3 可立即开工；冷启动约 1–3 s（比 Daytona 慢但可接受）；自托管全控，无外部服务依赖 |
| Cons | 安全边界比 KVM 弱（容器逃逸风险高于 VM）；网络隔离需手工配 iptables 或 docker network policy；需自行实现 workspace lifecycle 管理 |
| 风险缓解 | feature #4 必须实现：seccomp/AppArmor profile + 无 root 容器 + 网络白名单 + capabilities drop |

**关键**：应用层安全加固可将容器逃逸风险降至可接受范围；Phase 0 场景（受控 agent 任务）对隔离强度要求低于生产 Multi-tenant。

---

### Option C — CubeSandbox 提前到 v1（KVM MicroVM，**已否决**）

| 维度 | 评估 |
|------|------|
| Pros | 架构蓝本原定 v2 升级目标；冷启动比 Daytona 还快；安全边界强于 Docker pool |
| Cons | 项目成熟度不足（蓝本明确写"等其成熟"）；依赖 KVM（dev 服务器不可用）；Phase 1 W3 启动时可行性未验证 |

**结论**：成熟度不足 + KVM 依赖双重阻断，与 Option A 同样受制于基础设施缺口。

---

### Option D — Daytona Cloud API（托管服务，**已否决**）

| 维度 | 评估 |
|------|------|
| Pros | 规避 OSS 自托管复杂度（解决 A1-2b 问题）；workspace 预置，运维零负担；保留 Daytona 接口生态（未来切自托管时 API 兼容） |
| Cons | 引入外部服务依赖（与 #4 "完全自控"原则冲突）；每 workspace 按时计费，Phase 0 验证成本不可控（agent 任务并发量未知）；数据出境合规需评估（学员数据 + 工具调用结果） |

**结论**：外部服务依赖 + 数据合规未评估 + Phase 0 成本不可控 → 拒。但**保留为 V1 ADR Open Question Q1 的备选**：若未来 Tencent CPM 申请失败且 Docker pool 出现严重逃逸事件，可作紧急 SaaS 兜底。

---

### Option E — E2B（专为 AI agent 设计的沙箱 SaaS，**已否决**）

| 维度 | 评估 |
|------|------|
| Pros | 专为 AI agent 设计，API 简洁；冷启动 < 200ms；workspace 复用机制成熟；社区活跃 |
| Cons | 商业 SaaS，按调用计费（每秒级 CPU/RAM 计价）；数据出境（E2B 是美国公司，learner 数据 + 商业逻辑出境涉合规审查）；与项目"自托管可控"原则冲突；定价结构对长任务不友好 |

**结论**：合规风险 + 商业 SaaS 模式与项目自托管原则不符 → 拒。同 Option D，保留作紧急兜底备选。

---

## Decision

**选定 Option B — Docker pool。**

理由：
1. **基础设施现实**：V1 实测明确 KVM 不可用，dev 服务器与后续可能的 prod 同型机（Tencent Cloud 标准 CVM）大概率同样受限。
2. **交付速度**：Docker 现成可用，不阻塞 Phase 1 W3–W5 的并行开发。
3. **安全可接受**：Phase 0 场景为受控 AI agent 任务，通过应用层（seccomp + 无 root + iptables + 资源 limit）可将风险降至可接受水平。
4. **升级路径明确**：v2 阶段在自购 CPM 或申请 nested-virt 后，无缝切换 Daytona OSS / CubeSandbox，Docker pool 作为接口层可被替换。

---

## Consequences

### 覆盖蓝本决策 #5

architecture-v1.md Decision #5 默认选 Daytona OSS 作为 v1 沙箱。**本 ADR 将其替换为 Docker pool**。蓝本 §4.6 Sandbox 章节描述需更新为"Docker pool v1 + 未来 KVM 升级路径"，但蓝本本身的更新推迟到 #14 e2e-rollout 阶段统一同步，当前以本 ADR 为准。

### 影响 #4 sandbox-integration

- **接口边界变更**：从"Daytona API client"改为"Docker pool 自建 wrapper"
- **新增自行实现的 workspace lifecycle 接口**：`create` / `exec` / `destroy` / `cleanup`
- **资源 quota**：通过 Docker `--memory` / `--cpus` / `--pids-limit` 等 run-time 参数实现
- **网络隔离**：自定义 docker network + iptables，而非 Daytona 内置网络隔离

### 影响 #6 permission-pipeline

- 8 个 P0 Bash validator（应用层检查）不变 — 与运行时沙箱无关
- **新增 Docker runtime 安全加固为 #6 范围**（#6 S2 设计阶段必须覆盖；作为 #6 manifest 中独立 task 追踪）：
  - **seccomp profile**：方向是"Docker default profile + 追加 deny 规则"（白名单 + 黑名单混合策略）。具体禁用 syscall 清单（如 `ptrace` / `mount` / `unshare` / `keyctl` / `bpf` / `pivot_root` 等）在 #6 S2 spec 中列出
  - **AppArmor profile**：Phase 0 用 Docker 默认 `docker-default` AppArmor profile；feature #6 S2 评估是否需要自定义 profile（如限制特定路径写入）
  - **无 root 容器**：`USER 1000:1000` + `--user 1000:1000` 强制非 root 启动
  - **Capabilities drop**：`--cap-drop=ALL --cap-add=NET_BIND_SERVICE`（仅按需开 net bind）
  - **no-new-privileges**：`--security-opt=no-new-privileges` 防止 setuid 提权
  - 上述清单在 #6 S0 requirement card 中作为 acceptance criteria，S2 spec 中给出 Docker run-time 命令模板

### 不影响范围

- Phase 0 验证的其余假设（#2 工具注册、#3 状态持久化、#5 并发调度、#6 权限 pipeline）均不依赖具体沙箱技术
- prod 环境无变动

---

## Open Questions

以下问题须在 feature #4 S0 阶段启动前解决：

| # | 问题 | 负责阶段 | 备注 |
|---|------|---------|------|
| Q1 | Docker pool 的并发上限？dev 服务器 CPU/RAM 能支撑多少个并发容器工作空间？ | #4 S0 估算 | 影响容器池大小设计 |
| Q2 | 容器逃逸的应用层防御具体清单？（seccomp profile 内容 / AppArmor / 无 root / capabilities drop 列表） | #4 S2 设计 | 风险缓解的核心设计 |
| Q3 | workspace 清理策略：任务结束立刻删除 vs 池化复用预热容器？ | #4 S3 决策 | 影响冷启动延迟 |
| Q4 | 网络白名单实现方式：iptables rules / docker network alias / 反向代理哪种 | #4 S2 设计 | 影响 agent 外网访问行为 |
| Q5 | 资源监控告警阈值：CPU/RAM 打满时的降级策略 | #14 e2e-rollout | Phase 0 不强求 |
| Q6 | Tencent Cloud CPM 裸金属或 nested-virt VM type 的申请流程与定价调研 | v2 升级前 | 为 Daytona/CubeSandbox 切换做准备 |

---

## Trigger Conditions for Revisit

以下任一条件触发时，须重新评估本决策：

- 当 Tencent Cloud 申请到 nested-virt 虚拟机类型，或自购 CPM 实例时 → 重评 Option A（Daytona OSS）
- 当 Docker pool 出现容器逃逸事件或严重安全告警时 → 紧急评估切换至更强隔离方案
- 当 CubeSandbox 项目发布稳定 v1.0 并提供文档化部署方案时 → 重评 Option C
- 当 Phase 0 pilot 用户量超过预期、并发瓶颈成为 SLA 问题时 → 提前触发 v2 沙箱升级

---

*最后更新：2026-05-20 | 作者：agent-task-5*
