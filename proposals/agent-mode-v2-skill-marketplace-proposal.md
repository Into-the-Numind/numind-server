# agent-mode-v2-skill-marketplace — 提案

## §1 方案概述 [客户可见]

让机构父账户能像逛模板市场一样，发现别人写好的 Skill 并一键订阅使用；让有能力的父账户能脱敏后发布自己的高质量 Skill 给全平台。

**三个动作**：
- **发布**：父账户在自己 Skill 列表点"发布到市场" → 系统自动脱敏（识别人名 / 机构名 / 产品名 / 竞品名 / PII）→ 显示左右 diff 让发布方人工确认 → 上架
- **订阅**：另一父账户浏览市场 → 看详情 → 点订阅 → Skill 副本自动复制进自己租户的技能库 → 装载到任意 Agent 使用
- **运营推荐**：Numind 运营在 admin 端给优质 Skill 打"官方推荐"标，浏览页排在最前

**产品定位**：从 v1 的"自己写自己用"→ v2 闭环的"配置者社区飞轮"。前期靠 admin 运营推荐保证质量，后期飞轮转起来逐步放开。

**带来的价值**：
- 配置者不用从零写 Skill（学习平台市场最佳实践）
- 平台沉淀通用业务能力（销售调研 / 数据分析 / SOP 等高频场景）
- 优质配置者获得平台曝光（虽然 v2 不做付费，但建立社区声誉）

## §2 报价与周期 [客户可见]

- 预估工作量：**14 工作日（2 周）**
- 报价：内部 feature，无对外报价
- 交付时间线：
  - 提案确认后立即启动 S2 设计
  - 硬依赖：v2 #1 `agent-mode-v2-skill-as-artifact` 必须先 land develop 才能进 S4 编码
  - 软依赖：v2 #2 `agent-mode-v2-skill-invocation` 不阻塞订阅落地，但影响 UI 引导文案
  - 目标 dev 上线：2026-06-07（按 #1 land 预期）

## §3 技术可行性 [AI 内部]

### 现有功能复用

- **`biz/skill.Service.Create`**（v2 #1 实现）：订阅时复制副本进订阅方租户，直接调用 #1 的 Create 接口（带 source_type='subscribed' 新枚举值），不绕过 #1 的写路径，保证 source_type / version / history 机制一致
- **`aiservice.Chat` 入口**（已有）：脱敏 LLM 调用走统一入口路由到 qwen-turbo，自动 Langfuse generation + billing 计费
- **`langfuse.FetchPrompt`**（已有）：脱敏 prompt 模板从 Langfuse 拉取，5 分钟 cache + fallback，可热更新优化
- **`agent_permission_config.forbidden_competitor_names`**（v1 #5 已有 JSON 字段）：脱敏正则黑名单读这个字段获取发布方租户已配置的竞品名
- **`internal/pkg/middleware/user_token`**（已有）：父账户 JWT 鉴权 middleware；本 feature biz 层再加一道 `parent_user_id IS NULL` 校验
- **`internal/pkg/middleware/admin_token`**（已有）：admin 端 SetRecommended 复用，自带审计 log
- **GORM transaction**（已有）：订阅复制原子性 = transaction 内调 skill.Service.Create + 写 skill_subscription
- **MySQL FULLTEXT ngram**（v1.5 #3 task 3.5 agent_message_search 表已用过）：浏览页中文搜索复用同一模式
- **前端 markdown 渲染**（v2 #1 SkillDetail.vue 已实现）：MarketplaceDetail.vue 复用同一组件
- **前端 diff 视图**：用 `vue-diff` npm 包（轻量，无大依赖），左原 body / 右脱敏后 body

### 技术风险

