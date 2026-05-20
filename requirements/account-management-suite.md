# account-management-suite

## 来源
- 提出人：zhiyuchen（产品/技术 owner）
- 提出日期：2026-05-19

## 需求描述

为账户子系统补齐 4 项面向终端用户的功能，以闭环"注册—自助管理—密码恢复"基础体验：

1. **首页提交注册申请 + 管理后台审核创建父账户**
   - 首页（登录页旁）入口：未登录用户可填写注册申请（username / password / 昵称 / 手机号 / 申请说明）
   - 申请落 `registration_request` 表，状态 = `pending`
   - 管理后台新增"注册申请"列表 + 审核 UI：管理员可 `approve`（创建对应 `user` 行，`parent_user_id = null`，自动发放 trial 体验包）或 `reject`（带原因）
   - 审批不走支付路径，是单纯的"客户准入"流程

2. **修改密码（已登录用户）**
   - 设置页新增"修改密码"卡片
   - 接口 `POST /v1/users/me/change-password`：require 旧密码校验 + 新密码二次确认
   - 修改成功后令牌不强制失效（保持当前会话；若安全要求更高可后续迭代加 token invalidation）

3. **修改昵称（含全局去重）**
   - 设置页"个人资料"卡片已有 nickname 显示，新增可编辑形态
   - 复用现有 `PUT /v1/users/me`，**但 server 端补强 nickname 全局唯一校验**
   - DB 层补 `user.nickname` `uniqueIndex` 约束（migration）
   - 业务层：trim + 长度 1-30 + 唯一校验，冲突返回 `errno.ErrNicknameDuplicated` 并提示用户换一个

4. **忘记密码（短信验证码方案）**
   - 登录页"忘记密码"链接 → 新页面：填手机号 → 收短信验证码 → 设置新密码
   - 接口：`POST /v1/web/sms/send-code`（带防刷：同手机号 60s 间隔，单 IP 每日上限）+ `POST /v1/web/reset-password`（手机号 + 验证码 + 新密码）
   - 短信服务接入：S1 调研后定（**默认提议阿里云短信服务**，与现有阿里 DashScope 生态一致；fallback 提议 Mock 起步分阶段上线）
   - 验证码记录表 `sms_verification_code`：phone / code / purpose（reset_password / register_verify） / expires_at / used_at

## 业务目标

1. **闭环账户体验**：把"注册—改密码—改昵称—找回密码"4 个最基础的用户自助场景补齐，消除"丢密码只能找客服 reset DB"这类高成本支持负担。
2. **B2B 准入控制**：注册走审核（管理员 approve）而非开放公开注册，匹配产品目前的 B2B 销售模式 —— 控刷号、控质量，避免计费起步乱发 trial。
3. **合规底线**：用户能自助改密码 / 找回密码是基础信息系统的最低义务；后续若做账户注销则可独立 feature 补。
4. **生态一致**：短信服务复用阿里云，避免引入新供应商；trial 发放走现有 `MembershipService.GrantTrial` 路径，不动计费核心。

## 优先级

**中-高**

- 不是营收瓶颈，但是用户体验/支持成本的硬瓶颈：当前每丢一个密码就一次客服介入 + DB 操作
- 影响新客户准入：没有自助注册入口，全靠手工创建账号在 B2B 规模化时不可持续
- 风险有限：审核流由管理员把关、密码改/找回都是成熟模式，外部依赖只增加短信一项

## Triage

- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（新增 `registration_request` + `sms_verification_code` 两张表；`user.nickname` 加 `uniqueIndex`）
  2. 新增 API 端点：**是**（≥7 个：submit-registration / list-registrations / approve / reject / change-password / send-sms-code / reset-password；改昵称复用 `PUT /v1/users/me`）
  3. 新外部服务集成：**是**（短信服务，默认阿里云）
  4. 影响文件数：**>3**（跨三仓 ~15-20 文件：server 后端层 + numind-web-v3 登录/设置/忘记密码 3 页 + numind-admin-web 注册申请审核页）
  5. 高风险业务逻辑：**部分触发**（账户安全：密码改写 + 自动 trial 发放走计费起步路径；非最高敏度但绝非低风险）
