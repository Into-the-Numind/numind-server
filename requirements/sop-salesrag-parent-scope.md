# SOP / 销售智能体 父账户归属隔离

## 来源
- 提出人：用户（产品 owner）
- 提出日期：2026-05-18

## 需求描述

原话：

> prod 环境下的附件中的这些 SOP 或者 chatbot，是完全不区分父用户的，所有不同的父用户都可以看到。但其实需要把这四个 SOP 或 chatbot 变成某个父用户下的，也就是和其它的 SOP 或 chatbot 一样。因为附件里的这几个都是为莫小派（父用户 user id 为 30）打造，而不是另一个机构用户，所以肯定不能给另一个机构用户看到。

涉及的 4 个实体（prod 实测，2026-05-18）：

| 类型 | id / feature | 当前归属字段 | 备注 |
|------|--------------|------------|------|
| sop_template | id=1（AI文稿创作：流量选题口播稿） | `creator_user_id=NULL` | published，跨父账户全局可见 |
| sop_template | id=2（AI朋友圈：深度思考文案） | `creator_user_id=NULL` | published，跨父账户全局可见 |
| sop_template | id=4（小红书图文生成智能体） | `creator_user_id=30` | 字段已对，但 SQL 缺过滤所以照样泄露 |
| SalesRAG feature | `feature_key='sales_agent'` | 父账户硬 bypass | 所有 `parent_user_id IS NULL` 的用户自动通过 |

结构化描述：

平台必须按**父账户**隔离 SOP 模板 + 销售智能体。**多租户核心不变量**：每个资源都有一个 "owner tag = 父账户 id"，SQL 在每次列表查询时按当前用户所属父账户自动过滤。**没有任何人工管理层**（无 admin UI、无授权 API）。

当前 prod 上 4 个实体跨父账户全局可见的真实原因有两条独立路径，本需求一并修复：

1. **SOP 列表 SQL 缺 owner 过滤（结构性缺口）**
   - 当前 `store.Sop.ListVisibleTemplates` 只过滤 `status='active' AND publish_status='published'`，**完全没有** `creator_user_id` 或父账户关联过滤
   - 修复后：SOP 列表只返回 `creator_user_id = 当前用户所属父账户 id` 的行（语义与 chatbot.user_id 一致）
2. **3 行历史数据归属修复**
   - sop_template id=1 和 id=2 的 `creator_user_id` 从 `NULL` 改为 `30`
   - sop_template id=4 已经是 `creator_user_id=30`，无需变更（验证一致性）
3. **销售智能体 owner tag 补齐（结构性缺口）**
   - 销售智能体目前**没有 owner 字段**——它是前端硬编码磁贴，后端用 `customer.HasFeaturePermission` 控制可见性
   - 该函数里有一行 `if user.ParentUserID == nil → return true`，导致**所有父账户**自动看到磁贴
   - 修复后：给"销售智能体"打上 owner tag。落点 = **新建小表 `sales_agent_owner`**（一列 `parent_user_id` 作为主键），每行表示"该父账户拥有销售智能体卡片"
   - 选 sales_agent_owner 独立小表而不是 user.has_sales_agent 列：未来若新增同类"特殊智能体卡片"，独立表更易扩展，user 表不会被一堆 boolean 列污染
   - 修改 `customer.HasFeaturePermission` 的行为：在 `featureKey == 'sales_agent'` 分支移除父账户硬 bypass，改为查 sales_agent_owner 表；**其他 featureKey 保持现状**（content_monitor / self_service_config 是系统级功能，保留父账户 bypass）
   - 父账户访问 sales_agent：sales_agent_owner 表存在 `parent_user_id = user.id` 行 → 通过
   - 子账户访问 sales_agent：父账户存在 sales_agent_owner 行 ✓ **AND** `user_feature_permission` 表存在该子账户的 `feature_key='sales_agent'` 行 ✓（48 行原数据保留，Layer 1 子用户授权不变）

