# 子账号运行权限管理（SOP + Chatbot）

## 来源
- 提出人：产品（项目所有者）
- 提出日期：2026-04-20

## 需求描述

原话：

> 在dev环境中，父级账号上线了新的SOP或者智能体之后，是可以对子账号进行权限管理的，比如说某一个子账号不能运行某几个SOP或者智能体，这个在dev环境当中是可用的，但是在prod环境中就没有办法进行管理了。具体的情况是在prod环境中父级账号。点到客户管理页面，然后对某一个子账号进行权限管理，打开权限管理窗口之后，没有新建的那些智能体或者SOP的选项，所以就无从进行管理。

> 默认全部不允许。SOP和chatbot都默认全部允许，得手动开启。但是已有的账号的现有权限不要改变。

> prod现有的4个chatbot是几个小时前刚刚创建的，还没有改为上线，因为现在还不能设置权限。等支持设置权限之后，再上线这4个chatbot。

结构化描述：

父账号需要能在客户管理页面对每个子账号做细粒度运行权限管理 —— 具体到每一个 SOP 模板和每一个 chatbot（智能体），指定该子账号能否运行。两类资源语义对齐、UI 对齐、默认策略对齐。

## 业务目标

1. **权限可管理**：B 端父账号购买后，其团队成员（子账号）不应一律看到父账号创建的所有资源。父账号需要按角色、按业务线分配运行权限。
2. **默认最小化原则**：新建子账号、新建资源时默认关闭运行权限，避免泄露或误用。父账号手动打开才生效。
3. **存量保护**：上线时严禁撤销任何现存子账号已拥有的可见范围，否则造成线上用户业务中断。

## 优先级

**高**。目前 chatbot 维度完全无权限控制 —— 任何已发布的 chatbot 所有子账号都能看见并运行，无法做团队分工。prod 用户已经发现并创建了 4 个 chatbot 但选择不发布以等待这个功能。

## Triage

- **推荐轨道：Standard**
- **分类理由**：
  1. 数据库 schema 变更：**是**（新增 `user_chatbot_permission` 表；可能新增 backfill migration 写入存量 `user_template_permission` 数据）
  2. 新增 API 端点：**是**（`/v1/customers/sub-users/:user_id/chatbots` 的 GET/POST/DELETE 共 3 个；批量授权/撤销可选 2 个）
  3. 新外部服务集成：**否**
  4. 影响文件数：**>3**（backend: migration SQL + chatbot model 权限关联 + store/customer 或新 store/chatbot_permission + biz/customer + biz/chatbot 的 `ListVisibleChatbots` 加白名单过滤 + controller + router 注册 + 单测；frontend: CustomersView.vue 弹窗新增 chatbot 区块 + api/customers.ts 新增函数；合计约 8-12 文件）
  5. 高风险业务逻辑（支付/权限）：**是**（权限模型翻转 + 存量数据迁移，错误会导致子账号误屏蔽或权限泄露）
- **人类决定**：**确认 Standard**。走 S0→S7 完整流程，S4 用 subagent teams 并行实施任务。

## 备注

### 与 feature `sop-perm-dialog-show-all` 的关系

并行推进的 Hotfix。A 修复 SOP 权限弹窗列表截断（显示全部而非前 20），不改动权限判断逻辑。B 在 A 的基础上翻转默认语义（default-allow → default-deny）并扩展 chatbot 能力。B 上线时需要合并 A 的前端改动（A 在 B 合入前已 merge 到 develop，B 的 feature 分支会自动带上）。

### 存量数据现状（已 SSH 实地勘查）

**Prod（v2.1.6, 2026-04-20 06:39 部署）**：
- `sop_template`：2 条，均 `active+published`，`creator_user_id=NULL`（种子数据）
- `chatbot_config`：4 条，全部 `status=draft`（用户明确表示等本 feature 上线后再发布）
- `user_template_permission`：38+ 子账号各 2 条权限记录（已经处于白名单模式，backfill 逻辑不会改变其数据）

**Dev（develop 分支，2026-04-20 18:35 部署）**：
- `sop_template`：10 条（7 published + 3 draft）
- `chatbot_config`：10 条（9 published + 1 draft）

**语义翻转的存量保护策略**：backfill migration 只处理 `user_template_permission` 表中"0 条记录的子账号"（上线前默认 allow-all 的账号），为他们写入父账号当前全部已发布模板的授权行，等效冻结其当下可见范围。已有权限记录的子账号数据不动。Chatbot 方向无存量（4 条全 draft 对子账号不可见），新表可从零建起、直接走 default-deny。

### 语义核心约束（S2 Spec 必须覆盖）

1. 父账号（`parent_user_id IS NULL`）永远 allow-all，不受白名单影响
2. 子账号白名单语义：有记录 → 仅允许表中的资源；**0 记录 → 拒绝全部**（翻转自现行 SOP 逻辑）
3. Backfill migration 幂等 + 可回滚
4. 两资源的权限判断入口：SOP 现有 `Customers().HasTemplatePermission()`；Chatbot 新增对称的 `Customers().HasChatbotPermission()` 或在 biz/chatbot 内直接查

### 上线顺序的阻塞关系

- B 必须在 backfill migration 运行完成**之后**才能翻转默认语义。分步：
  1. Deploy backfill migration（写入 0 记录子账号的存量数据）
  2. Deploy 新代码（翻转判断逻辑 + 新增 chatbot 端点 + 前端 UI）
  3. 验证 prod 存量子账号可见范围完全不变
- 如果 2 先于 1 上线，所有 0 记录子账号立刻被 deny all → 线上事故

S2 Spec 必须锁定部署顺序，S3 Plan 的 S5/S6/S7 流程必须包含"先 migrate 再 merge code"的 gate。
