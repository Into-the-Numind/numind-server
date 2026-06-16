# 文档系统（Document System）

## 来源
- 提出人：用户（产品负责人）
- 提出日期：2026-06-16

## 需求描述
用户原话：搭一套文档系统 —— 用户可以上传自己的文档，在平台上编辑修改；AI 也能生成对应的文件，用户可以直接打开、编辑、下载。系统要能和其它部分很好区分开，不影响 prod 打 tag 部署；开发全程用 worktree（有并行任务）。

经过 3 轮问答澄清后的精确边界：

### 核心定位
**AI 生成交付物为主，用户微调后下载。编辑为辅。**

### 生成入口（关键澄清）
**不做"AI 专门生成文档"的独立功能。** AI 生成文档发生在 **agent mode** 里 —— agent 跑的过程中如果产出了文件，用户就能打开它。文档系统本质上是给「agent 产出的文件 + 用户上传的文件」提供**在平台内打开 / 编辑 / 下载**的能力。

### 打开入口
**agent 对话里直接点开**（不做独立的"我的文档"工作区）。AI 在对话中生成的文件、以及用户在对话中上传的文件，都在对话内点击即可打开编辑器。

### 编辑方式（转换式）
上传/生成的 docx 或文本 → 解析成 Markdown → **WYSIWYG 所见即所得编辑**（TipTap/Milkdown 这类无头编辑器库，底层存 Markdown）→ 导出 docx / pdf / md。
最大化复用现有 COS + DocumentParser（MarkItDown/go-fitz）+ qwen-long 解析能力。
**代价（已接受）**：复杂排版 / 图表 / 批注 / 精确样式在转换中会被简化。

### 可编辑范围
- **可在线 WYSIWYG 编辑**：Markdown / Word(docx) / 纯文本 / HTML 等文本类
- **只预览 + 下载**：图表 PNG / CSV / Excel / PPT / PDF

### 生成 / 下载格式
Markdown / 富文本 + Word(docx) 为核心。（xlsx/pptx/pdf 非 v1 重点；docx 导出复用现有 Python docx-author skill）

### 持久化
**自动可打开编辑** —— AI 一生成就能点开，编辑即自动持久化为用户的文档，无需手动"存为文档"。

### 使用者与协作
**仅 C 端个人私有文档。** v1 明确**不做**：分享 / 下发给子账户、版本历史、多人实时协作（架构上预留扩展位，不提前实现）。

## 业务目标
让 agent mode 产出的交付物（报告、方案、Word 文档等）从"只能下载的死文件"变成"能在平台内打开、微调、再下载的活文档"，降低用户拿成果去外部工具二次编辑的摩擦，提升交付闭环体验。

## v1 范围线（用户确认 2026-06-16）
**v1 只做 AI 生成产物的打开/编辑/下载。** 即：agent mode 在对话中生成的文本类文件（markdown/docx/纯文本/HTML）→ 在对话内点开 → WYSIWYG 编辑（自动持久化）→ 导出下载（md/docx/pdf）。
**v2（明确推迟）**：用户上传自己的文档后的在线编辑。
> 注：用户上传通道本身（agent attachment）已存在；v1 不为"上传的文档"接通在线编辑器，只聚焦 AI 生成产物这条核心闭环。

## 隔离策略：feature flag 休眠（复用通知中心模式）
为满足"和其它部分区分开、不影响 prod 打 tag 部署"：
- 后端 `features.document_system.enabled` 默认 **OFF**，关时路由/能力休眠，对现有流程零影响。
- 前端 `VITE_ENABLE_DOCUMENT_SYSTEM` 控制"打开编辑"入口的显隐。
- migration 独立、可单独执行；不开 flag 不跑也不影响现有表。
- 合 develop 安全；prod 手动 tag 部署时不开 flag 即等于未上线。

## 优先级
高

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（新增 document 表 + 关联 agent_tool_artifact / agent_attachment）
  2. 新增 API 端点：**是**（文档 CRUD、打开/解析、保存、导出下载）
  3. 新外部服务集成：否（复用现有 COS / DocumentParser / qwen-long / Python docx skill；前端新增无头编辑器库属前端依赖非外部服务）
  4. 影响文件数：**>3**（跨两仓库，后端 model+store+biz+controller+router，前端编辑器组件+API+视图）
  5. 高风险业务逻辑（支付/权限）：否（但涉及用户数据隔离，需 user_id/parent_user_id 严格隔离）
- 人类决定：{待确认 —— 确认 Standard}

## 隔离 / 部署约束（用户强调）
- **模块自包含、纯增量**：新 DB 表（建议前缀 `document_`）、新路由组 `/v1/documents/*`、新前端路由/组件；尽量不改动现有部署/打 tag 链路。
- **agent 侧改动控制为"附加"**：在对话中对已有 artifact/attachment 增加"打开编辑"动作，不改其现有生成/下载行为。
- prod 为手动 tag 部署，feature 合 develop 不会自动上线 —— 进一步降低对 prod 的影响。
- **全程 worktree 开发**（NDF Standard 由 `ndf-start` 自动建 worktree 保证）。

## 备注
- 复用资产盘点（已 explore 验证）：COS 存储 + 预签名下载 URL、`attachment/UploadService`、`DocumentParser`(MarkItDown+go-fitz)、qwen-long 文件解析、前端 CodeMirror（备选）、agent `AgentToolArtifact`/`AgentAttachment` 模型。
- 新增缺口：通用 document 数据模型、文档 CRUD/解析/导出 API、前端 WYSIWYG 编辑器（无头库）、md→docx 导出对接。
- 待 S1/S2 决策：document 表与 artifact/attachment 的关系（外键关联 vs 拷贝快照）、docx 导出走 Python skill 的同步/异步方式、WYSIWYG 库选型（TipTap vs Milkdown）。
