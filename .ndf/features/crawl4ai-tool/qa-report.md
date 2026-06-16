# QA Report — crawl4ai 网页读取工具（S5 本地验收）

## 验证策略（依 S3 plan T4）
后端 TDD（自动回归，mock crawl4ai，永久留库）+ **一次性本地 live 集成**（真实
crawl4ai:0.8.6 容器）。本 feature 纯后端、不涉前端/支付/权限 → 按 Rule 10 不要求
Playwright E2E。诚实声明：crawl4ai 服务连通性无持久自动回归测试（需起容器），未来靠
部署健康检查 + 手动重验。

## 验证环境
- 后端：本地（未起完整后端；用真实 crawl4ai 客户端代码直连容器验证，避免拉起 MySQL/Redis 全栈）
- crawl4ai：`unclecode/crawl4ai:0.8.6` 容器，经 `deploy/crawl4ai/docker-compose.yml` 启动，`/health` = `{"status":"ok","version":"0.8.6"}`
- 宿主：Docker 29.5.3，8.3GB RAM

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go vet | `go vet ./...` | PASS | 仅 sqlite-vec 第三方 cgo 警告（预存，非本改动） |
| golangci-lint（改动包） | `golangci-lint run ./internal/pkg/crawl4ai/... ./internal/numind/biz/agent/...` | PASS | 0 问题（`task lint` 全量失败仅因 golangci-lint 不在 PATH 的环境问题） |
| Go test（crawl4ai 包） | `go test -race ./internal/pkg/crawl4ai/` | PASS | 12 用例（含 hijack body-read 中断），race 净 |
| Go test（agent 包） | `go test -race ./internal/numind/biz/agent/` | PASS | 含 web_fetch 渲染/fallback/raw_direct/SSRF/软错误 + 既有用例无回归 |
| 全量 Go test | `go test ./...` | HAS_PREEXISTING_FAILURES | salesrag/credit/agent-controller 的 DB 依赖测试 0.00s 瞬失；在 baseline develop 565fc358（无本代码）复核**同样失败**=非本改动引入 |

## live 集成验证（一次性，throwaway 后删）

| 关键路径（S3 T4） | 方法 | 结果 |
|---|---|---|
| 1. 渲染路径读 JS 站非空正文（对照裸 HTTP 空壳） | `quotes.toscrape.com/js/`：裸 curl `class="quote"`=**0 条**；crawl4ai 渲染后 markdown 含 JS 加载引用文本+11 作者署名 | **PASS**（决定性） |
| 2. 真实 /crawl 响应形态与解析器吻合 | curl /crawl（用客户端确切请求体）→ `markdown` 为 object（`raw_markdown`/`fit_markdown`），`metadata.title`=Example Domain，`status_code`=200；example.com 的 `fit_markdown` 为空→**验证 fit→raw 兜底必要且正确** | **PASS** |
| 3. 真实 Go 客户端全链路 | gated throwaway test：`crawl4ai.Client.RenderMarkdown` 直连容器 → title=Example Domain, md_len=166, status=200，跑后删除 | **PASS** |
| 4. crawl4ai 不可用→fallback / 未配置→raw_direct | 单测（mock）已绿 | PASS（单测覆盖，未 live 重跑） |
| 5. SSRF 内网 URL 拒绝、renderer 不被调用 | 单测 `TestWebFetch_SSRFEnforced_RendererNotCalled` 绿 | PASS（单测覆盖） |
| 6. Langfuse span 带 fetch_path | 设计 + code review 确认；本地未起后端+langfuse 故未 live 跑 | DESIGN-VERIFIED（S6/线上可见时再目检） |
| 7. 容器网络隔离 | `docker inspect`：crawl4ai 仅挂专网 `crawl4ai_crawl4ai_net`，网内仅它一个容器，非 host/默认 bridge，能出公网 | 结构 PASS；跨容器"打不通内网"完整测试需后端容器在场→**S6 dev 做** |

## PRD 验收标准核对

| 验收标准（PRD §4） | 结果 | 备注 |
|---|---|---|
| crawl4ai 可用时 JS 站返回非空正文（对照裸 HTTP 空壳） | PASS | path 1 决定性验证 |
| crawl4ai 不可用/未配置时退回裸 HTTP 无回归 | PASS | 单测 fallback/raw_direct + 既有用例无回归 |
| SSRF 仍拦截 | PASS | 单测 + pre-flight 在渲染前 |
| Langfuse span 记录 fetch_path + 延迟 | DESIGN-VERIFIED | 代码已写全退出路径，live 目检留 S6/线上 |
| 工具输入契约不变 LLM 无感 | PASS | Name/InputSchema/输出 struct 未改 |
| 硬错误不杀 run | PASS | 单测 both-fail→returnSoftError，无 (nil,error) |

## 结论
**ALL_PASS**（live 关键路径 + 单测全绿；预存失败与本改动无关，已复核）。
渲染升级在真实容器上验证有效——同一 JS 页面旧裸抓为空、crawl4ai 渲染完整。

## 待 S6（dev）项
- 容器网络隔离的跨容器 reachability 实测（`docker exec crawl4ai curl <后端容器IP>` 应失败）
- Langfuse span `fetch_path` 线上目检
- dev 部署 crawl4ai 容器 + 配 `crawl4ai.base_url` + 重部署后端
