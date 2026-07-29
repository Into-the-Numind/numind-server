# Dev 全量升级到 Prod：上线执行清单

> 日期：2026-07-30  
> 目标：让正式客户使用 Dev 已实现并验证的产品能力，同时保留 Prod 现有用户资料、积分、订单、SOP、聊天记录、智能体记录和知识库内容。  
> 环境约束：不使用 QA；本地自动验证 → Dev 验收 → 人工授权 → Prod。

## 1. 大白话原则

这次不是“把 Dev 数据库复制到 Prod”，而是：

1. 把 Dev 的最新代码部署到 Prod。
2. 给 Prod 数据库补上新功能需要的空表和新字段。
3. 不覆盖 Prod 现有业务数据，不把 Dev 的测试账号、测试记录或测试积分带过去。
4. 先备份，再迁移，再部署；每一步都有停止条件。
5. Sandbox 和正式业务使用同一台云服务器，但使用独立 Docker 控制面；正式 API 只能创建 Sandbox 容器，不能控制 MySQL、Redis、正式前后端等业务容器。

## 2. 已完成的上线准备

| 项目 | 当前结果 |
| --- | --- |
| 用户 API Dev 基线 | `develop-3f327e50` 已部署，公网 `/healthz` 正常 |
| 管理端 API Dev 基线 | `develop-c624cd8b` 已部署，容器内部 `/healthz` 正常 |
| 用户前端 Dev 基线 | Dev 已是本地最新 `3522939` |
| 管理前端 Dev 基线 | `develop-aea474d` 已部署，公网 `/health` 正常 |
| 小红书统计测试 | 中国时区凌晨按日统计问题已修复；全量 Go 测试与 lint 通过 |
| 构建机清理 | 已删除未使用的旧镜像与构建缓存，释放约 14.2GB；运行中的 `voiceprint-dev`、`crawl4ai` 未受影响 |
| QA | 明确不使用 |

## 3. 正式版功能开关

### 3.1 需要打开

| 产品功能 | 后端设置 | 前端设置 | 额外前置 |
| --- | --- | --- | --- |
| 首页 SOP、AI 聊天、AI 智能体、配置、小红书/选题库、插件、设置 | 随最新代码上线 | 随最新代码上线 | 完整回归验证 |
| 通知中心 | `NUMIND_FEATURES_NOTIFICATION_CENTER_ENABLED=true` | Prod 构建时 `VITE_ENABLE_NOTIFICATIONS=true` | Prod 已有公告相关表 |
| 文档系统 | `NUMIND_FEATURES_DOCUMENT_SYSTEM_ENABLED=true` | Prod 构建时 `VITE_ENABLE_DOCUMENT_SYSTEM=true` | 新建 `document` 表；Sandbox 可用 |
| 飞书连接与飞书 Agent 工具 | `NUMIND_FEATURES_FEISHU_INTEGRATION_ENABLED=true` | 无单独编译开关 | 飞书表、加密 Keyring、临时目录 |
| 知识库可答性判断 | `NUMIND_FEATURES_ANSWERABILITY_GATE_ENABLED=true` | 无 | 不改旧文档 |
| 结构化切块 | `NUMIND_FEATURES_STRUCTURE_AWARE_CHUNKING_ENABLED=true` | 无 | 只作用于新上传/未来重建的文档 |
| 混合检索 | `NUMIND_FEATURES_HYBRID_RETRIEVAL_ENABLED=true` | 无 | 旧索引缺 FTS 时自动降级，不重写旧数据 |
| 重排保护 | `NUMIND_FEATURES_RERANK_HARDENING_ENABLED=true` | 无 | 无数据迁移 |
| 知识库无答案时的友好兜底 | `NUMIND_FEATURES_KB_FALLBACK_ENABLED=true` | 无 | 无数据迁移 |
| Doc2Query | `NUMIND_FEATURES_DOC2QUERY_ENABLED=true` | 无 | 只作用于新上传/未来重建的文档 |
| 模型供应商 Prompt 缓存 | `NUMIND_FEATURES_PROVIDER_PROMPT_CACHE_ENABLED=true` | 无 | 无数据迁移 |
| Sandbox 与技能执行 | `NUMIND_SANDBOX_BACKEND=docker`、`NUMIND_SANDBOX_SKILLS_ENABLED=true` | 无 | 必须先完成同机隔离方案 |

### 3.2 保持关闭

| 功能 | 设置 | 原因 |
| --- | --- | --- |
| 会议副驾 | 后端 `NUMIND_FEATURES_MEETING_COPILOT_ENABLED=false`；前端不设置 `VITE_ENABLE_MEETING_COPILOT` | 产品决定暂不上线 |
| 说话人分离 | 后端 `NUMIND_FEATURES_MEETING_DIARIZATION_ENABLED=false`；前端不设置 `VITE_ENABLE_MEETING_DIARIZATION` | 会议副驾关闭时没有独立产品价值 |
| Chatbot 查询改写 | `NUMIND_FEATURES_CHATBOT_QUERY_REWRITE_ENABLED=false` | 已有真实数据评估表明会破坏“资料库外拒答” |
| 通用查询改写器 | `NUMIND_FEATURES_UNIVERSAL_REWRITER_ENABLED=false` | A/B 未证明有稳定增益 |
| RAG 调试接口 | `NUMIND_FEATURES_RAG_EVAL_ENABLED=false` | 这是内部调试端点，不应暴露给正式环境 |

