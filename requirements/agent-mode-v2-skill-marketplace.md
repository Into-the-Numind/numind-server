# NDF S0 Requirement Card · `agent-mode-v2-skill-marketplace`

**Track**：Standard
**Feature ID**：`agent-mode-v2-skill-marketplace`（v2 第 3 个 feature，**v2 三件套之末**）
**起草日期**：2026-05-24
**起草人**：AI（基于 architecture-v1.md §4.3.10 预留接口 + 父账户 v2 终态目标）
**状态**：S0 草案
**硬依赖**：
- `agent-mode-v2-skill-as-artifact`（v2 #1，必须先 land develop 才能进 S4）
- `agent-mode-v2-skill-invocation`（v2 #2，软依赖 — 订阅功能落地不必等 #2，但 marketplace UI 引导文案默认 Skill 已可被 Agent 装载使用）
**阻塞**：无（v2 三件套之末）

---

## 1. 起因（Why now）

### 现状

v2 #1 把 Skill 升级为独立资产（独立 `skill` 表 + CRUD UI），但**Skill 仅在本租户内可见**。父账户 A 写了一个高质量"调研竞品"Skill，父账户 B 想用，只有两条路：
1. A 把 markdown 内容复制粘贴发给 B（脱敏靠人工，机构信息很可能漏，且 A 没有动力分享）
2. Numind 公司客服把"通用 Skill 模板"塞进平台预置库（10 个模板拍脑袋写出来，跟不上真实业务）

蓝本 architecture-v1.md §4.3.10 在 v1 阶段就预留了 `skill_template_marketplace` 表的 schema 草案 + 脱敏规则说明，但 v1 不交付。v2 #1/#2 完成后，**marketplace 是 v2 闭环的最后一块**——把"配置者社区"飞轮转起来。

### 父账户终态描述（2026-05-24 对话拍板）

> "机构父账户可以把自己设计的 Skill 脱敏发布到平台市场；其他父账户订阅后，副本拷贝进自己租户的 skill 表，再装载到 Agent 使用。"

**关键语义**：订阅是**复制**（订阅时刻打快照写副本到订阅方的 `skill` 表），**不是引用**。发布方后续编辑原 Skill 不影响已订阅方的副本——避免发布方推送 breaking change 把订阅方 Agent 跑挂。

### v2 三件套总览（本 feature 在末位）

| # | Feature | 范围 | 本 feature 关系 |
|---|---|---|---|
| 1 | `agent-mode-v2-skill-as-artifact` | DB 解耦 + 独立 CRUD + UI 菜单 + 数据迁移 | **硬依赖** — 必须先 land develop |
| 2 | `agent-mode-v2-skill-invocation` | 运行时 use_skill tool + system prompt 注入 + 子工具白名单扩展 + narration | 软依赖 — 不阻塞订阅落地，影响 UI 引导文案 |
| 3 | **agent-mode-v2-skill-marketplace** | 跨租户脱敏发布 / 浏览 / 订阅（=复制副本）/ 运营推荐 | **本 feature** |

**本 feature 唯一职责**：让 Skill 跨租户流通。三个动作：**发布**（脱敏 + 写市场表）/ **订阅**（复制副本到订阅方 skill 表）/ **运营推荐**（admin 打标 `is_platform_recommended=1`）。

---

## 2. 业务范围

### 关键术语统一

| 术语 | 含义 |
|---|---|
| **Marketplace Item** | `skill_marketplace` 表的一行，发布方上架的脱敏 Skill 副本 |
| **Subscribe** | 订阅方点"订阅" → 复制 marketplace item 的 `sanitized_body_md` + frontmatter 字段新建一行进订阅方租户的 `skill` 表，**同时**写一行 `skill_subscription` 关联两边 |
| **Unsubscribe** | 删 `skill_subscription` 行 + 软删除已复制的 `skill` 行（is_active=0）；订阅方已装载到 Agent 的 binding 跟随失效 |
| **Recommended** | admin 端打的 `is_platform_recommended=1` 标记，前端浏览页排在最前 + 加"官方推荐"徽章 |
| **租户** | 父账户（`parent_user_id IS NULL` 的 user 行）；marketplace 仅父账户可见，子账户无任何 marketplace 权限 |