- 人类决定：**待人类在 S0 gate 确认**

## 备注

### 关键设计假设（待 S1/S2 锁定）

1. **短信服务商**：默认提议**阿里云短信服务**（与 DashScope 同生态、统一 ak/sk 管理）。S1 调研期间最终敲定，**fallback 提议 Mock provider 起步**（开发/dev 环境返回固定验证码 123456，prod 切真实服务），可降低首期外部依赖风险。
2. **改密码必须校验旧密码**：行业标准，防止会话劫持后篡改密码；与"忘记密码"路径解耦（前者已登录用旧密码、后者未登录用短信验证码）。
3. **昵称去重范围**：**全局唯一**（不是父账户树内唯一）。理由：未来若开放跨账户协作 / SOP 分享 / 销售联系人，全局唯一标识更通用；DB 层强约束比业务层校验更可靠。
4. **trial 自动发放**：审核通过创建父账户时，调用现有 `MembershipService.GrantTrial`（200 积分 / 3 天），与 B2B grant-membership 路径独立。注意 `trial_grant` 表 UNIQUE per user_id 已保证不重发。
5. **审核结果通知**：起步用**站内通知 + 邮箱通知（可选）**，不强依赖邮件服务。短信通知留作后续迭代。
6. **注册申请字段**：username（同登录用户名，唯一） / password（提交时即 bcrypt 加密入库，避免明文）/ nickname（可选） / phone（必填，未来用作找回密码凭据） / 申请说明（可选）。审核通过后直接复用这些字段建 user 行。
7. **限流防刷**：
   - 注册申请：同 IP 1 小时内最多 3 次
   - 短信发送：同手机号 60s 间隔 + 同 IP 每日 10 条
   - 重置密码尝试：同手机号 + 验证码错误 5 次锁定 30 分钟
   - 用 Redis 实现（项目已有 `internal/pkg/redis`）

### S0 reviewer 发现的硬约束（修自 sonnet review 2026-05-19）

> 以下是 S0 reviewer 标记的 P0/P1，已在备注中固化为设计约束，S1/S2 必须遵守。

8. **【P0 已决】phone 唯一性策略 = Path A（加 uniqueIndex）**：`user.phone` 当前是普通索引（非 uniqueIndex），多用户可共享同一手机号。短信找回密码按 phone 查 user 会命中非确定性结果 = 安全漏洞。**2026-05-19 锁定 Path A**：
   - Migration 分步执行：
     - Step 1：SSH dev/prod 跑 `SELECT phone, COUNT(*) FROM user GROUP BY phone HAVING COUNT(*) > 1` + `SELECT COUNT(*) FROM user WHERE phone IS NULL OR phone=''` 摸现状
     - Step 2：空字符串 → NULL（MySQL unique 允许多个 NULL）
     - Step 3：重复 phone 治理 — S1 调研后按实际重复量决定策略（人工核对保留一个 + 其余清空 / 加数字后缀强制下次登录改）
     - Step 4：`ALTER TABLE user ADD UNIQUE INDEX idx_user_phone (phone)`
   - 决策理由：B2B 场景 1 用户 = 1 人 = 1 phone 是产品意图，重复值大概率是测试账号或录入错误；DB 层强约束 > 业务层校验；Path B 把"多 phone 找回密码"转嫁给客服是治理债。
   - 反悔条件：S1 调研发现实际重复 phone > 20% 用户量 → 回到 S0 gate 重审是否切 Path B

9. **【P0】nickname uniqueIndex migration 数据清理前置**：`user.nickname` 当前 `size:100` 无 not null 无唯一约束，现有用户大概率存在大量空字符串 / NULL / 重复值。一次性加 `ADD UNIQUE INDEX` 会因重复数据报错回滚。Migration 必须分两步：
   - Step 1：数据清理 — 空 nickname 用 username 填充；重复 nickname 加数字后缀（或要求用户首次登录时强制修改）
   - Step 2：加 `uniqueIndex`
   - S1 调研期必须 SSH dev/prod 数据库统计现状（COUNT GROUP BY nickname HAVING > 1，COUNT WHERE nickname IS NULL OR ''）