| 风险 | 缓解 |
|---|---|
| 脱敏 LLM 漏识别机构特定信息 → 数据泄漏 | 两阶段（正则黑名单 + LLM）+ 前端 diff 强制人工 review gate；Langfuse 监控漏识别 case 持续优化 prompt |
| 订阅复制非原子 → 脏数据（cloned_skill 孤儿）| GORM transaction + defer rollback + 单测覆盖中途失败 |
| 跨租户数据泄漏（订阅复制写错 user_id）| biz 层强制从 JWT 取 subscriber_user_id 不接受参数；单测必覆盖跨租户场景；reviewer 强制审查 |
| FULLTEXT ngram 中文搜索性能（>10k 条）| parent_user_id 不参与查询（市场是全平台维度），ngram 索引能撑 100k；超出再上 ES |
| 脱敏 LLM 调用计费扣发布方 credits，但发布方可能没买套餐 | Reserve/Reconcile 已有机制兜底；前端发布按钮预检 credits |
| #1 land 延迟阻塞本 feature S4 | S3 plan 写完后跑硬阻塞脚本（git fetch origin develop + grep skill-as-artifact）；不满足 ScheduleWakeup 1800s loop，最多等 7 天，超时 Pause and Ask |

### 涉及仓库

- [x] numind-server（biz/marketplace + controller + admin_controller + 2 router 改 + 2 model + 2 migration）
- [x] numind-web-v3（marketplace 4 view + api + store + AppLayout 菜单 + SkillEditor 加按钮 + SkillList 加徽章）
- [ ] numind-admin-web（不动 — admin 端 recommend endpoint 挂在 numind-server admin_router，admin-web 暂不做对应 UI，留下一 feature）

### AI 可观测性

- [x] 涉及 LLM 调用：是
- **Trace 起点**：`biz/marketplace.Service.Publish()` 调 `langfuse.CreateTrace("skill-marketplace-publish")`，附 user_id + skill_id
- **Generation 点**：
  - `biz/marketplace/sanitize.go::sanitizeWithLLM()` 调 `aiservice.Chat` 时记一个 generation，name=`sanitize-skill-body`，model=`qwen-turbo`，input=原 body + frontmatter，output=脱敏后 body，含 promptTokens + completionTokens
  - 失败时也记 generation（output={error: ...}），便于后续 prompt 调优
- **Span 点**：
  - `biz/marketplace/sanitize.go::regexBlacklist()` 包一个 span，name=`sanitize-regex-stage`（非 LLM 子操作）
  - `biz/marketplace/clone.go::cloneToSubscriber()` 包一个 span，name=`marketplace-subscribe-clone`，附 subscriber_user_id + marketplace_id + cloned_skill_id
- **关键元数据**：trace 上附 user_id (发布方) / skill_id / marketplace_id；generation 上附 sanitize_stage="regex" 或 "llm"

## §4 产品需求定义 — PRD [AI 内部 — 不要为可读性简化]

### 用户故事

1. **发布方故事**：作为父账户 A，我有一个写得很好的"销售调研"Skill，我想分享给平台其他机构使用，但不希望泄漏我们公司名 / 客户名 / 内部产品名。我需要系统自动脱敏后让我人工 review diff，确认无敏感信息后再上架。
2. **订阅方故事**：作为父账户 B，我刚接入平台不知道怎么写 Skill，我想浏览市场看别人怎么做的，找到合适的一键订阅进我自己的技能库，再装载到我的 Agent 使用。
3. **运营推荐故事**：作为 Numind 运营 admin，我看到市场上某个 Skill 写得特别好，我想给它打"官方推荐"标记，让浏览页排在最前 + 显示推荐徽章。
4. **取消订阅故事**：作为父账户 B，我订阅了某个 Skill 但发现不适用，我想取消订阅，且要清晰知道这会影响我的哪些 Agent。
5. **下架故事**：作为父账户 A，我想把某个上架的 Skill 下架（不希望新人订阅），但保留已订阅方的副本不受影响。
6. **更新故事**：作为父账户 A，我对发布过的 Skill 做了改进，我想推送新版本到市场，订阅方可以选择订阅新版（旧版仍可用）。
7. **搜索故事**：作为父账户 B，我想在浏览页搜"销售"关键词或筛选"销售"分类，看到所有相关 Skill。
8. **防滥用故事**：作为系统，我不能让 A 订阅自己发布的 Skill（无意义）；不能让 B 重复订阅同一项目（UNIQUE 约束）；不能让子账户访问市场（仅父账户）。

### 验收标准

复用 S0 卡 §3 的 AC-1..AC-13，逐条对应：

