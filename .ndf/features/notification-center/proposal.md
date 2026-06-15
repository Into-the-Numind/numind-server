# 通知系统（Notification Center）— 提案 + PRD

## §1 方案概述 [客户可见]
搭建一套官方 → 用户的单向通知通道：管理端发布官方公告，用户端通过顶部铃铛 + 通知中心查看，系统记录每个用户的已读/未读。公告支持"问卷"类型（单选/多选/评分/开放文本四种题型），用于低成本用户调研。管理端可看每条公告的已读率、已读/未读用户明细，问卷另含回收率与答卷聚合。整套系统通过 feature flag 默认关闭、独立数据表与独立路由实现强隔离，开发与合并不影响 prod 打 tag 上线其它功能。

## §2 报价与周期 [客户可见]
- 内部一人公司项目，N/A（无对外报价）。
- 交付节奏：单 session autopilot 推进至 S6（合入 develop、dev 可部署、flag 默认关），prod 上线由用户后续授权。

## §3 技术可行性 [AI 内部]
### 现有功能复用
- **双系统鉴权**：复用 user_token / admin_token 中间件（`internal/pkg/middleware`）。
- **统一响应**：`core.WriteResponse` + `errno` 错误码体系。
- **三层架构**：controller → biz → store，新增 `biz/announcement/`、`store` 接口。
- **前端 Markdown 渲染**：web-v3 既有 markdown 渲染能力（agent 输出复用）。
- **admin DataTable**：admin-web 既有 DataTable 组件（硬规则要求）。
- **前端 axios 封装**：`src/api/request.ts`（两端）+ Pinia setup store 模式。
- GORM 软删除、复合索引、FK CASCADE 模式（参考 credits 系统 T12）。

### 技术风险
- **R1 已读率分母**：'all' 受众的"目标用户总数"如何定义 → 取注册的非 admin 用户数。缓解：stats 接口实时 COUNT，明确口径写入 spec。
- **R2 GORM default 布尔坑**：`is_important`/`required` 等 `default` bool 字段创建时 false 被吞（见 `.claude/rules/database.md §6`）。缓解：用 `*bool` 指针 + Create 后 fixup，或不给这些字段加 DB default。
- **R3 feature flag 测试性**：用 guard 中间件读 config，flag off → 路由返回 404/feature-off，保证路由静态可测。
- **R4 并行 session 干扰**：manifest 直接提交 develop（只 add 单文件）；代码全程 worktree 隔离。
- **R5 问卷 JSON 字段**：选项/答案用 JSON 列，需定义稳定 schema 防止前后端漂移（spec 锁 JSON 结构）。

### 涉及仓库
- [x] numind-server（表 + migration + biz + store + controller + 路由 + feature flag）
- [x] numind-web-v3（铃铛 + 通知中心 + 详情/Markdown + 问卷作答）
- [x] numind-admin-web（公告管理 DataTable + 发布/问卷构建 + 已读统计 + 答卷聚合）

### AI 可观测性（如功能涉及 LLM 调用）
- [ ] 涉及 LLM 调用：否
- N/A（纯 CRUD + 统计，无 LLM 调用，无需 Langfuse trace）

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事
- 作为**管理员**，我需要发布官方公告（标题 + Markdown 正文，可选过期时间），以便向全体用户单向触达产品/运营信息。
- 作为**管理员**，我需要把公告设为"问卷"并配置单选/多选/评分/开放文本题目，以便做结构化用户调研。
- 作为**管理员**，我需要把公告先存为草稿、确认后再发布，以便避免误发。
- 作为**管理员**，我需要看到每条公告的已读率与已读/未读用户明细、问卷回收率与答卷聚合，以便量化触达与调研效果。
- 作为**用户**，我需要在顶部铃铛看到未读公告红点，点开通知中心浏览公告并区分已读/未读，以便不错过官方信息。
- 作为**用户**，我需要打开问卷类公告并提交答案（每份问卷只能提交一次），以便参与调研。

### 验收标准
- [ ] 管理端可创建 plain / survey 两类公告，survey 可加任意数量的四种题型题目。
- [ ] 公告有 draft / published / archived 三态；仅 published 且未过期（expires_at 为空或未到）的对用户可见。
- [ ] 用户端铃铛显示未读数；打开通知中心列出可见公告，已读/未读区分正确。
- [ ] 用户打开公告详情后该公告对该用户记为已读（幂等，不重复计数）。
- [ ] 问卷可提交，单选/多选/评分/文本答案正确落库；同一用户重复提交被拒绝（一人一答）。
- [ ] 管理端 stats：已读率 = 已读用户数 / 目标用户总数（'all' = 注册非 admin 用户数）；问卷回收率 = 答卷数 / 目标用户总数。
- [ ] 管理端可下钻：已读/未读用户列表分页；问卷每题聚合（选项计数、评分分布、文本答案列表）+ 可按用户查看其答卷。
- [ ] feature flag 关闭时：用户端铃铛不出现；后端公告路由返回 feature-off；现有功能零影响。
- [ ] 全程不改动任何现有表/端点；新增 migration 仅 CREATE 新表。

### 边界情况
- 用户拉取公告时无任何 published 公告 → 返回空列表 + unread_count=0（前端 empty 态）。
- 公告被 archive 或过期后，已读回执/答卷保留（历史统计仍可看），但用户端不再展示。
- 删除公告（软删）→ 级联软处理；stats 不再统计已删公告。
- 问卷 required 题未答 → 提交校验失败返回 errno。
- 重复标记已读 → upsert 幂等，read_at 保留首次。
- 并发提交同一问卷 → UNIQUE(announcement_id,user_id) 兜底，第二次返回"已提交"。
- 多选题答案为空数组 vs 未作答的区分（required 校验）。
- 评分题越界值（如 NPS 0-10 之外）→ 校验拒绝。

### 权限规则
- 发布/编辑/统计/删除：仅 admin（admin_token）。
- 浏览/已读/提交问卷：登录用户（user_token）。C 端/B 端子账户同等可见（V1 全员广播，不区分父子账户）。
- feature flag 关闭时两端能力均不可用。

### UI 行为规格
**用户端（web-v3）**
- 页面位置：顶部 header 铃铛图标（feature flag 控制显隐）+ 通知中心（下拉面板或 `/notifications` 路由）。
- 布局要求：通知中心为列表，未读高亮 + 红点；详情渲染 Markdown；问卷为表单。
- 交互模式：点击铃铛展开列表 → 点条目进详情 → 进入即标记已读 → 问卷填写后提交。
- 状态处理：loading（skeleton）/ empty（"暂无通知" + 插画）/ error（retry）/ success。

**管理端（admin-web）**
- 页面位置：侧边栏新增"通知中心"（公告管理）入口。
- 布局要求：列表用 **DataTable**（硬规则）；展示标题/类型/状态/发布时间/已读率。创建/编辑为表单 + 问卷动态题目构建器。
- 交互模式：新建 → 选类型 → （survey 则加题目）→ 存草稿/发布；行内 action：发布/归档/删除（销毁性操作走 ConfirmModal）/查看统计。
- 状态处理：列表与统计页均处理 loading/empty/error/success；表单 blur 校验。
- 统计页：已读率卡片 + 已读/未读用户分页列表 + 问卷每题聚合可视化 + 文本答案列表 + 按用户下钻答卷。
