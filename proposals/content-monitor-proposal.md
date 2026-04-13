# 竞品内容监控与自动汇总 — 提案

## §1 方案概述 [客户可见]

为您搭建一套小红书竞品/同行内容自动监控系统，集成在现有 AI 工作台中。您可以自行添加要关注的博主，系统每天自动抓取他们的最新内容（包括视频自动转文字），AI 会对每条内容做选题分析和分类，然后生成日报/周报，定时推送到您指定的飞书群。

核心能力：
- **自主管理**：每个用户独立添加监控博主、设定抓取频率和报告时间
- **全自动流水线**：抓取 → 视频转文字 → AI 分析 → 简报生成 → 飞书推送，无需人工干预
- **灵活触发**：除定时自动执行外，可随时手动触发检查，支持选择特定博主批量抓取
- **智能分析**：AI 自动提炼选题方向、内容分类、热度评估，辅助内容策划

## §2 报价与周期 [客户可见]
- 预估工作量：22 天
- 报价：¥26,000（含税）
- 月度运维费：¥500/月（含服务器、代理 IP、LLM API、日常维护）
- 交付时间线：5 周
  - 第 1 周：核心抓取系统上线
  - 第 2 周：视频转文字 + AI 内容分析
  - 第 3 周：飞书集成
  - 第 4 周：定时调度 + 自动推送简报
  - 第 5 周：稳定性优化 + 验收

## §3 技术可行性 [AI 内部]

### 现有功能复用
- **DMXAPIClient**（DMXAPI deepseek-v3-2-251201）：复用现有 LLM 客户端，自带 Langfuse 追踪和 billing 计费。需从 `biz/salesrag/adapter/` 提取到 `internal/pkg/llm/` 作为共享基础设施。
- **billing 计费系统**：`billing.WithBilling()` 上下文注入，后台 cron 任务手动注入 userID + operation
- **FeaturePermission 中间件**：复用 sales-rag 的功能权限控制模式
- **Python 微服务模式**：参考现有 semantic_splitter.py (FastAPI) 的集成方式
- **定时任务基础**：在现有 server.go 调度架构上扩展

### 技术风险
| 风险 | 等级 | 缓解 |
|------|------|------|
| 小红书反爬机制升级 | 高 | xhs-service 独立更新，Cookie 池 + 代理 IP + 请求间隔 5s+ |
| 博主账号异常 | 中 | consecutive_failures 机制，5 次失败自动暂停 |
| FFmpeg 并发资源耗尽 | 中 | 信号量限制 2 并发 + 专用临时目录 |
| FunASR 模型加载慢 | 低 | Docker 常驻服务，预加载模型 |

### 技术验证状态
7 项核心技术全部验证通过（详见 `项目评估/需求5-竞品内容监控/mock验证报告.md`）：
MediaCrawler 可用性、FunASR 语音识别、FFmpeg 音频提取、DeepSeek API 内容分析、飞书 API、APScheduler 定时调度、端到端流程。

### 涉及仓库
- [x] numind-server（后端：biz/controller/store/model/migration + 调度器 + 外部服务集成）
- [x] numind-web-v3（前端：MonitorView + 8 个组件 + store + api）
- [ ] numind-admin-web（管理端 API 可通过现有 admin 前端框架调用，暂不新建页面）

### AI 可观测性（功能涉及 LLM 调用）
- [x] 涉及 LLM 调用：是
- Trace 起点：`biz/monitor/crawler.go` 的 `crawlBloggers()` 创建 `"monitor-crawl"` trace；`biz/monitor/briefing.go` 的 `generateBriefing()` 创建 `"monitor-briefing"` trace
- Generation 点：
  - `dmxapi-chat`：单条笔记 AI 分析（`analyzer.go` 调用 `DMXAPIClient.ChatCompletion`）
  - `dmxapi-chat`：简报生成（`briefing.go` 调用 `DMXAPIClient.ChatCompletion`）