- [x] AC-1：发布方点"发布到市场" → 看到脱敏后 diff → 确认 → marketplace 出现一条上架记录（E2E）
- [x] AC-2：另一父账户在 `/marketplace` 看得到上架记录，详情显示完整脱敏 body markdown（E2E）
- [x] AC-3：点"订阅" → 自己的 `/config/skills` 出现新 skill（source_type=subscribed）+ subscription 行写入（E2E + DB）
- [x] AC-4：取消订阅 → cloned skill 软删除 + subscription 删除 + binding 失效（E2E + DB）
- [x] AC-5：发布前脱敏 LLM 调用在 Langfuse 出现 generation（Langfuse dashboard）
- [x] AC-6：子账户访问任意 marketplace 端点返回 403（unit test）
- [x] AC-7：发布方不能订阅自己发布的项目（unit test）
- [x] AC-8：同一 source_skill_id 不能重复发布（unit test）
- [x] AC-9：同一父账户对同一 marketplace 项不能重复订阅（unit test + UI 按钮置灰）
- [x] AC-10：admin SetRecommended → 浏览页 sort=recommended 排序生效（unit test + E2E）
- [x] AC-11：浏览页搜索关键词 → FULLTEXT 命中（unit test + E2E）
- [x] AC-12：脱敏 LLM 失败 → 发布按钮 disable + 提示文案（unit test mock 失败）
- [x] AC-13：发布方编辑原 Skill 后，marketplace 行 sanitized_body 不变（独立副本语义验证）

### 边界情况

- **脱敏后 markdown 语法 break**：LLM 输出可能破坏 markdown 结构（如错改 `*` 数量）→ 前端 diff 视图标注"格式异常"+ 发布方决定是否继续；不阻塞流程
- **发布方租户没配置竞品名黑名单**：跳过正则黑名单的竞品名阶段，仅跑 PII 正则 + LLM
- **超长 body（>50KB）**：超出 #1 的 Skill body 上限直接拒绝（前置 #1 校验）
- **发布方原 Skill 已被软删**：发布按钮 disable + 提示"原技能已删除，无法发布"；但市场上已有 marketplace 行不受影响（独立副本）
- **发布方 credits 不足**：发布按钮 disable + 提示"积分不足，无法调用脱敏服务"；引导购买
- **发布方未确认 diff 就关闭页面**：marketplace 行不写入，无副作用
- **订阅时刻发布方刚下架**：transaction 内查 is_public=1，否则报错"该项已下架"
- **订阅时刻订阅方 credits 不足**：订阅本身不消耗 credits（无 LLM 调用），不阻塞
- **取消订阅时 cloned_skill 已被订阅方手动删除**：subscription 行先于 cloned_skill 删除，allow nil
- **admin SetRecommended 设了不存在的 marketplace_id**：返回 404
- **并发订阅同一项**：UNIQUE(subscriber_user_id, marketplace_id) 拦截，第二个请求返回 409 conflict
- **FULLTEXT 搜索空查询**：返回全部（按 sort 排序），不报错
- **浏览页过滤分类但分类不存在**：返回空列表（不报错）

### 权限规则

| 角色 | marketplace 权限 |
|---|---|
| 父账户（`parent_user_id IS NULL`）| 所有 `/v1/marketplace/*` 端点可访问；只能 publish 自己的 skill；只能 unsubscribe 自己订阅的 |
| 子账户（`parent_user_id IS NOT NULL`）| 任意 marketplace 端点 403（错误码 `ErrChildAccountCannotAccessMarketplace`）|
| 学员（C 端 user）| 无 marketplace 入口（前端菜单根据 user 类型条件渲染）|
| admin | 通过 `/v1/admin/marketplace/:id/recommend` SetRecommended（admin_token 鉴权）；admin 端无市场浏览 UI（v2 不做）|

### UI 行为规格

#### 浏览页 `/marketplace`

- **页面位置**：顶部菜单（与"我的 Agent"、"我的技能"并列）"技能市场"入口
- **布局要求**：顶部搜索框 + 排序下拉（推荐/最新/最热）+ 左侧分类 sidebar + 右侧卡片网格列表
  - 卡片显示：name / description（截断 2 行） / publisher 显示名 / subscribe_count / 推荐徽章（is_platform_recommended=1 时）/ 订阅按钮（未订阅 = 蓝色 / 已订阅 = 灰色"已订阅"）
  - **设计例外说明**：本页是发现/浏览类，与 CLAUDE.md ui-ux 硬规则 1（管理端必须 DataTable）不冲突 — 硬规则 1 针对管理后台数据管理，前端 marketplace 是发现性质，参考 Notion / Figma 模板库形态
