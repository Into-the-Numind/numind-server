# ADR-0001: KVM Availability + Daytona OSS Dev Server Verification

**Status**: Rejected  
**Date**: 2026-05-20  
**Feature**: agent-mode-phase0-verification  
**Task**: Task 4 — V1 KVM + Daytona OSS Deployment Verification

---

## Context

Phase 0 Verification is evaluating which sandbox infrastructure to use for the AI Agent Mode feature.
V1 assumption (A1): the existing dev server (`49.233.219.254`) supports KVM hardware virtualization
and can run Daytona OSS self-hosted to provide isolated workspace containers for agent tasks.

This ADR records the empirical verification of that assumption.

---

## Findings

### A1-1: KVM Availability

**Judgment: KVM = NO**

The dev server is a Tencent Cloud VM (`VM-24-12-opencloudos`) running as a KVM guest itself.
Nested KVM (guest-inside-guest hardware virtualization) is not enabled/exposed by the hypervisor.

| Check | Result |
|-------|--------|
| `/dev/kvm` device node | **Absent** — `ls: cannot access '/dev/kvm': No such file or directory` |
| `/proc/cpuinfo` vmx/svm flags | **0** (no hardware virt flags exposed to guest) |
| `kvm-ok` utility | Not installed; `cpu-checker` package unavailable (not Ubuntu; OS is OpenCloudOS/CentOS-based) |
| `lsmod \| grep kvm` | **No kvm modules loaded** |
| `systemd-detect-virt` | Returns `kvm` — confirming the server itself IS a KVM guest |
| Kernel | `6.6.68-20.oc9.x86_64` (OpenCloudOS 9) |
| Virtualization type (lscpu) | `full` — guest VM, nested virt not available |

Raw SSH output:
```
root
Linux VM-24-12-opencloudos 6.6.68-20.x86_64 #1 SMP PREEMPT_DYNAMIC Thu Jan  9 21:45:05 CST 2025 x86_64
kvm-ok: command not found (cpu-checker not available on OpenCloudOS)
ls: cannot access '/dev/kvm': No such file or directory
egrep -c '(vmx|svm)' /proc/cpuinfo => 0
systemd-detect-virt => kvm  (this machine IS a KVM guest — nested KVM not exposed)
```

**Root cause**: Tencent Cloud standard VMs do not expose nested KVM by default. To get KVM inside
a VM, one would need a "bare metal" instance or a VM type with nested virtualization enabled
(e.g., CPM — Cloud Physical Machine). The current dev server is a standard CVM.

### A1-2: Daytona OSS Deployment

**Judgment: DEFERRED — skipped due to KVM = NO**

Per execution protocol: Daytona OSS is designed to manage VM-based workspaces using KVM.
Attempting to deploy Daytona OSS on a server without `/dev/kvm` would result in a non-functional
installation (workspace provisioning would fail at the VM creation step). No deployment attempt
was made to avoid polluting the dev server with a non-functional Daytona installation.

#### A1-2b: 附加发现 — Daytona OSS 部署复杂度（独立于 KVM 的发现）

Daytona OSS does not publish a ready-to-use `docker-compose.yml` for single-node
self-hosted deployment in its public GitHub release assets — deployment would require manual
configuration of multiple components (server, registry, runner). 这是 **独立于 KVM 缺失** 的额外阻碍：
即使将来 KVM 可用，OSS 自托管路径的运维成本仍然非零。**对 V5 ADR 的暗示**：若未来选 Daytona，
应优先 Daytona Cloud API（托管）而非 OSS 自托管。

### A1-3: Workspace Creation

**Judgment: DEFERRED** — depends on A1-2; not tested.

Docker is available on the dev server (`Docker version 0.0.0-20241223130549-3b49deb`), confirming
container-based sandboxing is feasible as a fallback approach.

---

## Decision

**Trigger V5 Backup: Do NOT proceed with Daytona OSS on current dev server.**

The V1 path (Daytona OSS on dev server) is blocked by the absence of nested KVM. The current
`49.233.219.254` CVM cannot support VM-based workspace isolation.

### Recommended V5 Alternatives (in priority order)

1. **Docker-pool sandbox** — use the existing Docker daemon on dev server to spin up container
   workspaces (no KVM required). Lower isolation than VMs but sufficient for Phase 0 agent tasks.
   Simpler operationally.

2. **Daytona Cloud API** — use Daytona's hosted service instead of self-hosted OSS. Avoids all
   infrastructure management; workspaces are pre-provisioned. Cost per workspace applies.

3. **Tencent Cloud CPM (bare metal)** — if strong VM isolation is required, procure a CPM instance
   that exposes nested KVM. Higher cost and longer setup time.