**本需求范围明确不含 admin 管理层**：
- 没有 admin UI 用于"批准哪些父账户能用销售智能体"
- 没有 admin API 用于授权 / 撤销
- 未来如果有第 2 个机构需要销售智能体，**直接改数据库**（手工 INSERT 一行）
- 这是产品 owner 的明确决策（2026-05-18 对话），不是技术 debt

## 业务目标

1. **数据隔离正确性**：第 2 个真实机构入驻 prod 时立刻可用，不需要额外人工筛查"哪些是莫小派的"
2. **修复结构性 bug**：这不只是 4 行数据修复，是 SOP 列表 + SalesRAG feature 两个层级的结构性缺口；现在补干净，未来补会留遗留数据
3. **保持 user 30 及其子用户体验不变**：莫小派父账户 + 全部子用户对 3 个 SOP + 销售智能体的访问行为绝对不能因这次修复改变（这是硬约束，验收必查）
4. **B2B2C 商业模型基础**：父账户级隔离是 SaaS 多租户的必备前提，本需求是从单租户向多租户结构演进的关键一步

## 用户补充的关键事实（2026-05-18 对话）

> admin 创建的东西都是 demo，非 admin 创建的，都是 user 30 创建的，或者是用硬编码为 user 30 及其子用户创建的，都不能影响使用。

含义拆解：

1. **admin (id=1) 的所有数据都是 demo**：
   - chatbot_config id=8 (user_id=1) → demo 数据，本需求**不需要保证 admin 的使用体验**
   - user_feature_permission 表里 parent_user_id=1 的子账户授权记录（id=19, 42, 47, 48）→ demo 数据，不需要单独保留
   - admin 登录后看到空工作区是可接受的
2. **非 admin 的内容必须照常工作**：
   - user 30 自创的 SOP（id=4）+ user 30 子账户们 + 硬编码挂在 user 30 名下的内容（id=1, 2 修复后） = **生产数据**，本次修复完成后 user 30 及其全部子用户的访问行为**必须**与修复前完全一致
3. **S1 两个待探索 case 直接回答**：
   - "admin 登录应该看到什么" → 看到空 / demo 内容均可，不重要
   - "chatbot id=8 怎么处理" → demo，不动，不在本需求范围内

## 平台历史背景（用户 2026-05-18 复述）

理解本需求时需要的产品历史：

1. **起点**：早期硬编码做了 3 个东西——2 个 SOP（小红书图文、AI 文稿创作 / 实际还有 AI 朋友圈）+ 1 个销售智能体。这 3 个都是"先写代码做出来"的内置功能，不是用户自助搭建的产物。
2. **演进**：后来客户要求平台支持**自助搭建**，做了 self-service config 功能，分两类**资源**：
   - SOP（工作流模板）
   - chatbot（智能体）
3. **回头分类**：原本硬编码的 3 个东西被纳入这套资源体系——2 个 SOP 进 SOP 资源池，销售智能体进 chatbot 资源池
4. **销售智能体的特殊性**：从外观/分类看它是 chatbot 类目下的一张卡片；但底层架构跟普通 chatbot 不同（独立的 sales_message / sales_session 表、独立 UI 路由 `/sales`、独立知识库机制）。**"分类上是 chatbot，物理上独立存储"。**

**所以本需求的统一规则是**：

> 不管是 SOP 还是 chatbot（含特殊 chatbot = 销售智能体），谁创建/拥有它，只有他和他的子账户看得到；其他租户及其子账户看不到。

这条规则统一适用于 3 类资源，差别只在"owner 字段落在哪张表"：

| 资源 | owner 字段位置 | 当前状态 |
|------|-------|-------|
| SOP | `sop_template.creator_user_id` | 字段在，2 行历史数据 NULL 待修 + list SQL 无过滤待补 |
| 普通 chatbot | `chatbot_config.user_id` | 字段在 + list SQL 已过滤，**正确工作** |
| 销售智能体 | **当前无 owner 字段** | 待新建 sales_agent_owner 表 |