- **交互模式**：搜索框 debounce 300ms 触发搜索 / 分类点击立即过滤 / 排序切换立即重排 / 卡片点击进详情 / 订阅按钮点击 → 订阅 modal 二次确认
- **状态处理**：
  - loading：骨架屏（6 个卡片占位）
  - empty：无搜索结果时显示"未找到相关技能，试试别的关键词"+ 清空搜索 CTA
  - error："加载失败，请刷新重试" + 重试 CTA

#### 详情页 `/marketplace/:id`

- **页面位置**：从浏览页或我的订阅页点击进入
- **布局要求**：顶部 breadcrumb + 标题 + publisher + subscribe_count + 推荐徽章 + 订阅/取消订阅按钮 + 分类标签；下方 frontmatter 表格（name / description / when_to_use / allowed_tools）+ 完整 sanitized_body_md markdown 渲染
- **交互模式**：订阅按钮二次确认 modal → 调 POST subscribe → 成功后按钮变"已订阅"+ toast；取消订阅同样二次确认（提示影响 N 个 Agent）
- **状态处理**：4 状态俱全

#### 我的订阅 `/marketplace/subscribed`

- **页面位置**：从 `/marketplace` 页面右上角"我的订阅"入口
- **布局要求**：DataTable 布局（符合硬规则 1：列 = 名称 / 发布方 / 订阅时间 / 已装载 Agent 数量 / 操作）
- **交互模式**：行内操作"取消订阅"二次确认 / "查看详情"跳详情页 / "装载到 Agent"跳 `/config/agents/:agent_id/edit` 的 Skill 装载区块（v2 #1 已实现）
- **状态处理**：empty 状态显示"还没有订阅的技能，去市场逛逛 →"

#### 发布页 `/marketplace/publish/:skill_id`

- **页面位置**：从 `/config/skills/:id`（SkillEditor）顶部"发布到市场"按钮进入
- **布局要求**：顶部标题"发布到市场" + skill 元信息（name / description） + 分类标签多选（销售 / 数据分析 / SOP / 客服 / 其他）+ 左右 diff 视图（左原 body / 右脱敏后 body，vue-diff 渲染高亮差异）+ 底部"我已确认脱敏内容无敏感信息"checkbox + "发布"按钮（checkbox 未勾选 = disable）
- **交互模式**：
  - 页面加载时调 POST `/v1/marketplace/sanitize-preview`（新增 dry-run 端点 - 见 §4 端点表补充）→ 返回脱敏后 body 不写库
  - 用户勾选 checkbox + 点发布 → 调 POST `/v1/marketplace/publish` body 带 confirmed_sanitized_body 回传 → 写库
- **状态处理**：
  - loading（调脱敏 LLM 中）：右侧 diff 区显示 spinner + "正在脱敏中..."
  - error（LLM 失败）：右侧显示"脱敏服务暂不可用，请稍后重试" + 重试按钮；发布按钮 disable
  - success：diff 渲染完成，checkbox 可勾选

#### 其它 UI 改动

- `SkillEditor.vue` 顶部加 "发布到市场" 按钮（已发布时按钮变"管理上架"跳详情页）
- `SkillList.vue` 列表项加 "已发布到市场" 徽章
- `AppLayout.vue` 顶部菜单（父账户视图）新增 "技能市场" 入口

#### §4 端点表补充

S0 卡 §2.5 中提到 6 个用户端端点 + 1 admin 端点，PRD 阶段补充第 7 个用户端端点：

| Method | Path | 说明 | 鉴权 |
|---|---|---|---|
| POST | `/v1/marketplace/sanitize-preview` | dry-run 脱敏（body: `{skill_id}`），返回 sanitized_body_md 不写库；用于发布页 diff 预览 | 父账户 JWT |

这样发布页可先预览脱敏结果让用户 review，再正式 publish。S0 卡 §2.5 隐含包含此功能（"由前端 review gate 通过后回传"），S1 显式补端点。S2 spec 锁完整契约时一并定义。
