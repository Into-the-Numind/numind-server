# crawl4ai 网页读取工具 — 实施计划（S3）

> 上游 spec：`.ndf/features/crawl4ai-tool/spec.md`。仓库：numind-server（仅后端）。
> 无 DB migration（无 schema 变更）。无新 API 端点。无前端。

## Task 依赖图
```
T1 (crawl4ai client 包) ──→ T2 (web_fetch 集成 + config + span)
T3 (crawl4ai 部署清单) 独立，可与 T1/T2 并行（不同文件，不同目录）
T4 (S5 验证策略) 文档项，依赖 T1-T3 设计已定
```

---

## T1：crawl4ai 客户端包
- **文件**：`internal/pkg/crawl4ai/client.go`（新增）、`internal/pkg/crawl4ai/client_test.go`（新增）
- **描述**：实现 spec §3 的 `Client`：
  - `NewClientFromConfig()` 读 viper `crawl4ai.base_url/token/timeout_seconds/content_filter`
  - `Configured() bool`（base_url 非空）
  - `RenderMarkdown(ctx, targetURL) (*RenderResult, error)`：`POST {base_url}/crawl`，请求体按 spec §3.3；防御性解析 markdown（**`json.RawMessage` 两段解析**：先试 string，再解 `{fit_markdown, raw_markdown}`；优先级 `fit_markdown`→`raw_markdown`→string），title 取 `metadata.title`；非 200 / `success=false` / markdown 全空 / **body 读取失败** → error（不 panic）
  - 用 `httpclient.NewClient`（超时取配置）；**每个 Request 显式 `RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0}`**（否则 Do() 补默认 3 次重试，照 `web_search.go:132`）；token 非空加 Bearer 头
- **验收条件**：
  - `go test ./internal/pkg/crawl4ai/...` 通过
  - 单测覆盖（httptest mock crawl4ai）：markdown 字符串形态、对象形态(fit/raw)、success=false、空 markdown、非 200、超时、**body 读取中断**；`Configured()` 在 base_url 空/非空两态
- **原子性**：完全自包含，不依赖 web_fetch 改动；完成后可独立编译+测试 ✓

## T2：web_fetch Execute 集成 + 配置 + span
- **文件**：
  - `internal/numind/biz/agent/tool_web_fetch.go`（改）
  - `internal/numind/biz/agent/tool_web_fetch_test.go`（改）
  - `config_local.yaml`、`config_dev.yaml`、`config_qa.yaml`（改，加 `crawl4ai:` 块）
  - `factory_platform.go`（**改 web_fetch metadata Description 反映"JS 渲染"——S3 review 提升为必做，非可选**：LLM 靠 Description 认知工具能力，明示渲染能改善选择质量）
- **描述**：按 spec §4：
  - `webFetchTool` 加 `crawl4ai *crawl4ai.Client` 字段；`NewWebFetchTool()` 注入 `crawl4ai.NewClientFromConfig()`
  - 把现有裸 HTTP 抓取+转换逻辑抽成内部 helper（如 `fetchRaw(ctx, targetURL)`），供 fallback 与 raw_direct 复用
  - Execute 加分支：configured→`RenderMarkdown`；成功用渲染结果，失败/未配置→`fetchRaw`
  - SSRF `validateFetchURL` 保持在分支之前（对两路径生效）
  - 软错误不变量：crawl4ai 失败不返回 Go error
  - Langfuse EndSpan output 增 `fetch_path` / `crawl4ai_latency_ms` / `crawl4ai_status`
  - **不改** 工具 Name/InputSchema/输出 struct