### In scope（本 feature 必交付）

#### 2.1 DB 层

新增 2 张表：

| 表 | 用途 | 关键字段 |
|---|---|---|
| `skill_marketplace` | 市场上架的脱敏 Skill 副本 | id / publisher_user_id (FK → user.id) / source_skill_id (FK → skill.id, 发布方原 Skill id, 用于追溯) / name / description / when_to_use / sanitized_body_md (TEXT 脱敏后 body) / allowed_tools (JSON) / category_tags (JSON, ["销售","数据分析","SOP" 等]) / is_public (TINYINT, 1=上架, 0=下架) / is_platform_recommended (TINYINT, admin 端打标) / subscribe_count (INT, 订阅时 +1, 取消时 -1) / created_at / updated_at |
| `skill_subscription` | 订阅关系 | id / subscriber_user_id (FK → user.id, 订阅方父账户) / marketplace_id (FK → skill_marketplace.id) / cloned_skill_id (FK → skill.id, 复制到订阅方租户后新建的 skill 行 id) / subscribed_at；UNIQUE(subscriber_user_id, marketplace_id) 同一父账户对同一上架项只能订阅一次 |

**索引策略**：
- `skill_marketplace`: `idx_publisher (publisher_user_id, is_public)` / `idx_recommended (is_platform_recommended, subscribe_count DESC)`（运营推荐查询） / FULLTEXT(name, description, when_to_use) ngram parser（搜索）
- `skill_subscription`: `idx_subscriber (subscriber_user_id, subscribed_at DESC)` / `idx_marketplace (marketplace_id)`

**FK + CHECK**：
- `skill_marketplace.publisher_user_id` 必须指向父账户（`user.parent_user_id IS NULL`）— **biz 层校验，不进 CHECK constraint**（MySQL 8.0.16 才支持，避免依赖版本特性）
- `skill_subscription.subscriber_user_id != skill_marketplace.publisher_user_id`（不允许订阅自己发布的）— biz 层校验
- 发布方删除原 Skill（v2 #1 软删 is_active=0）**不级联**下架 marketplace；marketplace 是独立副本，DB 层无 ON DELETE。但 admin 后续可以手动下架。

迁移策略：双文件 migration（forward + rollback）+ AutoMigrate 注册到 `internal/numind/helper.go`。

#### 2.2 biz/marketplace 子包（**新建**）

| 文件 | 职责 |
|---|---|
| `marketplace/service.go` | 业务编排，包含 Publish / Browse / Subscribe / Unsubscribe / Recommend / ListMySubscriptions 公开方法 |
| `marketplace/sanitize.go` | 脱敏管道（详见 §2.4），输入：原 Skill body + frontmatter；输出：sanitized_body_md + sanitized frontmatter；调 `aiservice.Chat`（qwen-turbo）做实体识别 |
| `marketplace/clone.go` | 订阅时复制副本逻辑：从 `skill_marketplace` 行 + 调 `biz/skill.Service.Create` 给订阅方新建 skill 行（source_type='subscribed' 新枚举值，需在 #1 skill 表 source_type CHECK 加） |
| `marketplace/search.go` | 浏览页搜索 + 分类过滤 + 分页（FULLTEXT match against + tag JSON_CONTAINS + cursor pagination） |
| `marketplace/admin.go` | 运营推荐相关：SetRecommended（admin 端独占）+ ListByRecommendedRank |

**复用与不复用**：
- 复用 `biz/skill.Service.Create`（v2 #1 已实现）创建订阅方副本——不绕过 #1 的写路径，保证 source_type / version / history 机制一致
- 不复用 `biz/skill.Service.Delete`（订阅方取消订阅时直接走 biz/marketplace.Unsubscribe，内含级联清 binding 逻辑，与发布方主动删 Skill 是不同语义）

