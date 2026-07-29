# Dev → Prod 数据库升级操作手册

这套脚本只负责把 Prod 数据库补到当前 Dev 产品功能需要的结构，不复制
Dev 数据，也不替换 Prod 用户、积分、订阅、SOP、聊天或智能体历史。

当前产品范围：

- 打开通知中心、文档系统、飞书个人连接、附件解析和 Qwen 3.5 Flash
  图片理解所需的数据库能力；
- 补齐周订阅所需字段；
- 保持会议副驾、说话人分离、`chatbot_query_rewrite` 和
  `universal_rewriter` 关闭；
- 历史订阅只为两个新字段补入 `monthly` 和 `2000`，原来的期限、来源、
  积分及时间字段不改。

本手册由 AI 操作员执行。任何数据库密码、API Key、加密密钥都只能从受控
运行环境读取，不写进命令历史、本文档或 Git。

## 固定工件

- 升级 SQL：`migrations/20260730_120000_prod_schema_reconcile.sql`
- 只读检查：`scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql`
- 升级后核对：`scripts/2026-07-30-prod-schema-reconcile/02-verify.sql`
- 隔离演练：`scripts/2026-07-30-prod-schema-reconcile/test-mysql8.sh`
- 当前升级 SQL SHA256：
  `caf975555768df1e4677856422d2c15bcd91354b2c2358153ea759bd655a86c6`

上线使用的 Git tag、SQL 文件和上述 SHA256 必须完全对应。SQL 有任何修改，
必须重新评审、重跑全部测试并更新这里的 SHA256。

## 第一关：代码和隔离数据库验证

在仓库根目录执行：

```bash
go test ./migrations -run ProdSchemaReconcile -count=1
scripts/2026-07-30-prod-schema-reconcile/test-mysql8.sh
```

隔离演练必须显示：

```text
PASS: MySQL 8 exact, partial, negative-preflight, double-apply, constraints, and protected-data checks
```

这一步使用临时 MySQL 8 容器和合成数据，不连接 Dev 或 Prod。

## 第二关：Dev 真库演练

### 1. 留存执行证据

记录以下内容，不记录密码：

- 执行时间和执行人；
- Dev 当前后端镜像、Git commit；
- 数据库容器/实例名和数据库名；
- 备份文件绝对路径、文件大小、SHA256；
- 升级 SQL SHA256；
- preflight、首次 verify、二次 verify 的完整输出；
- 升级前后的全部老数据保护投影、扩展 checksum 与核心表行数；
- Dev 产品验收结果。

### 2. 备份

使用 Dev 服务器已有的安全凭据做一次完整、可恢复的逻辑备份。备份至少使用
`--single-transaction --quick --routines --triggers --events`，输出到专用
备份目录。完成后必须：

1. 文件存在且非空；
2. 计算并记录 SHA256；
3. 用 `mysqldump`/MySQL 工具检查文件结尾完整；
4. 在隔离 MySQL 8 实例恢复一次，并确认恢复后的核心表行数与备份前一致。

没有可恢复备份，不执行下一步。

### 3. 只读检查

通过安全数据库客户端把 `00-preflight.sql` 输入 Dev 数据库。它只能执行
查询，不会改数据。

允许 Dev 已经存在本次目标表和字段，但输出中任何一行 `status=FAIL` 都要
立即停止。把完整输出保存到本次上线证据目录。

### 4. 保存全部老数据保护校验值

在升级前，对 `subscription` 的旧字段按主键排序后计算 SHA256。保护字段是：

- `id`、`user_id`
- `first_started_at`、`current_started_at`、`expires_at`
- `total_months_purchased`、`source`、`granter_user_id`
- `created_at`、`updated_at`

升级后的校验值必须与升级前完全相同。

同时保存脚本输出的：

- `agent_attachment_protected_projection`：只计算附件原字段，不包含本次新字段；
- `agent_attachment_complete_projection`：计算附件全部业务字段；缺失的新字段按
  新增字段将采用的默认值计算，因此升级前后必须完全一致；
- `agent_run_protected_projection`：只计算智能体运行原字段，不包含本次新字段；
- `feishu_proof_business_projection`：计算飞书操作凭证的全部业务字段，补外键前后
  必须完全一致；
- 用户、试用积分、订阅周期积分、加量包余额、会员事件、积分预扣/流水、
  SOP、聊天、销售历史表的 `CHECKSUM TABLE ... EXTENDED`；
- 每张核心业务表行数。

上述行数、投影和 checksum 在首次执行、第二次执行后都必须完全一致。

### 5. 执行两次并核对

顺序固定：