### 3.3 当前代码必须补的构建差异

`numind-web-v3/scripts/cicd/build-and-push.sh` 当前只会在 Dev/QA 编译通知中心和文档系统，Prod 仍为空值。上线前必须明确改为：

- Prod：通知中心 `true`
- Prod：文档系统 `true`
- Prod：会议副驾不设置
- Prod：说话人分离不设置

否则“代码部署成功”后，正式用户仍看不到通知中心和文档入口。

## 4. Prod 需要补的运行配置

### 4.1 新增能力所需变量

以下只列变量名和用途，真实值只能放在 Prod 服务器 `/opt/numind/prod/secrets.env`，权限必须是 `0600`。

| 变量 | 用途 |
| --- | --- |
| `NUMIND_WEB_SEARCH_TAVILY_API_KEY` | AI 智能体联网搜索 |
| `NUMIND_SECURITY_THIRDPARTY_TOKEN_KEY` | 飞书回执、游标和敏感边界的根密钥 |
| `NUMIND_FEISHU_KEYRING` | 飞书工作空间加密 Keyring；单行严格 JSON |
| `NUMIND_FEISHU_KEY_VERSION` | 当前飞书加密版本，例如 `v1` |
| `NUMIND_FEISHU_RUNTIME_BASE` | 飞书临时解密目录，建议 `/opt/numind/prod/feishu-runtime` |
| `NUMIND_FEISHU_AUTH_OWNER` | 飞书授权任务实例前缀，建议 `numind-prod-feishu` |
| `NUMIND_FEATURES_FEISHU_INTEGRATION_ENABLED` | 所有飞书前置满足后才设为 `true` |
| 第 3 节列出的 `NUMIND_FEATURES_*` | 后端产品功能开关 |
| Sandbox 的 `NUMIND_SANDBOX_*` | 等隔离方案完成后填写 |

飞书 Keyring 必须符合：

- 32 字节 AES-256 密钥的 canonical Base64；
- `NUMIND_FEISHU_KEYRING` 是单行 JSON 数组；
- 新旧版本轮换时先同时保留，不能直接删旧版本；
- 不写入 YAML、镜像、Git 或数据库。

### 4.2 密钥轮换硬门槛

由于 Prod 密钥值曾进入工具输出，所有现有 Prod 凭据必须按“可能泄露”处理。正式上线前至少完成：

1. 数据库、Redis、JWT 等内部凭据轮换。
2. 微信支付、支付宝等支付凭据按供应商流程轮换并验证回调。
3. 阿里云、火山、百度、COS、DashVector、AIHubMix、Langfuse 等第三方 Key 轮换。
4. 新增 Tavily Key。
5. 新生成飞书 `THIRDPARTY_TOKEN_KEY` 和 Keyring。
6. 更新 Prod `secrets.env` 后只做“变量名/格式/权限”检查，禁止打印真实值。

另外，当前 Git 跟踪的 Dev/Local/QA 配置文件含真实形式的凭据。它们也必须轮换并迁移到环境变量，不能继续长期留在仓库历史和镜像构建上下文中。

## 5. Prod 数据库现状与迁移

### 5.1 只读核查结果

| 检查项 | Prod 当前状态 | 上线动作 |
| --- | --- | --- |
| 通知中心表 | 已存在 | 不建表 |
| Sandbox/Skill/插件相关主表 | 已存在 | 不复制 Dev 数据 |
| `document` | 不存在 | 新建空表 |
| `user_third_party_account` 和飞书工作空间表 | 不存在 | 创建当前版本所需空表 |
| `agent_run.pending_external_action_*` | 已有部分列 | 迁移必须按列存在性跳过，不能直接跑旧 SQL |
| `subscription.plan_type`、`cycle_credits` | 不存在；现有订阅 102 条 | 添加新字段；历史行只给新字段填 `monthly/2000` |
| `agent_attachment.parsed_content*` | 不存在 | 添加新字段；只回填新字段，不修改旧业务字段 |
| `qwen3.5-flash` 服务与视觉任务路由 | 不存在 | 添加系统配置，不涉及用户数据 |
| 官方技能模板/官方示例技能 | 当前均为 0 条 | 删除迁移是 no-op，不影响客户技能 |

### 5.2 为什么不能直接顺序执行全部历史 SQL

Prod 是“部分结构已经被 AutoMigrate 补过、部分表又缺失”的混合状态。例如：

- `agent_run` 已经有飞书待执行动作的两列；
- 飞书主表却完全不存在；
- 原始飞书建表 SQL和后续 SQL之间还依赖中间列。