4. **E2B (cloud sandboxes API)** — purpose-built sandbox-as-a-service for AI agents, with
   per-execution pricing and no infra management overhead.

**For the Phase 0 prototype, Docker-pool (option 1) is recommended** as it reuses existing
infrastructure and the isolation requirements for Phase 0 are modest.

---

## Consequences

### Impact on Task #4 (sandbox-integration)

- Daytona OSS integration cannot proceed on current dev server without infrastructure change.
- Docker-based sandbox integration is the viable path for Phase 0.
- If Task #4 implementer was planning Daytona API integration, switch to Docker SDK approach.

### Impact on Decision #5 (architecture blueprint)

- V1 architecture (Daytona OSS self-hosted) should be replaced with Docker-pool approach in blueprint.
- The blueprint should note: "production graduation may adopt Daytona Cloud or bare-metal KVM;
  Phase 0 uses Docker-pool for velocity."
- No prod environment changes are implied by this decision.

### Cleanup Status

- **0 residual containers/images/files** on dev server (Daytona was never deployed).
- No cleanup required.

---

## Open Questions

1. **腾讯云嵌套 KVM 升级路径**：
   - Tencent CPM（Cloud Physical Machine 裸金属）：暴露 `/dev/kvm`，可装 Daytona OSS。需调研：申请流程、起价（粗估 ¥3-5k/月）、最小可用规格、是否支持按量计费
   - Tencent CVM 启用嵌套虚拟化：标准 CVM 不支持，是否有特殊 VM type 可申请？需通过工单咨询
   - 决策时机：v2 阶段（M5+，feature #14 e2e-rollout 之后）若需强 VM 隔离再评估

2. **Daytona OSS 在非 CVM 环境的可用性**：
   - 自建机房裸金属服务器：理论上 KVM 直通可用，但需运维投入
   - 其他云（AWS / Azure / GCP）的 nested KVM 支持现状：已有公开资料表明 AWS bare metal / Azure nested virt 支持成熟，可作未来多云方案备选
   - 决策时机：跨云迁移或自建数据中心时

3. **附加发现 — Daytona OSS 部署复杂度**（独立于 KVM 的发现）：
   - Daytona OSS 不发布 ready-to-use docker-compose.yml；需手工配 server + registry + runner 多组件
   - 即使将来 KVM 可用，部署 Daytona OSS 自托管的运维成本仍然非零
   - 若选 Daytona，应优先考虑 Daytona Cloud API（托管版）而非 OSS 自托管

### Revisit Conditions

本 ADR 决策路径（Daytona 不可行）在以下条件触发时**必须重评**：

- ✅ 申请到支持嵌套 KVM 的 Tencent CVM type / CPM 裸金属
- ✅ 自购 / 迁移到自建机房或支持 nested KVM 的云
- ✅ Daytona Cloud API 价格降到可接受区间（粗估 < ¥0.10 / workspace-hour）
- ✅ CubeSandbox（架构蓝本 §4.6 v2 升级目标）发布 v1.0 stable release
- ❌ 即使 Docker-pool 出现容器逃逸事件，也不应"回头选 Daytona"，应紧急切 E2B / 其他 SaaS

### Override Note for V5 ADR

> 本 V1 ADR 记录"为什么 Daytona OSS Phase 0 不可行"，但不直接声明"覆盖蓝本决策 #5"。
> 正式覆盖声明在 `0002-sandbox-final.md`（V5 ADR）顶部，本 ADR 是其输入证据。

---

## Appendix: Full SSH Output

> **Note on screenshots**：spec §2.3 模板要求 `screenshots/` 附件；本 ADR 用下方完整 SSH 文本输出代替 GUI 截图（实测过程是命令行 SSH，无 GUI 界面可截）。`.ndf/decisions/agent-mode-phase0-verification/screenshots/` 目录未创建（无文件归档需要）。

```
# SSH connectivity
$ ssh root@49.233.219.254 "whoami && uname -a"
root
Linux VM-24-12-opencloudos 6.6.68-20.oc9.x86_64 #1 SMP PREEMPT_DYNAMIC Thu Jan  9 21:45:05 CST 2025 x86_64 x86_64 x86_64 GNU/Linux

# KVM checks
$ ls -la /dev/kvm
ls: cannot access '/dev/kvm': No such file or directory

$ egrep -c '(vmx|svm)' /proc/cpuinfo
0

$ lsmod | grep kvm
(no output — no kvm modules loaded)

$ systemd-detect-virt
kvm   ← server is itself a KVM guest; nested KVM not available

$ lscpu | grep -i virt
Address sizes:  48 bits physical, 48 bits virtual
Virtualization type:  full

$ docker --version
Docker version 0.0.0-20241223130549-3b49deb, build 3b49deb
```