1. 执行升级 SQL；
2. 执行 `02-verify.sql`，所有检查必须是 `PASS`；
3. 再执行一次同一份升级 SQL；
4. 再执行一次 `02-verify.sql`，所有检查必须仍是 `PASS`；
5. 重新计算所有老数据保护校验值，必须与升级前一致。

第二次执行用于证明脚本可以安全重试，不会重复创建配置或破坏数据。

### 6. 部署 Dev 后端并做产品验收

先部署用户 API，再部署管理 API。只使用与评审 commit 对应的镜像。

产品验收至少包括：

- 老用户可登录，原积分余额、订阅期限和历史记录不变；
- 首页 SOP 可创建、运行并查看结果；
- AI 聊天机器人可新建会话、发消息、读历史；
- AI 智能体可运行，附件可解析，图片可由 Qwen 3.5 Flash 理解；
- 配置页中的聊天机器人、智能体、SOP 模板、知识库和技能可正常使用；
- 小红书/选题库可打开并完成核心操作；
- 插件市场和已安装插件可正常使用；
- 设置页可正常保存；
- 通知中心可打开、拉取列表和已读状态；
- 文档卡片可打开编辑器并下载；
- 飞书连接状态可查看，授权可以发起；
- 会议副驾和说话人分离不出现；
- `chatbot_query_rewrite` 和 `universal_rewriter` 不启用；
- API、管理 API 和前端无新增 5xx，日志无持续错误。

QA 不在本次流程中。

## 第三关：Prod 上线前准备

Prod 此阶段仍然只读。上线前必须同时满足：

- Dev 两次升级演练和完整产品验收通过；
- 后端、用户前端、管理前端都已固定 tag 和镜像 digest；
- Prod 只读 preflight 是当前重新执行的结果，且全部 `PASS`；
- Prod 完整备份已完成、SHA256 已记录、隔离恢复验证通过；
- 生产运行配置和密钥齐全；
- Sandbox 在同一台 Prod 服务器上的隔离、5 个并发上限和资源上限已验证；
- 通知中心、文档和飞书打开，会议副驾、说话人分离及两个 rewrite 功能关闭；
- 获得单独、明确的 Prod 执行授权。

任何一项不满足，都不执行 Prod 写入或部署。

## Prod 正式执行顺序

获得最终授权后，顺序不能调整：

1. 冻结本次相关代码和配置；
2. 创建 Prod 全库备份，记录大小和 SHA256，并验证可恢复；
3. 进入短维护窗口，暂停用户 API 和管理 API 的数据库写入；
4. 再跑一次只读 preflight，保存完整输出；
5. 再核对 tag、镜像 digest 和升级 SQL SHA256；
6. 保存全部老数据保护校验值和核心业务表行数；
7. 执行一次升级 SQL；
8. 执行 verify，所有检查必须是 `PASS`；
9. 重新核对全部老数据保护校验值和核心业务表行数；
10. 部署用户 API；
11. 部署管理 API；
12. 部署用户前端；
13. 部署管理前端；
14. 恢复流量并执行与 Dev 相同的产品验收；
15. 观察 API 5xx、容器重启、数据库错误和关键任务失败；
16. 保存最终证据并结束冻结。

Prod 不需要为了证明可重试而实际执行第二次升级；双次执行证明来自隔离 MySQL
和 Dev 演练。Prod 只执行一次，减少不必要操作。

## 立即停止的情况

出现以下任一情况，停止后续步骤，不继续“试试看”：

- preflight 或 verify 出现任何 `FAIL`；
- SQL SHA256 与本手册或发布 tag 不一致；
- 备份缺失、为空、SHA256 未记录或恢复验证失败；
- 任一老数据保护校验值或核心表行数变化；
- 用户、积分、订阅、SOP、聊天、销售或智能体历史表行数异常；
- 通知表存在孤儿数据，或 Ali/Qwen 稳定键重复；
- 必需的运行密钥、飞书配置或 Sandbox 隔离未完成；
- 新 API 持续返回 5xx、容器反复重启或数据库出现持续错误；
- 尚未获得单独的 Prod 执行授权。

## 回滚边界

如果升级 SQL 后、部署新后端前发现保护数据变化：

1. 保持应用不升级；
2. 停止所有后续写入步骤；
3. 从本次上线前备份恢复；
4. 重新核对保护校验值和核心表行数；
5. 记录事故并修正迁移，重新走完整流程。

如果新后端部署后产品异常：

1. 回滚到上一版用户 API 和管理 API 镜像；
2. 必要时回滚两套前端；
3. 保留本次新增的表、字段、索引和约束，不做破坏性“降级 SQL”；
4. 老版本会忽略这些新增结构；
5. 只有确认旧业务数据被错误修改时，才在停止写流量后从备份做定点恢复。

不删除新增表或字段，是为了避免删掉上线后可能已经产生的新文档、飞书授权或
其他用户数据。