#### 2.3 数据流（订阅复制语义详解）

```
[发布方 A]                              [Marketplace]                          [订阅方 B]
skill (id=10, A 租户)
  │ Publish
  ▼
sanitize body                  → skill_marketplace (id=100,
                                  publisher=A, source_skill_id=10,
                                  sanitized_body_md=...)

                                  [B 浏览 → 点订阅]
                                  ▼
                                clone body → skill (id=200, B 租户,
                                                    source_type='subscribed',
                                                    body_md = mp.sanitized_body_md)
                                                    │
                                  写关联     ←─────┘
                                  ▼
                                skill_subscription (id=300,
                                  subscriber=B, marketplace_id=100,
                                  cloned_skill_id=200)
                                  +1 subscribe_count
```

**A 后续改了 skill id=10**：marketplace id=100 不变（已是独立副本），B 看到的 skill id=200 也不变。如果 A 想推送更新，需主动 Republish（创建新的 marketplace 行 id=101），B 看到的是"v2 版本"独立项，B 可选订阅新版（cloned_skill_id=201），旧版仍可继续用——**保护订阅方的稳定性**。

#### 2.4 脱敏管道（核心高风险逻辑）

输入：发布方原 Skill 的 `body_md` + `name` + `description` + `when_to_use` + `allowed_tools`。

**脱敏对象**：机构特定信息（PII / 竞品名 / 课程名 / 学员名 / 机构名）。

**实现路径（两阶段）**：

1. **正则黑名单（廉价）**：
   - 邮箱 / 手机号 / 身份证 / 银行卡正则替换为 `[已隐藏]`
   - 发布方租户下所有已配置的"竞品名黑名单"（来自 v1 `agent_permission_config.forbidden_competitor_names` JSON）→ 替换为 `[竞品]`

2. **LLM 实体识别（精准）**：
   - 调 `aiservice.Chat`（model=`qwen-turbo`，遵循 ai-service.md §0 硬规则）
   - prompt 模板（参考 `langfuse.FetchPrompt("skill-marketplace-sanitize-v1")`，5min cache + fallback）：
     ```
     你是脱敏助手。请识别以下 markdown 文本中的：
     - 具体人名（学员、员工）→ 替换为 [姓名]
     - 具体机构名（公司、学校）→ 替换为 [机构]
     - 具体产品名/课程名 → 替换为 [产品]
     保留行业通用术语和职能描述。返回脱敏后的完整 markdown。
     ```
   - 输出后**人工 review gate**：发布页 UI 强制显示 diff（左原文 / 右脱敏后），发布方必须手动确认才能提交
   - LLM 调用必须经过 `aiservice` 统一入口（不可裸调），自动 Langfuse generation + billing 计费（计入发布方 credits）

**失败兜底**：LLM 调用失败 → 发布按钮 disable + 提示"脱敏服务暂不可用，请稍后重试"。**不允许**绕过脱敏直接发布。

**估算成本**：单次脱敏约 2-5K tokens（Skill body 通常 <5KB），qwen-turbo 成本 <0.01 元；按计费规则进发布方 credits 账户。

#### 2.5 API 端点

**用户端 `/v1/marketplace/*`（新增 6 端点）**：