- 关键元数据：`user_id`（trace 级别），tag `"monitor"`

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事
1. 作为运营人员，我需要添加要监控的小红书博主，以便系统自动跟踪他们的内容更新
2. 作为运营人员，我需要配置抓取频率和简报推送时间，以便按自己的工作节奏接收信息
3. 作为运营人员，我需要查看抓取到的笔记列表和详情（含 AI 分析结果），以便快速了解竞品动态
4. 作为运营人员，我需要查看自动生成的日报/周报简报，以便掌握选题趋势
5. 作为运营人员，我需要在飞书群中收到简报推送，以便在常用工具中获取信息
6. 作为运营人员，我需要手动触发抓取（支持选择特定博主，1-50 个），以便在需要时立即获取最新内容
7. 作为运营人员，我需要暂停/恢复对某个博主的监控，以便灵活管理关注列表
8. 作为管理员，我需要查看所有用户的监控情况，以便控制成本和排查问题

### 验收标准
- [ ] 用户可添加博主（输入 xhs_user_id，系统自动拉取头像/昵称）
- [ ] 用户可查看博主列表、详情，可软删除、可暂停/恢复监控
- [ ] 系统按用户配置的 cron 表达式准时触发抓取（误差 < 1 分钟）
- [ ] 用户可手动触发单个或批量博主抓取（1-50 个）
- [ ] 手动触发有冷却时间（检查 5 分钟，AI 分析 10 分钟）
- [ ] 新笔记按 (user_id, xhs_note_id) 增量去重入库
- [ ] 视频笔记自动下载 → FFmpeg 提取音频 → FunASR 转文字 → transcript 字段更新
- [ ] 每条笔记通过 DMXAPI deepseek-v3 生成 ai_summary / ai_topics / ai_category
- [ ] 简报按用户配置的 cron 表达式准时生成（日报/周报）
- [ ] 简报通过飞书 Webhook 推送到用户配置的地址
- [ ] 连续抓取失败 5 次的博主自动暂停，记录失败原因
- [ ] 所有 LLM 调用有 Langfuse trace + generation 记录
- [ ] 所有 LLM 调用有 billing 计费记录
- [ ] FeaturePermission("content_monitor") 权限控制生效
- [ ] 管理端可查看全局监控概览、跨用户博主/笔记/简报列表
- [ ] 管理端可查看和覆盖用户配置

### 边界情况
- 添加不存在的 XHS 用户 → 返回 `Monitor.XhsUserNotFound` (404)
- 重复添加同一博主 → 返回 `Monitor.BloggerAlreadyMonitored` (409)
- 无效 cron 表达式 → 返回 `Monitor.InvalidCronExpression` (400)，config 不保存
- PUT config cron 更新失败 → 事务回滚，config 不变
- xhs-service 不可达 → 抓取失败，记录 check_error，consecutive_failures++
- FunASR 不可达 → 视频转文字跳过，笔记仍入库但 transcript 为空
- 飞书 Webhook 未配置 → 跳过推送，不报错
- 该周期简报已存在 → 返回 `Monitor.BriefingAlreadyExists` (409)
- 同一用户同时 cron 触发和手动触发 → 并发安全，去重由 DB unique key 保证
- 服务重启 → cron job 从 DB 重建，无状态丢失

### 权限规则
- 功能权限：`FeatureKeyContentMonitor = "content_monitor"`
- 父用户（parent）自动拥有所有功能权限
- 子用户需被父用户授权 `content_monitor` 权限
- `GET /v1/monitor/check-permission` 注册在 FeaturePermission 中间件之外
- 管理端接口使用 admin_token 中间件，独立于用户端权限

### UI 行为规格
- 页面位置：`/monitor` 路由，AppSidebar 新增「竞品监控」入口
- 布局要求：
  - 主页面 Tab 切换：博主管理 | 内容流 | 简报 | 配置
  - 博主管理：表格布局（BloggerList），支持批量选择 + 触发抓取
  - 内容流：时间线展示（ContentFeed），支持按博主/时间筛选和排序
  - 笔记详情：弹窗（NoteDetail）
  - 简报：列表 + 详情双栏
  - 配置：表单面板，CronPicker 友好选择器（非直接输入 cron）
- 交互模式：
  - 添加博主：输入框输入 xhs_user_id + 确认
  - 批量抓取：勾选博主 → 点击「立即检查」
  - CronPicker：下拉选频率 + 时间选择器，自动转 cron 表达式
- 状态处理：
  - Loading：骨架屏占位
  - Empty：引导文案 + 「添加第一个监控博主」CTA
  - Error：错误信息 + 重试按钮
  - 冷却中：按钮禁用 + 倒计时提示