10. **【P1】注册申请 username 冲突双层校验**：`user.username` 已有 `uniqueIndex`，注册申请的 username 必须：
    - 提交时即时校验（查 `user` 表 + `registration_request` 表 status=pending 中是否已占用）
    - 审批通过时再做 final 校验（防竞态）
    - 两次都过才能建 user

11. **【P1】审批通过的原子事务边界**：approve 操作必须在**单个 DB 事务内**完成 3 件事：
    - `UPDATE registration_request SET status='approved'`
    - `INSERT INTO user (...)`
    - `GrantTrial(tx, userID, ...)` — `GrantTrial` 函数签名需扩展接受外部 tx 参数
    - 任一失败全部回滚，避免"user 已建但 trial 未发"的半成品状态

12. **【P1】GrantTrial Source 字段语义扩展**：当前 `GrantTrial` 写死 `Source: SourceB2BGrant`，会让审批送的 trial 被月末 b2b-billing-report 错误聚合进 B2B 结算单。`model.Source` 枚举需扩展 `'registration_grant'`（注册审批送）以区分 `'b2b_grant'`（父账户帮子账户）。

13. **【P2】registration_request.phone 不加 unique 约束**：审核拒绝后用户应可重新提交（含换号或修信息），如 phone 加 unique 会导致同号无法重申请。reject 操作直接置 `status='rejected'` 即可，不动 phone 字段，重申请走新的 request 行。

14. **【P2】sms_verification_code.purpose 范围限定**：本 feature **只使用 `'reset_password'` 一个 purpose**。`'register_verify'` 不属于本 feature 范围（注册走管理员审批，不走短信验证），不在表中预留，避免 S2 spec 歧义。未来若加自助注册再扩展。

15. **【P2】密码策略明确排除**：本 feature **不实现**密码强度策略（最小长度只校验 ≥6，与现有 `CreateUserRequest` 一致 1）/ 密码 reuse 检查 / 密码过期策略 / 密码 history。这些可作后续独立 feature。

### 拆分讨论结果

用户在 Triage 阶段拍板"4 项一起做"，不拆分。理由：S0-S7 流程跑一遍代价相对固定，分两期会重复 NDF 全流程。同 feature 内 task 拆分由 S3 plan 负责。

### 待 S1 office-hours 挑战的假设

- **审核流真有必要吗 vs 直接开放注册**：B2B 销售导向 → 审核更合适；若产品要做 PLG 转型，则审核反而是阻碍
- **短信验证码 vs 邮箱链接重置**：手机号在 B2B 触达率更高，但邮箱方案零外部依赖（用 SMTP）；若 S1 调研发现短信集成成本/合规风险高，邮箱方案是 fallback
- **昵称全局唯一是否过严**：若后续昵称冲突频发，可能需要支持"昵称 + 4 位 discriminator"（类似 Discord）。S1 先按全局唯一推进，留作后续迭代点

### 关联 features

- `legacy-tier-removal`（completed）— 已统一 credits 计费模式，新注册账户直接走 credits + trial_grant 路径，无 legacy 模式分叉
- `customer-permission-lifecycle`（S6）— B2B2C 父子账户权限基础设施，本 feature 的"创建父账户"产出物 = customer-permission 体系的"父账户"层
- `membership-credits-redesign`（S5-prod-deferred）— trial_grant / subscription / credit_cycle 三池 SOT，本 feature 的"自动发放 trial"直接走 `MembershipService.GrantTrial`

### 不在本 feature 范围

- 账户注销/删除（合规项，可独立 feature 补，本轮明确不做）
- 修改手机号（依赖短信验证码闭环，本轮明确不做）
- 邮箱绑定（user 表无 email 字段，开新坑成本高，本轮不做）
- 2FA / 多设备管理 / 登录历史 / 第三方 SSO（用户明确"不要太复杂"，全部排除）
- 邀请码生成系统（用户选审批方案，邀请码不做）
- 管理后台的"批量审批"/"申请数据统计看板"（S3 plan 阶段如时间充裕可加，否则起步只做单条审核）