| Method | Path | 说明 | 鉴权 |
|---|---|---|---|
| POST | `/v1/marketplace/publish` | 发布我的某个 Skill（body: `{skill_id, category_tags, confirmed_sanitized_body}`，confirmed_sanitized_body 由前端 review gate 通过后回传） | 父账户 JWT |
| GET | `/v1/marketplace/list` | 浏览（query: `?q=关键词&category=销售&page=1&page_size=20&sort=recommended\|recent\|popular`） | 父账户 JWT |
| GET | `/v1/marketplace/:id` | 详情（含完整 sanitized_body_md 预览 + publisher 显示名 + subscribe_count） | 父账户 JWT |
| POST | `/v1/marketplace/:id/subscribe` | 订阅 = 复制副本进我租户 skill 表 | 父账户 JWT |
| GET | `/v1/marketplace/my-subscriptions` | 我订阅了哪些（含原 marketplace 信息 + 我租户的 cloned_skill_id） | 父账户 JWT |
| DELETE | `/v1/marketplace/:id/unsubscribe` | 取消订阅（id 是 marketplace_id，不是 subscription_id） | 父账户 JWT |

**管理端 `/v1/admin/marketplace/*`（新增 1 端点）**：

| Method | Path | 说明 | 鉴权 |
|---|---|---|---|
| POST | `/v1/admin/marketplace/:id/recommend` | 设置 is_platform_recommended（body: `{recommended: bool}`） | admin_token |

**所有用户端端点**：
- 父账户 JWT 鉴权（middleware 沿用 v1 的 user_token + 额外 biz 层检查 `parent_user_id IS NULL`）
- 子账户调用任何 marketplace 端点返回 403（错误码 `ErrChildAccountCannotAccessMarketplace`）

#### 2.6 前端（numind-web-v3）

新增 4 个 view 在 `src/views/marketplace/*`：

| 路由 | 页面 | 说明 |
|---|---|---|
| `/marketplace` | `MarketplaceBrowse.vue` | 浏览页（顶部搜索框 + 左侧分类 sidebar + 卡片网格列表，每卡片显示 name / description / publisher / subscribe_count / 推荐徽章 / 订阅按钮）。**例外**：本页是发现/浏览类，与管理端表格规则不冲突（CLAUDE.md ui-ux 硬规则 1 针对管理端 DataTable，前端 marketplace 是发现性质，参考 Notion/Figma 模板库） |
| `/marketplace/:id` | `MarketplaceDetail.vue` | 详情页（完整 sanitized_body_md markdown 渲染 + frontmatter 字段表格 + 订阅按钮 + 取消订阅按钮 + 已订阅 vs 未订阅状态） |
| `/marketplace/subscribed` | `MarketplaceSubscribed.vue` | 我的订阅列表（DataTable 布局，符合硬规则 1：列 = 名称/发布方/订阅时间/已装载 Agent 数量/操作） |
| `/marketplace/publish/:skill_id` | `MarketplacePublish.vue` | 发布页（左右 diff 视图：左原 body / 右脱敏后 body + 分类标签多选 + 二次确认按钮"我已确认脱敏内容无敏感信息"） |

**其它 UI 改动**：

- `src/views/config/skills/SkillEditor.vue`（#1 创建）顶部加 "发布到市场" 按钮 → 跳转 `/marketplace/publish/:id`
- `src/views/config/skills/SkillList.vue`（#1 创建）列表项加 "已发布到市场" 徽章（join `skill_marketplace where source_skill_id = skill.id and is_public=1`）
- 主菜单（`AppLayout.vue` 顶部菜单或侧栏）新增 "技能市场" 入口 → `/marketplace`
- `MarketplaceSubscribed.vue` 列出每个订阅的 cloned_skill，行内操作"装载到 Agent" → 跳转 `/config/agents/:agent_id/edit` 的 Skill 装载区块（v2 #1 已实现）

API 层 `src/api/marketplace.ts` + Pinia store `src/stores/marketplace.ts` 新建，遵循 frontend-state.md 规范。

### Out of scope（**明确不做**）

