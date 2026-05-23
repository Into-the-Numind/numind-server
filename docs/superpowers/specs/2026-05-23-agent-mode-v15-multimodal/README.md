# 有数 Agent Mode V1.5 — NDF Standard Track 草案

> **方案 B 均衡派 + 5 个 Hermes/OpenHuman 加法 + 9 个产品 owner 决策** 的实施草案。
> 共 3 个板块、18 个 sub-task、估算总工期 13-21 个工作日。
>
> **产品名"有数"（Numind）** — 莫小派是有数的一个客户，不是产品名本身。

## 9 个关键决策（已敲定，详见 context.md §11）

| # | 决策摘要 | 实施约束 |
|---|---|---|
| D1 | 新加 `profile.attachment.vision_describe` | task 1.2 用此新 profile |
| D2 | **要做放大镜**：实现 `analyze_image` / `annotate_image` vision 工具 | task 1.4 完整实施 |
| D3 | **平行重做**：新建 `compactv2/` 包，V1 完全保留不动 | 板块 2 全部走 V2 独立路径 |
| D4 | dialectic / autocompact 用 qwen-plus 或 deepseek-v3-2（**不 thinking**）| 后续可调 |
| D5 | autocompact 摘要用 `<reference-only>` XML 包裹 | task 2.4 + scrubber 配合 |
| D6 | AGENT.md cascade **只激活 2 层**（部署级 + 用户全局）| task 3.1 简化 |
| D7 | B2B2C 父子账户 memory **完全隔离** | 板块 3 全部 schema 设计 |
| D8 | 25 个 task profile（4 个新增，dialectic 通用化、Layer A）| 全板块 profile 注册 |
| D9 | 中文搜索先用 MySQL 8 FULLTEXT + ngram | task 3.5 |

**Layer A vs Layer B 范围澄清**（关键认知）：
- **V1.5 = Layer A**：dialectic 对**使用 agent 的真实 user 本人**画像
- **V2 = Layer B**：dialectic 对**使用者会话关注的客观对象**（客户/数据集/文档/产线等）画像
- V1.5 schema 加 `subject_id NULLABLE` 字段预留 V2

## 文档结构

```
/tmp/numind-spec/
├── README.md                          # 你正在看（含 9 决策摘要）
├── context.md                         # 共享上下文（项目背景 + 9 决策 + 26 profile 列表 + V2 预留）
│
├── 01-multimodal/                     # 板块 1：多模态 fallback
│   ├── README.md                      # S0 + 整体架构（含放大镜层）
│   ├── task-01-capability-matrix.md   # capability 字段 + 路由 helper
│   ├── task-02-attachment-fallback.md # 上传时双模态固化
│   ├── task-03-routing-layer.md       # buildAgentInput capability-aware
│   ├── task-04-tool-gating.md         # ⚠️ 重写：放大镜 vision 工具实现
│   └── task-05-error-recovery.md      # runtime 错误剥离重试
│
├── 02-context/                        # 板块 2：上下文管理 V2（平行重做）
│   ├── README.md                      # S0 + 平行重做架构（V1 / V2 双路径）
│   ├── task-01-schema.md              # ⚠️ 新增 compact_state_v2 字段，不动 V1
│   ├── task-02-tool-artifact.md       # tool result 写盘 + read 工具
│   ├── task-03-prune-microcompact.md  # L1 prune + L2 microcompact
│   ├── task-04-autocompact.md         # L3 autocompact + XML <reference-only>
│   └── task-05-streaming-scrubber.md  # 防 memory 标签泄露
│
└── 03-memory/                         # 板块 3：记忆管理（V1.5 = Layer A only）
    ├── README.md                      # S0 + 3 层 + Layer A 范围
    ├── task-01-agent-md-cascade.md    # ⚠️ 简化为 2 层 cascade
    ├── task-02-memory-schema.md       # ⚠️ facts 表加 subject_id 字段预留 V2
    ├── task-03-llm-extraction.md      # 异步 LLM 抽取 facts (Layer A)
    ├── task-04-top5-selector.md       # side-query 选 top-5 注入
    ├── task-05-fts5-search.md         # MySQL 8 + ngram 中文搜索
    ├── task-06-trivial-shortcut.md    # trivial 短路 + cadence
    ├── task-07-dialectic.md           # ⚠️ 通用化 + Layer A 澄清 + 多场景例子
    └── task-08-temporal-tree.md       # 日/周/月/季 digest cron
```

