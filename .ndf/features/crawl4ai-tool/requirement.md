# crawl4ai 网页读取工具（agent mode）

## 来源
- 提出人：用户（有数 / zhiyuchen）
- 提出日期：2026-06-17

## 需求描述
给 agent mode 接入一个真正能用的"网页读取"工具。

现状痛点：agent 现有的 `web_fetch` 工具只做裸 HTTP GET（`tool_web_fetch.go`）——不渲染 JavaScript、不过任何反爬。在 2026 年这意味着它对**相当一部分普通网页**都力不从心：Medium / Substack / Notion 站、用 React/Next.js 渲染的官网与文档、以及大量挂着 Cloudflare 基础 bot 墙的小站点，抓回来要么是空壳，要么直接被挡。用户原话定性为"摆设"。

引入自托管的 **crawl4ai**（开源，Apache-2.0）作为 agent 的网页读取工具：用真实浏览器（Playwright）渲染页面后输出 LLM-ready Markdown，补强或替换 `web_fetch` 的开放网页读取能力。crawl4ai 可自托管成 Docker HTTP 服务，Go 后端通过 HTTP 调用接入，接法接近现有 `web_fetch`，不改 agent 运行时架构。

## 业务目标
agent mode 的核心价值是"自动跑调研类任务"（IP 孵化研究、竞品分析、市场调研），这类任务高度依赖"读网页"。眼睛弱 = 产品价值直接打折。本需求让 agent 能可靠读取现代开放网页，把 `web_fetch` 从"摆设"变成"能用"。

## 范围边界（重要）
**做（In scope）：**
- 自托管 crawl4ai Docker 服务（dev / prod 部署）
- 新增一个 agent 工具：单页"渲染 + 提取"，输出干净 Markdown
- 沿用 `web_fetch` 的 SSRF 两段校验 + 软错误模式（硬错误会杀整个 run，见 `project_agent_tool_hard_error_kills_run`）
- Langfuse trace / generation 接入（`.claude/rules/ai-service.md`）
- 在 `tool_definition` 表注册新工具（admin 可启停）

**不做（Out of scope，明确排除）：**
- ❌ 小红书 / 抖音 / 微博等强反爬平台的数据抓取。理由：自托管 = 用自家 IP/账号硬爬，`xiaohongshu-mcp` 已让账号被禁言十天，验证此路不通。平台数据是"换策略"（托管供应商扛封禁 / 官方商务通道）的问题，不是"换个更强开源库"能解决的，留作独立议题。
- ❌ 通用爬虫的多页深度抓取（BFS/DFS 全站 crawl）。先做单页 render+extract，深度 crawl 视后续需求再议。

## 开放问题（留待 S2 技术设计定夺）
- 新工具是**替换** `web_fetch` 的后端（统一走 crawl4ai），还是**并存**（`web_fetch` 保留为快路径 + 新工具走渲染路径）？倾向并存（快慢分层），但需在 S2 评估成本/复杂度后定。

## 优先级
高

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：否（用现有 `tool_definition` 表注册一行工具数据，非 schema 变更）
  2. 新增 API 端点：否（agent 工具是内部注册，不是对外 HTTP route）
  3. 新外部服务集成：**是**（自托管 crawl4ai Docker 服务）← 触发 Standard
  4. 影响文件数：**>3**（新工具实现 + factory 注册 + config_*.yaml + 部署 compose/Dockerfile + 测试）
  5. 高风险业务逻辑（支付/权限）：否（但涉及 SSRF 安全，沿用 web_fetch 两段校验）
- 人类决定：用户 2026-06-17"直接动手"确认 Standard

## 备注
- 候选评估过程：先评 invisible_playwright / firecrawl / obscura / Scrapling / Webwright 五个，再评 crawl4ai。选 crawl4ai 自托管的关键理由——Apache-2.0（无 firecrawl 自托管的 AGPL 商用传染顾虑）+ 天生 LLM-native markdown + Docker HTTP 服务接入贴合现有 web_fetch。
- 仅后端 numind-server，无前端改动（agent 工具由 LLM 自动发现，无手动工具选择器）。
