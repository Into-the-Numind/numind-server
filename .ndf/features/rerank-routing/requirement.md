# 需求卡片：rerank-routing（可扩展 rerank 多供应商优先级路由）

> S0 工件 · feature id: rerank-routing · track: standard · 2026-06-11

## 1. 需求一句话

把 rerank（重排序）做成和主对话模型一样的**一等公民**：注册表里可挂多个 rerank 供应商，按优先级自动 fallback；任何一家失败（限流/宕机/模型下架）自动切下一家。

## 2. 背景与动机（实证）

- DMXAPI **下架了 qwen3-rerank**（实测 `POST /v1/rerank` model=qwen3-rerank → HTTP 503「所有令牌分组下均无可用渠道」）。
- 注册表把 rerank 改路由到 `ali-dashscope`，但 `ali.go` 适配器**只实现 chat+embed，没有 Rerank**（全代码库只有 `dmxapi.go` 实现）。gateway 报 `provider "ali" does not support Rerank`。
- 结果：**dev + prod 双环境 rerank 静默失效**，salesrag（付费）+ chatbot 的重排在线上一直不工作。已用 `bge-reranker-v2-m3-free`（DMXAPI 免费，5次/分钟限流）临时止血。
- 根本架构缺口：
  1. gateway 派发把 adapter 绑死 primary（`makeHandler(p, primary)`），fallback 只换 route 不换 adapter → 跨家 fallback 失败（chat 之所以"看似能跨家"，是因为所有 chat 都是 OAI 兼容协议，碰巧能拿 A 的 adapter 打 B 的地址；rerank/embed 各家原生协议会失败）。
  2. Fallback 中间件只试 `fallbacks[0]`（单级），429 限流被判不可重试（`wrapHTTPStatusErr` 4xx → 非 retryable）→ 限流根本不触发 fallback。
  3. 没有任何非 DMXAPI 的 rerank 适配器，无法做供应商冗余。

## 3. 用户故事

- 作为运营，我希望在 admin 注册表里像配主对话模型一样，给 rerank 任务挂多个供应商（DMXAPI bge 免费档做主、百炼 qwen3-rerank 做兜底），按优先级排序。
- 作为系统，当主 rerank 供应商限流/报错时，我自动按优先级切到下一家，全程对业务无感。
- 作为开发，以后新增任意 rerank 供应商，只要注册 adapter（新协议时）+ admin 加一条按优先级的路由即可，无需改派发核心。

## 4. Triage 判定

**推荐档位：Standard**

| 5 条标准 | 评估 |
|---|---|
| DB schema 变更 | 否（注册表路由是数据，经 admin/SQL 配） |
| 新增 API 端点 | 否 |
| **新外部服务集成** | **是**（百炼 rerank capability） |
| 影响文件 ≤3 | 否（gateway + ali adapter + fallback 中间件 + 错误映射 + 测试） |
| 支付/权限高风险 | 否，但触及**共享 AI gateway 派发核心** + salesrag 收入路径 |

→ **Standard 主干**（用户已确认）。不可降级。

## 5. 范围

- **In**：numind-server。gateway 每路由自适配器解析；ali/百炼 rerank 适配器（qwen3-rerank）；429 → retryable；Fallback 中间件多级 cascade；dev 注册表路由配置（bge 主 + 百炼 qwen3 兜底）。
- **Out（本次不做）**：prod 路由切换（停在 dev 验收，prod 由用户后续授权）；admin UI 对 rerank 路由的可视化增强（已有通用 AI 服务 CRUD 可用）；embed 跨家 fallback 虽被 T1 一并修好但不专门验证。

## 6. 验收标准（详见 S1 PRD）

- AC1：rerank 任务可在注册表挂 ≥2 个供应商（不同 provider），按优先级排序。
- AC2：主供应商返回限流/可重试错误时，自动按优先级 fallback 到下一家并成功返回。
- AC3：百炼 qwen3-rerank 经 ali 适配器可正常 rerank（HTTP 200，返回 index+score）。
- AC4：gateway 派发对每条 route 用其**自己 provider 的 adapter**（跨家 fallback 不再拿错适配器）。
- AC5：429 限流被识别为可触发 fallback 的错误。
- AC6：chat/embed 既有 fallback 行为零回归（多级 cascade 是纯增强，单 fallback 等价旧行为）。
- AC7：所有 rerank 调用仍走 aiservice 统一入口 + Langfuse trace（rerank span 不再 ERROR）。