直接把历史 SQL 从头跑到尾会在“重复加列”处失败。因此必须先生成一份 **Prod schema reconcile migration**：

1. 所有建表使用存在性检查。
2. 所有加列先查 `INFORMATION_SCHEMA`。
3. 已存在且类型正确的列跳过。
4. 不删除、不改名任何客户业务字段。
5. 每个阶段都有验证 SQL。

### 5.3 允许的数据变化

允许：

- 新建空表。
- 给现有表增加新字段。
- 给 102 条历史订阅的 **新字段** 填 `plan_type=monthly`、`cycle_credits=2000`。
- 从旧附件内容复制到新增的 `parsed_content*` 字段。
- 新增/更新模型路由、计价规则等系统配置。

不允许：

- 覆盖用户资料、积分余额、订单、会员起止时间。
- 覆盖 SOP、聊天、智能体、知识库原内容。
- 把 Dev 数据导入 Prod。
- 为了启用新 RAG 强制重建客户已有知识库。
- 执行附件迁移中把旧 `modality` 字段从 `unknown` 改成 `text`；本次保持原值。

## 6. Sandbox 同机隔离

计划形态：

- 正式业务容器继续使用宿主机主 Docker daemon。
- 新建 Sandbox 专用 Docker daemon、专用 socket、专用数据目录和专用网络。
- 正式 API 只挂载 Sandbox 专用 socket，不挂载 `/var/run/docker.sock`。
- API 的 Docker CLI 只能看到 Sandbox 容器，看不到 MySQL、Redis、正式 API、前端和管理端容器。
- Sandbox 不挂载 Prod 密钥、数据库目录、上传目录或业务配置。
- 保持非特权用户、只读 rootfs、网络禁用、512MB 内存、1 CPU、64 PIDs 等限制。
- Prod 主机约 7.4GiB 内存且无 swap，不能照搬 Dev 的 5 个常驻 Sandbox；首发池大小需在 S1 提案中确认。

在此方案完成并验证前，Prod 的 Sandbox 和技能执行开关保持关闭。

## 7. 上线执行顺序

### 阶段 A：代码和配置缺口

1. 完成 Prod 通知中心/文档系统前端构建开关。
2. 完成 Prod schema reconcile migration 和验证脚本。
3. 完成同机 Sandbox 隔离。
4. 清除 Git 中硬编码的环境凭据，并更新运行时配置模板。
5. 本地全量测试，再部署 Dev。

### 阶段 B：Dev 产品验收

按正式用户视角验证：

1. 首页运行 SOP：普通流程、文件输入、结果下载。
2. AI 聊天：新建会话、历史会话、知识库问答。
3. AI 智能体：基础对话、工具调用、附件读取、Sandbox 技能产物。
4. 配置：SOP/智能体配置与权限。
5. 小红书/选题库：列表、分析、统计。
6. 插件/技能：浏览、导入、调用、生成文件。
7. 设置：个人设置、模型/连接入口。
8. 飞书：连接、状态、文档/Base 操作和失败提示。
9. 通知中心：入口、已读状态。
10. 文档系统：在线编辑、保存、导出。
11. 管理端：登录、用户、积分、账单、AI 服务配置。

会议副驾和说话人分离不验收，因为明确不上线。

### 阶段 C：Prod 变更窗口

1. 冻结发布 commit 和四个版本号；不再合入新功能。
2. 轮换并验证所有密钥。
3. 对 Prod MySQL 做可恢复备份，校验备份文件。
4. 只读记录关键用户数据基线：用户数、积分总额、订阅数、订单数、SOP/聊天/Agent 记录数。
5. 执行 schema reconcile migration。
6. 再次核对上述基线；原字段和总量必须一致。
7. 先部署后端用户 API，再部署管理端 API。
8. 做后端接口 smoke。
9. 部署用户前端，再部署管理前端。
10. 做正式域名 smoke 和核心产品功能抽查。
11. 观察日志、错误率、积分扣减和资源 30–60 分钟。

### 阶段 D：回滚条件

任一情况立即停止或回滚代码镜像：

- 登录、首页或核心 API 大面积失败。
- 积分异常扣减、会员状态异常或订单异常。
- 用户记录数量/关键字段基线异常。
- Sandbox 能看到或控制正式业务容器。
- MySQL/Redis/正式 API 因 Sandbox 资源竞争明显不稳定。
- 飞书密钥校验失败或出现明文敏感日志。

数据库以“只加表/加字段”为主，代码回滚时保留新增空表和新字段即可；不要为了回滚镜像去删除客户数据。

## 8. 当前硬阻塞

正式打 tag 和部署 Prod 前，以下必须全部关闭：

1. Prod 密钥轮换完成。
2. Prod schema reconcile migration 设计、测试、Dev 演练完成。
3. 通知中心/文档系统 Prod 构建开关修复完成。
4. Sandbox 同机隔离完成并在 Dev/同形态环境验证。
5. Dev 全功能产品验收完成。
6. 产品负责人单独明确授权“现在部署 Prod”。

