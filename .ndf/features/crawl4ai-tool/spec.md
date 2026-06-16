# crawl4ai 网页读取工具 — 技术设计 spec

> 上游：`requirement.md`（S0）/ `proposal.md`（S1）。本 spec 是 S4 编码的权威依据。
> 仓库：numind-server（仅后端）。不涉及前端、不涉及 DB schema 变更。

## 1. 目标与方案（回顾）
把 agent 的 `web_fetch` 工具从"裸 HTTP GET"升级为"crawl4ai 真实浏览器渲染优先 + 裸 HTTP 兜底"，让它能读现代 JS 网页与挂基础反爬的站点。工具对 LLM 的输入/输出契约**完全不变**（仍是 `url` + 可选 `prompt` → `{title, content_md, ...}`）。crawl4ai 未配置或调用失败时自动退回现有裸 HTTP 路径，**零回归**。

明确排除：小红书等强反爬平台抓取（见 requirement §范围边界）。

## 2. 组件与文件清单
| 文件 | 动作 | 说明 |
|------|------|------|
| `internal/pkg/crawl4ai/client.go` | 新增 | crawl4ai HTTP 客户端（`RenderMarkdown`、`Configured`） |
| `internal/pkg/crawl4ai/client_test.go` | 新增 | 客户端单测（httptest mock crawl4ai） |
| `internal/numind/biz/agent/tool_web_fetch.go` | 改 | Execute 内部加渲染优先 + fallback 分支；新增 span 字段 |
| `internal/numind/biz/agent/tool_web_fetch_test.go` | 改 | 加渲染路径 / fallback / SSRF-still-enforced 用例 |
| `config_local.yaml` / `config_dev.yaml` / `config_qa.yaml` | 改 | 加 `crawl4ai.*` 配置块 |
| `config_prod.yaml` | **禁改**（硬规则）| prod 配置块由运维另加，文档化在 `deploy/` |
| `deploy/crawl4ai/docker-compose.yml`（或等价）| 新增 | crawl4ai 服务部署清单 + 网络隔离 |
| `deploy/crawl4ai/README.md` | 新增 | dev/prod 部署步骤 + prod 配置块样例 |

> `factory_platform.go` **无需改**：工具名/元数据不变（仍是 `web_fetch`），只是 Execute 内部实现升级。

## 3. crawl4ai 客户端契约（`internal/pkg/crawl4ai`）

### 3.1 为何独立包、不走 aiservice
crawl4ai 用 `f:"fit"` 启发式内容过滤，**不调用 LLM**（不用 `f:"llm"`），因此不属于 `.claude/rules/ai-service.md` 管辖的 LLM/embed/rerank/ocr/asr 调用，**不需要走 aiservice 入口**。它是一个网页渲染器，归入 `internal/pkg/crawl4ai` 独立基础设施包，与 `httpclient` 同级。

### 3.2 接口
```go
package crawl4ai

type RenderResult struct {
    Title      string
    Markdown   string
    StatusCode int  // 目标页面的 HTTP 状态（crawl4ai 透传）
}

type Client struct { /* base_url, token, http client, content_filter */ }

// NewClientFromConfig 读 viper crawl4ai.* 构造；base_url 为空返回 nil-ish（Configured()=false）
func NewClientFromConfig() *Client

// Configured 报告 base_url 是否非空（决定是否尝试渲染路径）
func (c *Client) Configured() bool

// RenderMarkdown 调 crawl4ai POST /crawl 渲染 targetURL，返回 markdown + title。
// targetURL 必须是调用方已做 SSRF 校验的安全 URL（本函数不重复校验）。
// 失败（超时/非200/success=false/空 markdown）返回 error，由调用方决定 fallback。
func (c *Client) RenderMarkdown(ctx context.Context, targetURL string) (*RenderResult, error)
```

### 3.3 crawl4ai HTTP 调用（已核对 v0.8.6 契约）
- 端点：`POST {base_url}/crawl`（同步）
- 请求体：
  ```json
  {
    "urls": ["<targetURL>"],
    "browser_config": {"type": "BrowserConfig", "params": {"headless": true}},
    "crawler_config": {"type": "CrawlerRunConfig", "params": {"cache_mode": "bypass"}}
  }
  ```