## 三大板块概览

| 板块 | 解决的问题 | sub-task 数 | 估算工期 | 优先级 |
|---|---|---|---|---|
| **1. 多模态 fallback** | 单模态模型 + 图片 → 不再静默挂掉 + 放大镜精读 | 5 | 5-8 天 | P0（必须）|
| **2. 上下文管理 V2** | 长会话 + 大 tool result → 不再爆 context（平行重做，V1 保留）| 5 | 5-7 天 | P0（必须）|
| **3. 记忆管理（Layer A）** | 跨会话 → "agent 记得使用者本人"的体验 | 8 | 7-10 天 | P1（强烈建议）|

总工期：**13-21 工作日**（按 1 人 vs 多人并行有差异）。

## 实施顺序建议

```
Week 1:
  Day 1-2: 板块 1 task-01 (capability matrix) — 是 task-03/04 的前置
  Day 3:   板块 1 task-02 (attachment fallback) ⊥ 板块 2 task-01 (compactv2 schema)
  Day 4-5: 板块 1 task-03 (routing) + task-04 (vision tools 放大镜) ⊥ 板块 2 task-02
  Day 6:   板块 1 task-05 + 板块 2 task-03 (prune+microcompact)
  Day 7-8: 板块 2 task-04 (autocompact) + task-05 (scrubber) ⊥ 板块 3 task-01/02

Week 2:
  Day 9-10: 板块 3 task-03 (LLM extraction) + task-04 (top-5 selector)
  Day 11:   板块 3 task-05 (FTS5) + task-06 (trivial) — 都很快
  Day 12-14: 板块 3 task-07 (dialectic Layer A 通用化)
  Day 15-19: 板块 3 task-08 (temporal tree) — 最长

Week 3 (buffer):
  Day 20-21: 联调 + 测试 + dev 验证
```

⊥ = 可并行

## 关键依赖图

```
板块 1 内部：
  task-01 (capability) ─┬─→ task-03 (routing)
                        ├─→ task-04 (vision tools 放大镜)
                        └─→ task-02 (attachment fallback)
                            ↓
                        task-05 (error recovery)

板块 2 内部（compactv2 独立包）：
  task-01 (schema)
       ↓
  task-02 (tool artifact) → task-03 (prune) → task-04 (autocompact)
                                                        ↓
                              task-05 (scrubber - 独立) ┴

板块 3 内部（Layer A only）：
  task-01 (AGENT.md cascade 简化版) ⊥ task-02 (schema + subject_id 预留)
                            ↓
                       task-03 (extraction)
                            ↓
                       task-04 (top-5 selector)
                            ↓
       task-05 (FTS5) ⊥ task-06 (trivial) ⊥ task-07 (dialectic Layer A)
                            ↓
                       task-08 (temporal)

跨板块：
  板块 1 完成后才能在 多模态切换场景 中测试 板块 2 的 autocompact
  板块 3 task-04 注入的 top-5 facts 受 板块 2 token budget 管理
```

## NDF 流程对应

每个 task 对应一个 NDF Standard track 阶段：
- S0 requirement card → 在各板块 README 里
- S1 + S2 → 在 task spec 文件里
- S3 task plan → 在每个 task spec 的"实施步骤"
- S4 编码 → 实际写代码（不在 spec 范围）
- S5 验证 → 在 task spec 的"验证策略"
- S6 部署 → 走 `/deploy-dev` 验证后再讨论

## V2 扩展预留（仅 schema 兼容，不实施）

V1.5 spec 已在 schema 层面预留：
- `user_memory_facts.subject_id` 字段（V1.5 全 NULL）
- compact V1 包完全保留（V2 跑稳后可平滑切换 SOP/SalesRAG/监控等场景）
- AGENT.md cascade 路径 3-6 在代码注释里留 hook（V2 业务出现"项目"概念时启用）

V2 范围（未来）：
- 加 `agent.dialectic.subject` profile（Layer B：对会话关注对象建画像）
- 加 `subject_card` 表
- 加 entity 三元组（客户关系图）
- 升级 ES 替代 MySQL 8 ngram（量大时）
