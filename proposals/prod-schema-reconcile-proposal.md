# Dev → Prod 数据库增量兼容 — 提案

## §1 方案概述 [客户可见]

本次不复制 Dev 数据，也不把 Prod 数据库“重装成 Dev”。我们只给 Prod 补上新版本功能
缺少的数据库结构和系统配置：

1. 给已有表增加新版本需要的新字段；
2. 新建文档系统和飞书连接使用的空表；
3. 补齐通知中心缺少的数据库约束；
4. 注册 `qwen3.5-flash` 并切换图片理解任务；
5. 对 102 条历史订阅只补两个新说明字段，原来的开始时间、到期时间、积分和来源都不改。

整个过程做成“检查 → 备份 → 补缺 → 核对”四件套。迁移脚本可以重复执行，第二次执行
不会重复新增或重复写配置。Prod 真正执行前先在 MySQL 8 的旧结构副本上连续执行两次，
并核对所有受保护数据。

## §2 工作量与时间线 [客户可见]
- 预估工作量：1–2 个工作日（迁移包、双跑演练、Dev 验收和 Prod 执行材料）
- 报价：内部上线工作，不单独报价
- 交付时间线：完成迁移演练和 Dev 全功能验收后，再单独申请 Prod 执行授权

## §3 技术可行性 [AI 内部]

### 现状结论

Prod 不是“完全旧版”，而是部分升级状态：

- 通知中心表已存在，但外键不完整；
- `agent_run` 已有飞书等待状态字段，但飞书账号/授权/操作表不存在；
- 文档表不存在；
- 附件解析字段不存在；
- `qwen3-vl-flash` 仍启用，`qwen3.5-flash` 路由不存在；
- Dev 与 Prod 都缺最新周会员订阅字段；
- 会议表只在 Dev 存在，而产品要求 Prod 会议副驾保持关闭。

因此直接重放所有历史 migration 会在已存在字段上失败，也可能把不应启用的会议表带进 Prod。

### 方案对比

#### 方案 A：从某个日期起顺序重放全部历史 migration

不采用。部分历史 SQL 使用无存在性保护的 `CREATE TABLE` / `ADD COLUMN`，而 Prod 已经处于
部分升级状态；顺序重放会在中途失败。它还会包含已明确保持关闭的会议功能。

#### 方案 B：直接部署新后端，让 GORM AutoMigrate 在启动时补表

不采用。启动迁移无法完整补齐系统 seed、通知外键和订阅字段；一旦 DDL 失败，客户 API 会和
迁移一起失败，停止点和核对结果也不清楚。

#### 方案 C：独立幂等 reconcile 迁移包（推荐）

采用。迁移包只描述“当前代码最终需要、Prod 当前又缺少”的差异，每一步先查
`information_schema` 再决定是否执行，并把系统配置按稳定唯一键 UPSERT。

### 迁移包组成

1. `preflight.sql`：只读检查数据库版本、表/字段/索引、影响行数、关键 provider 和冲突数据。
2. `apply.sql`：只新增或精确更新允许范围内的 schema / 系统配置。
3. `verify.sql`：检查最终结构、唯一配置、历史订阅新字段和受保护数据摘要。
4. MySQL 8 集成测试：从 Prod 旧结构的最小合成基线开始，执行 apply 两次并断言幂等。
5. 上线运行手册：备份标识、执行顺序、停止条件、应用回滚和验收清单。

### `apply.sql` 允许做的事

- `CREATE TABLE IF NOT EXISTS`：
  - `document`
  - `user_third_party_account`
  - `feishu_cli_vault`
  - `feishu_auth_session`
  - `feishu_operation`
  - `feishu_operation_proof_consumption`
  - `feishu_operation_execution_gate`
- 受 `information_schema` 保护的 `ALTER TABLE ... ADD COLUMN/INDEX/CONSTRAINT`：
  - `subscription.plan_type`
  - `subscription.cycle_credits`
  - `agent_attachment.parsed_content*`
  - Prod 通知中心缺少的外键
  - `agent_run.idx_ar_state_pending`
- 精确的系统配置 UPSERT：
  - `ai_service.model_key='qwen3.5-flash'`
  - Ali DashScope route
  - `task_profile.task_id='attachment.vision_describe'`
  - 对应 pricing rule
  - 停用旧 `qwen3-vl-flash` 的新请求路由，但保留历史行
- 精确 seed 清理：
  - 官方模板和官方示例技能；当前 Prod 均为 0 行，预计实际执行为 no-op。

### 明确禁止

- 不执行任何 `DROP TABLE` / `TRUNCATE`。
- 不删除或覆盖客户业务记录。
- 不导入 Dev 表数据。
- 不修改 `config_prod.yaml`。
- 不创建 meeting / diarization 表。
- 不修改 `chatbot_query_rewrite` / `universal_rewriter` 数据或开关。
- 不用自增 ID 定位 AI 服务、路由、定价或任务。

### 历史订阅处理

`subscription` 的 102 条历史记录都是旧版月会员。增加字段时给它们补：