## 优先级

**高**。当前 prod 单租户偶然没暴露，第 2 个机构入驻立刻漏。属于"潜伏性高危结构缺口"。

## Triage

- **推荐轨道：Standard**
- **分类理由**（5 条标准逐条）：
  1. 数据库 schema 变更：**是**（新建 `sales_agent_owner` 小表 + `sop_template.creator_user_id` 加索引）
  2. 新 API 端点：**否**（移除父账户硬 bypass，纯改 biz / store 层；admin UI 不在范围内）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（numind-server 仅：store/sop + biz/sop + store/customer + biz/customer + model/sales_agent_owner + store/sales_agent_owner + migration + 测试 ~ 9-11 文件；numind-admin-web 不涉及；numind-web-v3 不涉及）
  5. 高风险业务逻辑（支付/权限）：**是**。跨父账户数据可见性，错则要么 user 30 子账户看不到自家 SOP（业务停摆），要么未来其他机构看到莫小派内容（数据泄露）
- **人类决定**：**确认 Standard**（用户在 Triage 对话中已选 option C；2026-05-18 后续对话纠偏：去掉 admin 管理层，纯系统过滤）

## 产品决策（来自 Triage 对话）

用户在 Triage 阶段选择了语义 **C**：

> **C. 严格父账户隔离 + 保留未来扩展可能性**
>
> 当前实现 = 选项 A 的语义（每个 SOP/feature 严格属于某个父账户，无"公共/官方模板"概念，4 个实体全部收归 user 30）。代码结构留口给未来选项 B（如果有商业需要再加 `is_official` / 平台级模板概念）。

具体表现：
- 不引入 `is_official` 字段
- 不引入"平台级模板"概念
- SOP 列表 SQL 永不返回 `creator_user_id IS NULL` 的行（后续防御性，4 行历史数据迁移后理论上无 NULL 行）
- 未来要加 option B 语义时：sop_template 加 `is_official` 字段 + list 查询变为 `WHERE creator_user_id IN (...) OR is_official=true`，是局部 ALTER + 单点修改，结构上为这个扩展留口

S1 office-hours 阶段进一步确认：用户提出 Option B（custom_agent 通用表 + 前端新类别"自定义智能体"，3-4 天 13-15 文件跨两仓）vs AI 提的 Option A（sales_agent_owner 小表，1 天 ~9 文件 仅 numind-server）。**用户拍板 A**（YAGNI 友好选择，3-6 个月内无第 2、3 个自定义智能体计划；未来真有 N>1 时升级 B 成本不高）。

## 与现有功能的关系

**重要：此功能是 SOP / chatbot / sales-agent 权限体系的第 0 层。**

| Layer | 功能 | 语义 | 状态 |
|-------|------|------|------|
| **Layer 0 (本需求)** | 父账户归属 | 跨机构隔离（哪些 SOP 属于本机构） | 待实现 |
| Layer 1 | `sop-chatbot-visibility-scope`（2026-05-14 上线） | 子用户级可见性（本机构内哪些子账户可见） | 已上线 |
| Layer 2 | `child-run-permission`（2026-04-20 上线） | 子用户运行授权（403 gate） | 已上线 |

**三层 gate 串行**：Layer 0 决定哪些 SOP 属于本机构 → Layer 1 决定哪些本机构子账户能在列表看到 → Layer 2 决定子账户能否实际运行。

**关键不变量**：
- Layer 0 过滤掉的 SOP，Layer 1/2 的授权对它无意义
- Layer 1/2 已有 48 行 `user_feature_permission` 记录（全部 parent=30 或 parent=1），数据形态不变；只在 Layer 0 之上独立加父账户级 feature 授权
- chatbot_config 列表查询已经按 `user_id = ownerID`（ownerID = 父账户）做过 Layer 0 过滤，**SOP 缺这层 = bug**，本需求补齐

## 数据迁移清单（prod 实测）

S4 编码完成后必须包含以下迁移：