- 鉴权：默认 JWT 关闭。若 `crawl4ai.token` 非空则加 `Authorization: Bearer <token>`。
- 响应体（**防御性解析，markdown 字段跨版本有两种形态**）：
  ```json
  {"success": true, "results": [{
     "url": "...", "status_code": 200,
     "markdown": "..."  // 形态A: 纯字符串
     // 或 形态B: {"raw_markdown": "...", "fit_markdown": "..."}
     , "metadata": {"title": "..."}
  }]}
  ```
  - markdown 提取优先级：`fit_markdown` → `raw_markdown` → 字符串形态。三者皆空 → 视为失败（返回 error，触发 fallback）。
  - title 取 `results[0].metadata.title`，缺省空串。
  - **markdown 字段实现注意（S3 review P2）**：`markdown` 跨版本是 string 或 object，Go 侧用 `json.RawMessage` 接收后两段解析——先试 `json.Unmarshal` 为 string，失败再解为 `{fit_markdown, raw_markdown}` struct。不要直接假定单一形态。
- 健康检查：`GET {base_url}/health`（部署脚本用，非工具运行时用）。
- HTTP 客户端：`httpclient.NewClient(...)`，超时取 `crawl4ai.timeout_seconds`（默认 30s）。
  - **重试必须显式关（S3 review P2）**：`httpclient.Client.Do()` 在 `Request.RetryPolicy == nil` 时会套用 `DefaultRetryPolicy()`（MaxRetries=3）——Config 级 MaxRetries 被忽略。因此每个 crawl4ai `Request` **必须**显式 `RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0}`（照 `web_search.go:132` Tavily 写法）。渲染昂贵，失败即 fallback 而非重试。
  - **body 读取失败也算渲染失败（S3 review P2）**：200 OK 后 `io.ReadAll(resp.Body)` 仍可能失败 → `RenderMarkdown` 返回 error（不 panic），由调用方 fallback。

## 4. web_fetch Execute 改造（核心流程）

```
Execute(ctx, input):
  1. json.Unmarshal(input) → 失败: returnSoftError（不变）
  2. targetURL = validateFetchURL(in.URL, skipSSRFCheck)   // SSRF pre-flight，不变；对两条路径都生效
     失败: returnSoftError（不变，含 "use file_read for attachments" 路由提示）
  3. fetch_path 决策:
     a. if crawl4aiClient != nil && crawl4aiClient.Configured():
          res, err = crawl4aiClient.RenderMarkdown(ctx, targetURL)
          if err == nil && res.Markdown != "":
              fetch_path = "render"; title = res.Title; md = res.Markdown; → 第5步
          else:
              记录 fallback 原因; fetch_path = "raw_fallback"; → 进裸 HTTP 路径
        else:
          fetch_path = "raw_direct"; → 进裸 HTTP 路径
     b. 裸 HTTP 路径 = 现有逻辑（newSafeHTTPClient + html-to-markdown + simpleHTMLStrip fallback），原样保留
  4. （裸 HTTP 路径产出 title/md）
  5. 截断: md 超 100KB（webFetchMaxBytes，沿用现值）→ 截断 + truncated=true
  6. 构造 webFetchOutput{title, content_md, byte_size, truncated, fetched_at} → ToolResult
  7. Langfuse span EndSpan output 增加: fetch_path, crawl4ai_latency_ms, crawl4ai_status(若走过渲染)
```

**软错误不变量（沿用 `project_agent_tool_hard_error_kills_run`）**：crawl4ai 任何失败都**不**返回 Go error——要么 fallback，要么（裸 HTTP 也失败时）`returnSoftError`。Execute 永不因 crawl4ai 故障返回 `(nil, error)` 杀 run。

**工具注入 crawl4ai client**：`webFetchTool` 加字段 `crawl4ai *crawl4ai.Client`。`NewWebFetchTool()` 改为读 `crawl4ai.NewClientFromConfig()` 注入（client 内部 base_url 空时 `Configured()=false`，行为=纯裸 HTTP，零回归）。测试构造器仍可注入 nil（=纯裸 HTTP）或 mock。

## 5. SSRF 安全设计（关键）
渲染路径把 URL 交给在内网运行的 crawl4ai，必须防止它变成打内部服务的 SSRF 代理。两道防线：

1. **Go 侧 pre-flight（已有，复用）**：`validateFetchURL` + `checkIPSafe` 在调 crawl4ai **之前**对 targetURL 做 DNS 解析 + 内网/loopback/link-local/cloud-metadata 拦截。内网 URL 在交给 crawl4ai 前就被拒。
2. **crawl4ai 容器网络隔离（部署层，新增）**：crawl4ai 容器放在**仅出公网、不可达内部子网/DB/其他业务容器**的网络。这是渲染路径 SSRF 的**真正控制**——因为 Go 侧的 dial-time DNS-rebinding 防护（`newSafeHTTPClient` 的自定义 DialContext）只覆盖裸 HTTP 路径，覆盖不到 crawl4ai 内部发起的二次解析/请求。