- `plan_type='monthly'`
- `cycle_credits=2000`

这相当于在旧档案旁边补两张新标签，不会修改任何旧字段。迁移前后对 102 条记录的全部原字段
做逐行有序摘要；摘要不同立即停止上线。

### 表结构宽度

最终建表以当前 Go model 和 Dev 已运行结构为准，不照抄已经过时的早期 SQL。例如：

- `user.id` 当前为 `BIGINT UNSIGNED`；
- 文档和飞书表的 `user_id` 同样使用 `BIGINT UNSIGNED`；
- 所有宽 object key / 唯一索引表显式使用 `ROW_FORMAT=DYNAMIC`。

这样可以避免服务启动后 AutoMigrate 再次修改刚建好的表。

### 通知中心修复

Prod 通知/问卷表当前为空，因此可安全补齐：

- `announcement_read → announcement`
- `announcement_read → user`
- `survey_question → announcement`
- `survey_response → announcement`
- `survey_response → user`
- `survey_answer → survey_question`
- `survey_answer → survey_response`

新增前先检查孤儿行；即使当前行数为 0，也不跳过该检查。若未来执行时出现孤儿行，preflight
失败并停止，不自动删数据。

### 备份与失败处理

MySQL DDL 会自动提交，不能假装整个 migration 是一笔可回滚事务。因此采用：

1. 执行前做完整 Prod 备份并记录文件大小、SHA256 和时间；
2. 单独备份 `subscription`、附件、通知和 AI 配置相关表；
3. preflight 任一红项不通过则不执行；
4. apply 任一步失败则不部署新镜像，先核对已完成的幂等步骤；
5. 新版本上线后如应用异常，回滚旧镜像，但保留向后兼容的新增表/字段；
6. 只有确认旧数据被意外改变时才从备份恢复，不能为了“回滚干净”删除可能已产生的新客户数据。

用户已确认视觉模型无需单独 rollback；旧模型配置行仍保留，仅停用其新请求路由。

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性
- 涉及 LLM 调用：否（本需求只补数据库结构和系统路由）
- Trace 起点：N/A
- Generation 点：N/A
- 关键元数据：N/A

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为长期使用 Prod 的客户，我升级后能使用 Dev 的新功能，同时自己的资料、历史记录和积分不变。
- 作为使用智能体的客户，我可以读取上传文件、打开编辑文档，并在任务需要时连接个人飞书。
- 作为运营人员，我可以在 Prod 发布通知/问卷，用户能查看且不会产生重复阅读或重复答卷。
- 作为财务/运营人员，历史月会员仍按原有月度规则工作，新周会员可以按 7 天 / 500 积分工作。

### 验收标准
- [ ] `preflight.sql` 在当前 Prod 只读执行，输出所有预期差异且无未知冲突。
- [ ] `apply.sql` 在 Prod 旧结构合成基线上连续执行两次成功。
- [ ] 新表结构与当前 model 对齐，AutoMigrate 不再产生额外结构变更。
- [ ] 历史订阅原字段有序摘要前后一致，新增字段为 `monthly/2000`。
- [ ] 受保护的用户、SOP、聊天、智能体历史和积分表没有 UPDATE/DELETE 语句命中。
- [ ] 通知中心唯一索引和 7 个外键完整。
- [ ] 文档、飞书表首次上线为空。
- [ ] `qwen3.5-flash` route / task profile / pricing rule 各只有一份有效配置。
- [ ] Dev 应用 migration 后，会员余额、通知、文档、飞书和附件读取 smoke test 通过。
- [ ] Prod 执行材料包含备份证明、SQL SHA256、执行日志、验证日志和镜像版本。

### 边界情况
- Prod 已有某个新字段：跳过 ADD，并校验类型；类型不符则 preflight 红灯，禁止自动改类型。
- Prod 有部分飞书表：若已存在的表与最终结构逐项一致，则补建缺少的整表；若同名表、
  字段、索引或外键形状不一致，preflight 立即停止，不自动修补或回填旧数据。
- 通知表出现孤儿行：停止，不自动删除。
- `ali-dashscope` 不存在或不唯一：停止，不静默跳过 Qwen route。
- 周会员字段已存在但有未知值：停止并人工核对，不批量覆盖。
- migration 中断：重新跑 preflight；由于步骤幂等，可从已完成状态继续。
- 旧后端镜像回滚：新增表和字段保留，旧代码忽略它们。

### 权限规则
- migration 只由上线执行账号运行。
- 运行时文档、飞书、通知仍使用现有用户鉴权和 B2B2C 隔离规则，本需求不改变权限模型。
- 数据库凭据、飞书加密主密钥、API Key 不写入 SQL、Git 或日志。

### UI 行为规格
- 页面位置：无新 UI；为已有通知、智能体文档、设置/飞书连接和会员功能提供数据库支撑。
- 布局要求：N/A
- 交互模式：N/A
- 状态处理：
  - 缺配置时后端必须返回明确功能错误，不能写半条业务记录；
  - migration 未通过 verify 时不切流量；
  - 表为空是文档/飞书首次上线的正常状态。