1. ❌ **运行时调用 Skill**：v2 #2 的事，本 feature 只管"流通"，订阅过来的 Skill 装载到 Agent 后运行时怎么用是 #2 范围
2. ❌ **付费 / 收益分成机制**：所有发布免费、所有订阅免费；脱敏 LLM 成本计发布方 credits（已经是计费机制兜底，不专门设计 marketplace 经济）
3. ❌ **prod 部署**：用户明令禁止 v2 三件套任何 prod 部署，收尾在 dev
4. ❌ **C 端学员看市场**：marketplace 是父账户工具，子账户/学员侧无入口、无权限
5. ❌ **Skill scripts/ 子目录跨租户共享**：v2 #1 不交付 scripts/ 字段（v2.5 评估），本 feature 同步不做
6. ❌ **跨租户 Skill 版本同步**：订阅是一次性复制，发布方更新后**不自动推送**到订阅方（订阅方主动 Republish 才会创建新 marketplace 行，订阅方可选订阅新版）
7. ❌ **发布方收到订阅通知 / 订阅数排行榜 / 评分评论**：v3 社交化功能
8. ❌ **管理端浏览市场全貌 / 批量下架**：单独 admin 端点先做 SetRecommended，批量管理 UI 留 admin-web 后续 feature
9. ❌ **平台预置 Skill 模板（publisher=NULL 或 platform）**：v1 SkillTemplate 表保留，平台预置走那条路；marketplace 仅父账户上架
10. ❌ **发布前自动跑 Skill 单元测试 / 沙箱验证**：脱敏后 markdown 可能语法 break，前端 review gate 显示 diff 让发布方人眼 verify；自动语法校验留 v2.5

---

## 3. 业务目标 / 验收标准

### 业务目标

让父账户能像逛模板市场一样发现别人写好的 Skill 并一键订阅使用；让有能力的父账户能脱敏发布自己的高质量 Skill。**前期靠 admin 端运营推荐保证质量**，飞轮转起来后逐步放开自由发布。

### 关键验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| AC-1 | 父账户 A 能在 SkillEditor 点"发布到市场" → 看到脱敏后 diff → 确认 → marketplace 出现一条上架记录 | Playwright E2E |
| AC-2 | 父账户 B 在 `/marketplace` 看得到 A 发布的项目，点详情 → 看完整脱敏 body markdown | Playwright E2E |
| AC-3 | B 点"订阅" → B 自己的 `/config/skills` 出现一条新 skill（source_type=subscribed）+ `/marketplace/subscribed` 出现一条订阅记录 | Playwright E2E + DB 验证 |
| AC-4 | B 取消订阅 → B 的 skill 软删除（is_active=0）+ subscription 行删除；B 之前装载到 Agent 的 binding 失效 | Playwright E2E + DB 验证 |
| AC-5 | A 发布前的脱敏 LLM 调用必须在 Langfuse 出现一个 generation（model=qwen-turbo，含 input + output + token usage）| Langfuse dashboard 检查 |
| AC-6 | 子账户调用任意 `/v1/marketplace/*` 端点 → 403 + 错误码 `ErrChildAccountCannotAccessMarketplace` | Go unit test |
| AC-7 | A 不能订阅自己发布的项目（publisher_user_id == subscriber_user_id 校验拦截）| Go unit test |
| AC-8 | A 同一 Skill 不能重复发布（同一 source_skill_id 已有 is_public=1 的 marketplace 行 → 报错 "已上架"，需先 Unpublish 再 Republish 或更新现有项） | Go unit test |
| AC-9 | B 对同一 marketplace 项重复订阅 → UNIQUE 约束拦截，前端按钮置灰显示"已订阅" | Go unit test + UI |
| AC-10 | admin 端 POST recommend → 浏览页 sort=recommended 排序生效 | Go unit test + Playwright |
| AC-11 | 浏览页搜索"销售调研" → FULLTEXT match 命中相关 Skill | Go unit test + Playwright |
| AC-12 | 脱敏 LLM 失败时，发布按钮 disable + 提示文案显示 | Go unit test（mock aiservice 失败） |
| AC-13 | 发布方原 Skill 编辑后，marketplace 行 sanitized_body_md 不变（独立副本语义） | Go unit test |

### 非功能性