> 残余风险（DNS rebinding 在渲染路径）：pre-flight 解析与 crawl4ai 实际抓取之间有时间窗。**另有重定向链风险（S3 review P2）**：Go 侧只校验初始 URL，但 crawl4ai 的浏览器可能跟随 HTTP 3xx 或 JS 跳转到内网地址——pre-flight 覆盖不到二次跳转。结论：**这两类都靠容器网络隔离兜底**（隔离网络里二次请求打不到内部服务），并在 spec/部署文档显著标注。S3 plan 的部署 task 必须**实测**网络隔离生效（见 plan T3），否则该 task 不算完成。

## 6. 配置块（viper）
```yaml
# config_local.yaml / config_dev.yaml / config_qa.yaml
crawl4ai:
  base_url: "http://localhost:11235"   # 空 = 跳过渲染，纯裸 HTTP（prod 默认态）
  token: ""                            # JWT，默认关
  timeout_seconds: 30
  content_filter: "fit"                # fit | raw（默认 fit=干净正文）
```
- 读取：`viper.GetString("crawl4ai.base_url")` 等。
- **prod**：禁改 `config_prod.yaml`。代码默认 base_url 空 → prod 上线后仍走裸 HTTP（=现状，无回归）；运维在 prod 部署 crawl4ai 容器后，于 prod 配置另加该块并重启，`web_fetch` 自动升级。prod 配置样例写入 `deploy/crawl4ai/README.md`。

## 7. 部署（dev 必做，prod 文档化）
- 镜像：`unclecode/crawl4ai:0.8.6`（**pin 版本**，勿用 latest）。
- 端口：11235；`--shm-size=1g`；容器需 ≥4GB RAM。
- dev：`deploy/crawl4ai/docker-compose.yml` 起服务 + 健康检查（`/health`）+ 资源限制 + 网络隔离（见 §5）。后端容器经 `crawl4ai.base_url` 指向它。
  - **资源限制用 Compose v2 语法（S3 review P2）**：`deploy.resources.limits.memory: 4g`，**不是** v1 的服务级 `mem_limit`（现代 compose 会静默忽略）。
- prod：同清单，运维手动部署 + 加配置（不进自动 CI，本 feature 不触 prod）。

## 8. Langfuse 可观测性
- 不新增 LLM generation（crawl4ai 非 LLM 调用）。
- 沿用现有 `tool.web_fetch.execute` span，EndSpan output 增加：`fetch_path`(render/raw_fallback/raw_direct)、`crawl4ai_latency_ms`、`crawl4ai_status`（走渲染时的 crawl4ai 返回码/错误）。
- **所有早退出路径也要带 fetch_path（S3 review P2）**：现有代码有多处早 `EndSpan`（请求构建失败、HTTP 4xx/5xx、body 读取失败等分支）。这些早退出的 span output 同样要 set `fetch_path`，避免可观测性出现空洞。
- 优雅降级：`if tc := langfuse.FromContext(ctx); tc != nil` 守卫不变。

## 9. PRD 验收标准 → 设计映射
| PRD 验收标准 | 本设计如何满足 |
|---|---|
| 渲染路径返回 JS 站非空正文 | §3 `/crawl` 真实浏览器渲染；§4 fetch_path=render |
| crawl4ai 不可用时退回裸 HTTP 无回归 | §4 fallback 分支 + §6 base_url 空跳过；裸 HTTP 逻辑原样保留 |
| SSRF 仍拦截 | §5 pre-flight `validateFetchURL` 对两路径生效 |
| span 记录 fetch_path + 延迟 | §8 |
| 工具契约不变 LLM 无感 | §4 输入输出 struct 不变，仅 Execute 内部 |
| 硬错误不杀 run | §4 软错误不变量 |

## 10. 边界与降级
- crawl4ai 超时/非200/success=false/空 markdown → fallback 裸 HTTP。
- 目标 URL 本身 4xx/5xx → 裸 HTTP 路径软错误（同现状）。
- 渲染 markdown 超 100KB → 截断 + truncated。
- crawl4ai 配置为空 → raw_direct。
- 渲染与裸 HTTP **都**失败 → `returnSoftError`（LLM 看到错误自纠，不杀 run）。
