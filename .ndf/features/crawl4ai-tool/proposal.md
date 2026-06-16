# crawl4ai 网页读取工具 — 提案

## §1 方案概述 [客户可见]
给 agent 一双能看清现代网页的眼睛。

现在 agent 的"读网页"工具(`web_fetch`)只会做最原始的抓取——不会等页面把内容加载出来,也过不了大多数网站的基础防护。结果是:很多用 JS 渲染的网页、挂着 Cloudflare 的站点，它抓回来要么是空壳、要么直接被挡。

方案：在服务器上自托管一个开源组件 **crawl4ai**（用真实浏览器把页面渲染好，再提取成干净文本）。`web_fetch` 改为优先走 crawl4ai；**万一 crawl4ai 没部署或临时挂了，自动退回现在的原始抓取方式，绝不影响现有功能**。对 agent 和用户来说，工具用法完全不变——只是从"经常读不到"变成"读得到"。

> **不在本次范围**：小红书/抖音这类强反爬平台的抓取。自托管硬爬会重蹈"账号被禁言"覆辙，那是"换数据获取策略"的独立议题。

## §2 报价与周期 [客户可见]
- 预估工作量：**2–3 天**（含 crawl4ai 容器部署调试）
- 报价：N/A（内部功能，一人公司自研）
- 交付时间线：本周内 dev 可验收

## §3 技术可行性 [AI 内部]
### 现有功能复用
- **`web_fetch` 是完美的姊妹模板**：`FullTool` 接口 + `BaseTool` 默认实现，新逻辑只改 `Execute` 内部，工具元数据/输入契约（`url`+`prompt`）不动。
- **SSRF 防护直接复用**：`validateFetchURL()` + `checkIPSafe()`（`tool_web_fetch.go`）原样沿用——把目标 URL 交给 crawl4ai 之前先做 pre-flight 校验。
- **HTTP 客户端**：`httpclient.NewClient()`（连接池）调 crawl4ai 服务。
- **配置**：viper，照 `web_search.tavily.*` 加一个 `crawl4ai.*` 块。
- **可观测性**：沿用现有 `tool.web_fetch.execute` span，在 output 里记录走了哪条路径（render / raw_fallback）+ 延迟。

### 技术风险
- **crawl4ai 容器吃资源**（自带浏览器，内存占用高）→ 缓解：限制并发、设超时、dev 先验证资源占用再上 prod。
- **SSRF（渲染路径）**：crawl4ai 在我们内网里跑，若把内网 URL 交给它，它可能去打内部服务 → 缓解：① Go 侧 pre-flight `validateFetchURL` 拦截内网/loopback/metadata；② crawl4ai 容器**网络隔离**（仅出网，禁访内部子网）。S2 详定。
- **prod 配置约束**：硬规则禁改 `config_prod.yaml` → 缓解：代码默认 `crawl4ai.base_url` 为空时**跳过 crawl4ai 直接走裸 HTTP**（即现状），prod 由运维另加配置 + 部署服务后才"自动升级"，天然安全灰度。
- **新容器运维**：dev/prod 各多一个常驻容器 → 部署脚本 + 健康检查纳入。

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（如功能涉及 LLM 调用）
- [ ] 涉及 LLM 调用：**否**（web_fetch 是工具，不是 LLM 调用本身）
- Trace 起点：N/A（沿用 agent run 已有 trace）
- Generation 点：N/A（无新 LLM generation）
- 关键元数据：保留现有 `tool.web_fetch.execute` span，新增 output 字段 `fetch_path`(render/raw_fallback)、`crawl4ai_latency_ms`
- 说明：不新增 generation，但保留并增强工具 span。

## §4 产品需求定义 — PRD [AI 内部]
### 用户故事
- 作为 agent 的使用者（陪跑学员/机构），我需要 agent 能读取**现代网页**（JS 渲染、挂基础反爬的站点），以便调研类任务拿到真实正文而不是空壳。
- 作为运维，我需要 crawl4ai 服务挂掉时 agent 的读网页能力**优雅降级**而不是整体报错。

### 验收标准
- [ ] crawl4ai 服务可用时：对一个已知 JS 渲染页面，`web_fetch` 返回**非空正文**（对照组：裸 HTTP 路径对同一页面返回空壳/被挡）
- [ ] crawl4ai 不可用/未配置时：`web_fetch` 自动退回裸 HTTP，行为与现状**一致（无回归）**
- [ ] SSRF 仍拦截：内网/loopback/cloud-metadata URL 在渲染路径下同样被拒
- [ ] Langfuse span 记录 `fetch_path`（render / raw_fallback）+ 延迟
- [ ] 工具输入契约不变：LLM 仍只传 `url` + 可选 `prompt`，无感知
- [ ] 硬错误绝不杀 run：crawl4ai 超时/报错走软错误或 fallback，不返回 Go error（沿用 `returnSoftError`）

### 边界情况
- crawl4ai 超时 → 退 fallback（或软错误，S2 定策略）
- crawl4ai 返回非 200 / 空内容 → 退 fallback
- 目标 URL 本身 4xx/5xx → 软错误（同现状）
- 渲染后内容超大 → 截断上限（沿用/调整 100KB，S2 定）
- crawl4ai 配置为空 → 跳过，直接裸 HTTP

### 权限规则
- 不变。agent 工具对所有 agent run 可用，经 `tool_definition` 启停；无用户分级差异。

### UI 行为规格
- N/A（纯后端，agent 工具由 LLM 自动发现，无前端、无手动工具选择器）