- 脱敏 LLM 调用 p95 < 3s（qwen-turbo 通常 1-2s，<5KB body）
- 浏览页列表查询（含 FULLTEXT match）p95 < 200ms（5000 条数据 + 索引覆盖）
- 订阅复制原子性：clone 写 skill 表 + 写 subscription 表必须在同一事务，失败回滚
- frontmatter 解析失败时不阻塞发布（与 #1 一致），但前端 diff 视图标注"该字段未识别"
- 现有 e2e 测试零回归（marketplace 是纯新增功能，不动 v2 #1/#2 已有路径）

---

## 4. Triage

- **推荐轨道**：**Standard**
- **分类理由**：
  1. 数据库 schema 变更：**是**（2 张新表 + 双向 migration + FK + FULLTEXT 索引）
  2. 新增 API 端点：**是**（7 个新端点：用户端 6 + admin 1）
  3. 新外部服务集成：**否**（脱敏调 aiservice 已有 qwen-turbo 路由，无新 provider）
  4. 影响文件数：**>3**（biz/marketplace 5 文件 + controller + admin_controller + 2 router 注册 + 2 model + 2 migration + 前端 4 view + store + api + AppLayout 菜单 + SkillEditor 加按钮 + SkillList 加徽章 + 单测 + e2e）
  5. 高风险业务逻辑：**是**（**跨租户脱敏**——脱敏漏机构信息 = 数据泄漏；订阅复制错租户 = 跨租户污染；recommend 权限错位 = 平台公信力受损；UNIQUE 约束错 = 重复订阅 / 重复发布）

条件 1+2+4+5 触发 Standard 强制。

- **人类决定**：autopilot 默认通过（父账户已默认通过 v2 三件套，本卡片不再单独硬门禁停顿）

---

## 5. 风险