- **验收条件**：
  - `go test ./internal/numind/biz/agent/...` 通过；`task lint` 0
  - 新增/改造用例：渲染路径（注入 mock client 返回 markdown→content_md 来自渲染、fetch_path=render）；fallback（mock client 报错→走裸 HTTP，fetch_path=raw_fallback）；raw_direct（client=nil/未配置→裸 HTTP）；**SSRF still enforced**（内网 URL 在调 crawl4ai 前被拒，mock client 不应被调用）；soft-error 保留（裸 HTTP 与渲染都失败→returnSoftError 不杀 run）
  - **早退出 span 也带 fetch_path（S3 review P2）**：请求构建失败 / HTTP 4xx-5xx / body 读取失败等早 `EndSpan` 分支的 span output 都要 set `fetch_path`
  - 现有 web_fetch 全部既有用例仍绿（无回归）
- **原子性**：依赖 T1；完成后系统编译通过、tests 绿、行为=配置则渲染/否则裸 HTTP ✓

## T3：crawl4ai 部署清单（dev）+ 文档
- **文件**：`deploy/crawl4ai/docker-compose.yml`（新增）、`deploy/crawl4ai/README.md`（新增）
- **描述**：
  - compose 服务：`unclecode/crawl4ai:0.8.6`，端口 11235，`shm_size: 1g`，**资源限制用 Compose v2 `deploy.resources.limits.memory: 4g`（不是 v1 `mem_limit`，S3 review P2）**，健康检查打 `/health`
  - **网络隔离（spec §5 硬要求）**：crawl4ai 容器置于仅可出公网、不可达内部业务子网/DB/其他容器的网络；compose 里用独立 network + 不挂内部 network
  - README：dev 部署步骤 + 后端如何连（`crawl4ai.base_url`）+ **prod 配置块样例 + prod 部署步骤**（运维手动，强调禁改 config_prod.yaml、本 feature 不触 prod）
- **验收条件**：
  - `docker compose -f deploy/crawl4ai/docker-compose.yml config` 校验通过（YAML 合法）
  - 本地起容器 `curl localhost:11235/health` 成功（S5 做）
  - **网络隔离须实测（S3 review P2，非仅文档）**：S5 用 `docker exec crawl4ai curl -f --max-time 3 http://<内部容器IP>:<port>` 验证打不通内部网络（连接拒绝/超时）
  - README 含网络隔离说明 + prod 配置样例
- **原子性**：纯部署/文档文件，不影响 Go 编译；可与 T1/T2 并行（Tier 2，跨目录 disjoint）✓

## T4：S5 验证策略（Rule 10 — 独立 task，交 S3 reviewer 审）
- **验证方式**：**后端 TDD（自动回归）+ 本地集成验证（一次性）**。本 feature 纯后端、不涉及前端，不涉及支付/权限/会员高风险逻辑 → 按 Rule 10 **不要求 Playwright E2E**。
- **理由**：
  - 持久回归保护来自 T1/T2 的 Go 单测（mock crawl4ai），永久留库。
  - 真实 crawl4ai 渲染需起容器，属一次性本地集成验证（无持久 live-service 测试——诚实声明：crawl4ai 服务本身的连通未来靠手动/部署健康检查，非自动回归）。
- **S5 关键验证路径**：
  1. 起本地 `crawl4ai:0.8.6` 容器 + 后端，配 `crawl4ai.base_url` → 对一个**已知 JS 渲染页面**调 web_fetch → content_md 非空；对照裸 HTTP 路径对同页返回空壳/被挡（证明升级真实有效）
  2. 停掉 crawl4ai 容器 → 同一调用自动 fallback 裸 HTTP，不报错（fetch_path=raw_fallback）
  3. base_url 置空 → raw_direct，行为=现状
  4. 传一个解析到内网/loopback 的 URL → SSRF 拒绝，crawl4ai 未被调用
  5. 本地 Langfuse（如启用）→ 确认 span 有 fetch_path 字段
- **产出**：`qa-report.md`

---

## 不做（范围护栏，复述）
- ❌ 小红书等强反爬平台抓取（换策略议题，独立）
- ❌ 多页深度 crawl（BFS/DFS）
- ❌ 触碰 prod（config_prod.yaml / prod 部署）——本 feature 止于 dev