```sql
-- 1. 收归 2 行 NULL 创建者的 SOP 到 user 30
UPDATE sop_template SET creator_user_id=30 WHERE id IN (1, 2);
-- id=3（草稿）和 id=4（小红书图文，已发布）已经是 creator_user_id=30，无需变更

-- 2. 新建销售智能体 owner 表 + 给父账户 user 30 打 tag
CREATE TABLE IF NOT EXISTS sales_agent_owner (
  parent_user_id INT UNSIGNED PRIMARY KEY,
  created_at DATETIME(3) NOT NULL,
  CONSTRAINT fk_sao_parent FOREIGN KEY (parent_user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
INSERT IGNORE INTO sales_agent_owner (parent_user_id, created_at)
  VALUES (30, NOW(3));
-- 注意：admin (id=1) 默认不插入（admin 数据全部为 demo，不需销售智能体磁贴）
-- 未来其他机构需要时手工 INSERT，没有 admin UI
```

**故意不迁移**：
- 不修改 `user_feature_permission` 表的 48 行子账户 sales_agent 授权记录（Layer 1 子用户授权数据不变）
- 不动 admin (id=1) 的任何数据（demo 数据不在范围内）
- 不动 `content_monitor` / `self_service_config` 的父账户 bypass 行为（系统级功能，本需求不动）

## 待 S1/S2 探索的边界 case（已大部分锁定）

1. ✓ "父账户名下"的 SOP 怎么界定 → 锁定：`creator_user_id = 父账户 id`。biz 层 CreateTemplateByUser/CreateTemplate 在写入时 resolve actor → parent（与 chatbot.user_id 模式一致）
2. ✓ 子账户登录时如何查父账户 → `user.parent_user_id` 反推
3. ✓ admin（id=1, parent_user_id=NULL）登录时看到什么 → 用户补充："admin 是 demo，看到空工作区或 demo 内容均可"
4. ✓ 父账户在 admin 端被关闭 `sales_agent` feature 后，其子账户的 `user_feature_permission` 记录如何处理 → 不动（保留作为 Layer 1，由 Layer 0 拦截）
5. ✓ SOP 模板转移端点是否需要 → S1 office-hours 决定不在本需求范围
6. ✓ NULL creator_user_id 的防御性策略 → SQL 加 `IS NOT NULL` 防御
7. ✓ chatbot id=8 (user_id=1) demo 怎么处理 → 不动

## 备注

- 当前 prod 数据状态（2026-05-18 实测）：
  - 父账户 2 个：admin(id=1) + user_moxiaopai(id=30)
  - sop_template 4 行（id=1, 2 创建者 NULL；id=3 草稿归 30；id=4 已发布归 30）
  - chatbot_config 9 行（8 行 user_id=30，1 行 user_id=1）
  - user_feature_permission 48 行 sales_agent 子账户授权
- 范围明确**不含**：
  - chatbot_config 父账户隔离逻辑（已正常工作，不动代码）
  - 用户工作区主页 UI 重设计（只修后端过滤）
  - SOP 模板跨父账户转移功能（不纳入，S1 office-hours 确认）
  - "平台官方模板"概念（option B 留口但不实现）
  - **`content_monitor` 和 `self_service_config` 这两个 feature_key 的父账户 bypass 行为**（它们是系统级平台基础能力，与"租户拥有的资源"概念不同。content_monitor 据用户确认未来要废弃。S1 office-hours 确认本需求不动）
  - 升级到"自定义智能体类别"架构（Option B 路径，需新建 custom_agent 通用表 + 前端新分类区块 + 新 list API，3-4 天工作量。S1 office-hours 阶段评估：当前 platform 只有 1 个特殊 chatbot 卡片即销售智能体，YAGNI 原则倾向 Option A 小表先行；未来如有第 2、3 个同类卡片再升级 B）
- **仓库影响**：仅 numind-server。numind-admin-web 不涉及（无 admin UI），numind-web-v3 不涉及（后端透明过滤，前端无感）