| # | 风险 | 概率 | 影响 | 缓解 |
|---|------|------|------|------|
| 1 | **脱敏 LLM 漏机构特定信息**（人名/竞品名漏识别）| 高 | 极高 | (1) 两阶段：正则黑名单兜底常见 PII + LLM 处理上下文敏感实体；(2) 发布前**强制人工 review gate**——前端 diff 视图，发布方手动确认；(3) Langfuse 监控漏识别 case，持续优化 prompt；(4) 上线后 admin 端给举报入口 |
| 2 | **订阅复制非原子导致脏数据**（写 skill 表成功但写 subscription 失败 → cloned_skill 孤儿）| 中 | 中 | gorm transaction + defer rollback + 测试覆盖 mock store 中途失败场景 |
| 3 | **跨租户数据泄漏**（订阅复制逻辑写错 user_id 导致复制到错租户）| 低 | 极高 | (1) biz 层强制从 JWT 取 subscriber_user_id 不接受参数；(2) 单测必覆盖跨租户场景（A 订阅，结果应在 A 的 skill 表）；(3) reviewer subagent 强制审查租户隔离 |
| 4 | **发布方滥用上架（垃圾 Skill 灌水）** | 中 | 低 | (1) 前期 admin 端 SetRecommended 控制可见性优先级；(2) v2.5 加发布数量限制 / 举报机制；(3) 当前阶段量级小，问题不紧迫 |
| 5 | **FULLTEXT ngram 中文搜索性能问题（>10k 条）**| 低 | 低 | parent_user_id 不参与 marketplace 查询（市场是全平台维度），ngram 索引能撑到 100k 量级；超出再上 ES |
| 6 | **脱敏 LLM 调用计费扣发布方 credits，但发布方可能没买套餐** | 中 | 中 | (1) 复用 Reserve/Reconcile 机制，无 credits 时直接 reject；(2) 父账户默认有 B2B grant 套餐，正常路径有 credits；(3) 前端发布按钮预检 credits |
| 7 | **取消订阅后已装载到 Agent 的 binding 失效，Agent 行为变化**| 中 | 中 | (1) 取消订阅前提示"将影响 N 个 Agent"；(2) 跟 v2 #1 软删 Skill 后 binding 处理一致（is_active=0 后运行时跳过该 skill）|
| 8 | **#1 land develop 延迟，本 feature S4 阻塞**| 中 | 中 | S3 plan 写完后跑硬阻塞检查脚本（git fetch origin develop + grep skill-as-artifact）；不满足 → ScheduleWakeup 1800s loop 最多等 7 天；超过 7 天 Pause and Ask |
| 9 | **admin 端"运营推荐"权限误用（任何 admin 可推荐，无审计）** | 低 | 中 | admin_token 已有审计 middleware；本 feature 复用，记录 SetRecommended 操作到现有 admin audit log |
| 10 | **B 订阅 A 的 Skill 后，A 删除原 skill (v2 #1 软删)，marketplace item 是否还能订阅？** | 低 | 低 | 设计决策：**marketplace item 独立存活**——A 删原 skill 不影响 marketplace 行（独立副本），但 admin 后续可手动下架。文档化在 §2.1 |

---

## 6. 仓库与估时

- **仓库**：`numind-server` + `numind-web-v3`（admin-web 不动；admin 端点挂在 numind-server 已有 admin_router.go）
- **估时**：2 周（含 S0-S6 全流程；S4 编码主体 ~7 工作日；脱敏管道是新逻辑需多轮调优）
- **worktree**：
  - numind-server: `/private/tmp/wt-agent-mode-v2-skill-marketplace-numind-server` ✓ 已建
  - numind-web-v3: `/private/tmp/wt-agent-mode-v2-skill-marketplace-numind-web-v3` ✓ 已建

---

## 7. S0 待解决项（留给 S1/S2）

1. **脱敏 LLM 模型选型**：默认 `qwen-turbo`（成本低）。是否需在 S2 评估 `qwen-plus` 提升识别准确率？倾向 turbo 起步，后续按 Langfuse 监控的漏识别率决定升级
2. **脱敏失败重试策略**：单次失败立刻报错，还是后台异步重试 3 次？倾向同步单次（用户主动操作场景下，异步失败不可见，体验差）
3. **marketplace item 唯一性键**：同一 source_skill_id 是否允许多个 is_public=1 的 marketplace 行（多版本并存）？S2 拍板。倾向 UNIQUE(source_skill_id) where is_public=1，更新 = 先下架再上架（语义清晰）
4. **订阅复制后版本绑定**：cloned_skill 是否记录"来源版本号"（marketplace_item 当前 sanitized_body 的版本）？倾向写 cloned_skill.description 元数据里"订阅自市场 / 版本 X / 订阅时间 YYYY-MM-DD"
5. **运营推荐排序公式**：`is_platform_recommended DESC, subscribe_count DESC, created_at DESC` 三层排序？S2 拍板
6. **subscribe_count 一致性**：实时 +1/-1 还是定期 cron job 重算？倾向实时（量级小），cron 作 weekly reconcile 兜底
7. **前端 diff 视图组件**：用现成 `vue-diff` 库还是自研？倾向 `vue-diff`（轻量，无大依赖）
8. **marketplace 项目"删除"语义**：发布方主动下架（is_public=0）还是硬删？倾向软下架（is_public=0），保留历史和已订阅方的 cloned_skill 链路追溯

---

## 8. 备注

- 本 feature 是 v2 三件套之末，**完成后 v2 闭环**（独立资产 → 运行时调用 → 跨租户流通）
- 与 #1/#2 并行进展中，**S4 硬阻塞**等 #1 land develop（依赖 skill 表 + biz/skill 包）
- #2 不阻塞本 feature 落地：订阅复制路径不依赖 use_skill 运行时；但 marketplace UI 引导文案需要假设 Skill 可被装载 → 装载是 #1 范围，已具备
- 脱敏 prompt 模板进 Langfuse 管理（参考 ai-service.md prompt 管理规范），后续可热更新优化
- 本 feature 不引入新 provider / 新 task profile（脱敏复用 qwen-turbo 现有路由）
