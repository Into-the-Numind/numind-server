# numind-server 项目详细拆解

> 最后更新：2026-02-21

---

## 一、项目定位

**numind-server** 是「莫小派 (Numind)」产品的 Go 后端服务，提供 **AI 驱动的 SOP 工作流引擎** + **销售智能体 (SalesRAG)** + **卡片生成** + **用户体系** 等核心能力。

**一句话概括**：一个集成了 RAG、SOP 工作流、Markdown→图片渲染、微信支付、多层级用户体系的 AI 后端平台。

---

## 二、技术栈总览

| 层次 | 技术 |
|------|------|
| **语言** | Go 1.24 |
| **Web 框架** | Gin |
| **ORM** | GORM + MySQL 8.0 |
| **缓存** | Redis (go-redis/v9) |
| **认证** | JWT (golang-jwt/v5) |
| **配置管理** | Viper + Cobra |
| **日志** | Zap (go.uber.org/zap) |
| **AI 大模型** | 火山引擎 (Volcengine)、阿里云百炼 (DashScope)、DMXAPI (DeepSeek-V3.2) |
| **向量数据库** | 阿里云 DashVector / 火山引擎 VikingDB / 内存向量(chromem-go) |
| **对象存储** | 腾讯云 COS |
| **PDF 处理** | go-fitz (基于 MuPDF) |
| **图片处理** | chromedp (Chrome Headless) + imaging + WebP |
| **分词/NLP** | gojieba (结巴分词) + tiktoken |
| **支付** | 微信支付 V3 (wechatpay-go) |
| **部署** | Docker + Docker Compose + Nginx + GitHub Actions CI/CD |
| **监控** | Prometheus + Grafana + pprof |

---

## 三、代码目录结构（深度展开）

```
numind-server/
│
├── cmd/                          # ① 启动入口
│   ├── numind/                   # 主服务入口 (main.go)
│   └── numind-admin/             # 管理后台入口 (main.go, config-validator)
│
├── internal/                     # ② 核心业务代码（Go 内部包规范）
│   │
│   ├── numind/                   # ═══ 主服务核心 ═══
│   │   ├── numind.go             # 应用启动引导（Cobra Command → run()）
│   │   ├── server.go             # HTTP Server 创建
│   │   ├── router.go             # 🔑 全局路由注册（约500行，核心枢纽）
│   │   ├── helper.go             # 辅助函数（COS 初始化等）
│   │   ├── cmd.go                # 子命令注册
│   │   ├── config/               # 配置初始化
│   │   │
│   │   ├── biz/                  # ═══ 业务逻辑层 (核心) ═══
│   │   │   ├── biz.go            # IBiz 接口定义 + biz 工厂（聚合所有子 biz）
│   │   │   ├── api_diagnostics.go  # API 诊断工具
│   │   │   ├── api_recovery_manager.go  # API 恢复管理器
│   │   │   │
│   │   │   ├── user/             # 用户管理
│   │   │   ├── admin/            # 管理员逻辑
│   │   │   ├── admin_account/    # 管理端账户
│   │   │   ├── customer/         # 客户(子账户)管理
│   │   │   │
│   │   │   ├── book/             # 📓 笔记管理
│   │   │   │   ├── book.go       # 笔记 CRUD
│   │   │   │   ├── async_processor.go  # 异步处理器 (132KB，核心文件)
│   │   │   │   ├── keyword_generator.go  # 关键词生成
│   │   │   │   └── search.go     # 搜索逻辑
│   │   │   │
│   │   │   ├── card/             # 🃏 卡片渲染引擎
│   │   │   │   ├── card.go       # 卡片基础逻辑
│   │   │   │   ├── config.go     # 卡片配置
│   │   │   │   ├── renderer.go   # 基础渲染器 (Markdown→HTML→图片)
│   │   │   │   ├── flow_renderer.go  # 流式分页渲染器 (26KB)
│   │   │   │   ├── lightweight_renderer.go  # 轻量渲染器
│   │   │   │   ├── cover_renderer.go  # 封面渲染器 (20KB)
│   │   │   │   ├── cover_optimizer.go  # 封面优化器
│   │   │   │   └── error_handler.go  # 渲染错误处理
│   │   │   │
│   │   │   ├── sop/              # 🔄 SOP 工作流引擎
│   │   │   │   ├── sop.go        # SOP 完整业务逻辑 (62KB，核心文件)
│   │   │   │   ├── executor.go   # 节点执行器 (47KB)
│   │   │   │   ├── executor_helper.go  # 执行器辅助函数
│   │   │   │   └── cleanup.go    # 草稿清理任务
│   │   │   │
│   │   │   ├── salesrag/         # 🤖 销售智能体 RAG (最大模块)
│   │   │   │   ├── salesrag.go   # 核心业务逻辑 (115KB，最大文件)
│   │   │   │   │
│   │   │   │   ├── domain/       # 领域模型
│   │   │   │   │   ├── schema.go # 知识文档/切片数据结构
│   │   │   │   │   └── strategy.go  # 销售策略领域模型
│   │   │   │   │
│   │   │   │   ├── port/         # 端口接口（六边形架构）
│   │   │   │   │   ├── parser.go    # 文档解析器接口
│   │   │   │   │   ├── router.go    # LLM 路由接口
│   │   │   │   │   ├── strategy_router.go  # 策略路由接口
│   │   │   │   │   ├── tagger.go    # 标签器接口
│   │   │   │   │   └── vector_store.go  # 向量存储接口
│   │   │   │   │
│   │   │   │   ├── adapter/      # 适配器实现
│   │   │   │   │   ├── dashvector_store.go  # 阿里云 DashVector 适配
│   │   │   │   │   ├── viking_store.go  # 火山引擎 VikingDB 适配
│   │   │   │   │   ├── chromem_store.go  # 内存向量存储适配
│   │   │   │   │   ├── memory_store.go  # 简单内存存储
│   │   │   │   │   ├── dmxapi_client.go  # DMXAPI (DeepSeek) 客户端
│   │   │   │   │   ├── llm_router.go  # LLM 路由实现
│   │   │   │   │   ├── regex_router.go  # 正则路由
│   │   │   │   │   ├── strategy_router.go  # 策略路由实现
│   │   │   │   │   ├── enhanced_parser.go  # 增强文档解析器 (19KB)
│   │   │   │   │   └── simple_parser.go  # 简单解析器
│   │   │   │   │
│   │   │   │   └── service/      # 服务层
│   │   │   │       ├── sales_rag.go  # RAG 核心服务
│   │   │   │       ├── pipeline.go  # 文档导入 Pipeline
│   │   │   │       ├── ingestion.go  # 导入逻辑
│   │   │   │       ├── splitter.go  # 文本切片器
│   │   │   │       ├── enhanced_splitter.go  # 增强切片器 (17KB)
│   │   │   │       ├── hybrid_splitter.go  # 混合切片器
│   │   │   │       ├── embedding_splitter.go  # Embedding 切片器
│   │   │   │       ├── splitter_adapter.go  # 切片器适配器
│   │   │   │       ├── tagger.go  # 自动标签
│   │   │   │       ├── strategy_service.go  # 销售策略服务
│   │   │   │       └── strategy_data.go  # 策略数据 (11KB)
│   │   │   │
│   │   │   ├── rag/              # 通用 RAG 服务
│   │   │   │   ├── rag_service.go  # RAG 服务 (笔记级)
│   │   │   │   ├── retriever.go  # 检索器
│   │   │   │   └── generator.go  # 回答生成器
│   │   │   │
│   │   │   ├── ali/              # 阿里云服务封装
│   │   │   │   ├── ali.go        # 阿里云统一入口 (28KB)
│   │   │   │   └── prompt_manager.go  # Prompt 管理器
│   │   │   │
│   │   │   ├── volc/             # 火山引擎服务封装
│   │   │   │   └── volc.go       # 火山引擎 API (31KB)
│   │   │   │
│   │   │   ├── chat/             # AI 对话管理
│   │   │   ├── article/          # 文章管理
│   │   │   ├── category/         # 分类管理
│   │   │   ├── template/         # 模板管理
│   │   │   ├── feedback/         # 反馈管理
│   │   │   ├── image/            # 图片管理
│   │   │   ├── markdown/         # Markdown 处理
│   │   │   ├── pagination/       # 分页算法
│   │   │   ├── payment/          # 支付业务
│   │   │   ├── order/            # 订单管理
│   │   │   ├── account_record/   # 账户流水
│   │   │   ├── config/           # 配置管理
│   │   │   ├── wechat/           # 微信服务
│   │   │   └── baidu/            # 百度服务
│   │   │
│   │   ├── controller/v1/        # ═══ API 控制器层 ═══
│   │   │   ├── user/             # 用户 API (登录/注册/资料)
│   │   │   ├── login/            # 登录控制器
│   │   │   ├── admin/            # 管理员 API
│   │   │   ├── admin_account/    # 管理端账户 API
│   │   │   ├── admin_sop/        # 管理端 SOP API
│   │   │   ├── book/             # 笔记 API (10个文件)
│   │   │   ├── card/             # 卡片 API
│   │   │   ├── category/         # 分类 API
│   │   │   ├── template/         # 模板 API
│   │   │   ├── feedback/         # 反馈 API
│   │   │   ├── image/            # 图片 API
│   │   │   ├── chat/             # 对话 API
│   │   │   ├── article/          # 文章 API
│   │   │   ├── sop/              # SOP API (用户端)
│   │   │   ├── salesrag/         # 销售 RAG API
│   │   │   ├── rag/              # 通用 RAG API
│   │   │   ├── customer/         # 客户管理 API
│   │   │   ├── pagination/       # 分页 API
│   │   │   ├── pdf/              # 文档转换 API
│   │   │   ├── ali/              # 阿里云 API
│   │   │   ├── volc/             # 火山引擎 API
│   │   │   ├── membership/       # 会员 API
│   │   │   ├── order/            # 订单 API
│   │   │   ├── pay/              # 支付 API
│   │   │   ├── payment/          # 支付管理 API
│   │   │   └── account/          # 账户 API
│   │   │
│   │   └── store/                # ═══ 数据访问层 (DAO) ═══
│   │       ├── store.go          # IStore 接口定义
│   │       ├── user.go           # 用户表操作
│   │       ├── admin.go          # 管理员表操作
│   │       ├── book.go           # 笔记表操作
│   │       ├── card.go           # 卡片表操作
│   │       ├── chat.go           # 对话表操作
│   │       ├── sop.go            # SOP 表操作 (26KB)
│   │       ├── customer.go       # 客户表操作
│   │       ├── sales_session.go  # 销售会话表操作
│   │       ├── knowledge_document.go  # 知识文档表
│   │       ├── knowledge_chunk.go  # 知识切片表
│   │       ├── language_style.go  # 语言风格表
│   │       ├── payment.go         # 支付表
│   │       ├── order.go           # 订单表
│   │       └── ...               # 其他表操作
│   │
│   ├── numind-admin/             # ═══ 管理后台服务 ═══
│   │   ├── numind-admin.go       # 管理后台启动引导
│   │   ├── router.go             # 管理端路由 (含 /v1/admin/login)
│   │   └── helper.go             # 管理端辅助函数
│   │
│   ├── pkg/                      # ═══ 内部公共包 ═══
│   │   ├── core/                 # 统一响应结构
│   │   ├── errno/                # 错误码定义
│   │   ├── log/                  # 日志封装 (Zap)
│   │   ├── middleware/           # 中间件
│   │   │   ├── middleware.go     # JWT 认证中间件
│   │   │   ├── admin_middleware.go  # 管理端中间件
│   │   │   ├── authn.go          # 认证
│   │   │   ├── compression.go   # Gzip 压缩
│   │   │   ├── header.go        # 请求头处理
│   │   │   ├── requestid.go     # RequestID
│   │   │   └── token_blacklist.go  # Token 黑名单
│   │   ├── model/                # 数据库模型 (35个文件!)
│   │   │   ├── user.go           # 用户模型
│   │   │   ├── book.go           # 笔记模型 (含 ProcessedText, OriginalText)
│   │   │   ├── card.go           # 卡片模型
│   │   │   ├── sop.go            # SOP 模型 (12KB，含模板/节点/运行/笔记)
│   │   │   ├── chat.go           # 对话模型
│   │   │   ├── knowledge_document.go  # 知识文档模型
│   │   │   ├── knowledge_chunk.go  # 知识切片模型
│   │   │   ├── sales_session.go  # 销售会话模型
│   │   │   ├── payment.go        # 支付模型
│   │   │   ├── user.go           # 用户模型 (15KB，含分层、权限)
│   │   │   └── ...               # 30+ 其他模型
│   │   ├── redis/                # Redis 封装
│   │   ├── httpclient/           # HTTP 客户端工具
│   │   │   ├── client.go         # 通用 HTTP 客户端
│   │   │   ├── streaming.go     # 流式响应处理
│   │   │   ├── json_response.go  # JSON 响应解析 (28KB)
│   │   │   ├── json_repair.go   # JSON 修复 (9KB)
│   │   │   ├── json_config.go   # JSON 配置
│   │   │   └── resume.go        # 断点续传
│   │   ├── tokenizer/           # Token 计数器
│   │   ├── util/                # 工具函数
│   │   └── known/               # 常量定义
│   │
│   └── service/                  # ═══ 外部服务调用 ═══
│       └── bailian_http.go       # 阿里云百炼 HTTP 客户端
│
├── pkg/                          # ③ 公共包（可被外部引用）
│   ├── api/                      # API 相关工具
│   ├── auth/                     # 认证工具
│   ├── db/                       # 数据库工具
│   ├── token/                    # Token 工具
│   ├── util/                     # 通用工具
│   └── version/                  # 版本信息
│
├── scripts/                      # ④ 运维/辅助脚本
│   ├── docker-entrypoint.sh      # Docker 入口脚本
│   ├── deploy_to_49.sh           # 部署到开发服务器
│   ├── execute_migration.sh      # 数据库迁移执行
│   ├── semantic_server.py        # 语义切分 Python 服务
│   ├── semantic_splitter.py      # 语义切分器
│   ├── document_parser.py        # 文档解析 (Python)
│   ├── pdf_parser.py             # PDF 解析 (Python)
│   └── ...                       # SQL 迁移脚本等
│
├── migrations/                   # ⑤ 数据库迁移文件
├── docs/                         # ⑥ 开发文档 (43个文件)
├── nginx/                        # ⑦ Nginx 配置
│
├── config_local.yaml             # ⑧ 多环境配置
├── config_dev.yaml
├── config_qa.yaml
├── config_prod.yaml
│
├── Dockerfile                    # ⑨ 容器化
├── Dockerfile.admin
├── docker-compose.yml
├── docker-compose.dev.yml
├── Taskfile.yaml                 # ⑩ 任务自动化 (task dev/lint/build)
│
├── go.mod / go.sum               # Go 依赖管理
└── .github/                      # CI/CD (GitHub Actions)
```

---

## 四、架构分层详解

### 4.1 三层架构

```
┌────────────────────────────────────────────────────┐
│                    router.go                        │  路由注册
├────────────────────────────────────────────────────┤
│              controller/v1/*                        │  控制器层
│         (参数校验 + 响应格式化)                      │  仅处理 HTTP 协议
├────────────────────────────────────────────────────┤
│                  biz/*                              │  业务逻辑层
│        (核心业务 + AI 调用 + 流程编排)               │  业务代码集中地
├────────────────────────────────────────────────────┤
│                 store/*                             │  数据访问层
│             (GORM CRUD 操作)                        │  仅负责 DB 读写
├────────────────────────────────────────────────────┤
│           internal/pkg/model/*                     │  数据模型
│            (GORM Model 定义)                        │  表结构映射
└────────────────────────────────────────────────────┘
```

### 4.2 接口驱动

- **`IBiz` 接口** (`biz/biz.go`)：聚合所有子模块 Biz 接口，是 controller 唯一依赖的入口。
- **`IStore` 接口** (`store/store.go`)：聚合所有子模块 Store 接口，biz 层通过此接口访问数据库。
- **六边形架构** (仅 `salesrag` 模块)：通过 `port/` + `adapter/` 模式解耦向量数据库、LLM 等外部依赖。

---

## 五、核心业务模块拆解

### 5.1 🤖 销售智能体 SalesRAG（最核心、最复杂）

**位置**: `biz/salesrag/`  
**文件量**: 46 个文件  
**核心文件**: `salesrag.go` (115KB, 2912行)

#### 架构（六边形/端口-适配器模式）

```
┌──────────────────────────────────────────────┐
│             salesrag.go (业务逻辑)             │
│  Ingest / Retrieve / Chat / Analyze          │
├─────────────┬────────────┬───────────────────┤
│  port/      │  domain/   │  service/          │
│  接口定义    │  领域模型   │  核心服务           │
│             │            │  Pipeline / RAG    │
├─────────────┴────────────┴───────────────────┤
│             adapter/ (适配器)                  │
│  DashVector | VikingDB | chromem | DMXAPI    │
└──────────────────────────────────────────────┘
```

#### 功能清单

| 功能 | 说明 |
|------|------|
| **文档导入 (Ingest)** | 上传文件 → COS 存储 → 解析 → 切片 → Embedding → 入向量库 |
| **知识检索 (Retrieve)** | 语义检索 + Rerank + 策略匹配 → 流式生成回答 |
| **会话管理** | 创建/删除/置顶/重命名销售会话，保存聊天历史 |
| **客户档案分析** | 上传文档/图片 → AI 生成客户画像 (Markdown) |
| **聊天风格分析** | 上传聊天截图 → OCR → AI 生成语言指纹 |
| **双模式对话** | Sales 模式 (生成话术) / Free 模式 (顾问咨询) |
| **策略匹配** | 根据问题自动匹配预设销售策略 |
| **知识库管理** | 文档 CRUD、切片查看、启用/禁用 |

#### 切片策略

- **enhanced_splitter.go**: 增强切片器，支持标题、段落、列表等 Markdown 语义
- **hybrid_splitter.go**: 混合切片器，结合语义和规则
- **embedding_splitter.go**: 基于 Embedding 的语义切片
- **splitter.go**: 基础切片器

#### 向量存储适配器

- **DashVector** (阿里云): 主要生产环境使用
- **VikingDB** (火山引擎): 备选
- **chromem-go**: 内存向量库，本地开发用
- **memory_store**: 最简内存存储

#### LLM 调用链

```
salesrag → dmxClient (DMXAPI/DeepSeek-V3.2) → 流式回复
salesrag → volcBiz  (火山引擎) → 备选/思维链
salesrag → aliBiz   (阿里云/Qwen) → 视觉分析/OCR
```

---

### 5.2 🔄 SOP 工作流引擎

**位置**: `biz/sop/`  
**核心文件**: `sop.go` (62KB) + `executor.go` (47KB)

#### 概念模型

```
Template (模板)
  └── Node (节点, 有序列表)
        ├── 类型: text_input / file_upload / ai_generate / manual_edit
        └── 配置: AI Prompt / 输入来源 / 输出格式

Run (执行记录)
  └── NodeRun (节点执行记录)
        ├── input → output
        └── status: draft/pending/running/succeeded/failed

Bookmark (书签)
  └── 保存节点输出，可在新 Run 中复用
```

#### 功能清单

| 功能 | 说明 |
|------|------|
| **模板管理** | 创建/编辑/删除 SOP 模板 |
| **节点管理** | 有序节点，支持多种类型 |
| **逐步执行** | CreateRun → GetNextNode → ExecuteNode (流式 SSE) |
| **一键执行** | ExecuteTemplate (异步执行所有节点) |
| **书签系统** | 保存节点输出为书签，后续 Run 可一键应用 |
| **文件上传** | 支持文件/图片上传，检测质量，解析文本 |
| **流式对话** | Run 完成后可基于结果继续 AI 对话 |
| **草稿清理** | 定时清理超过 8 小时的草稿 Run |
| **权限控制** | 模板授权，二级客户权限检查 |
| **批量操作** | 批量删除 Run |

#### 执行流程

```
1. 用户选择模板 → CreateRun (草稿)
2. 获取第一个节点 → GetNextNode
3. 用户输入/上传 → ExecuteNodeStream (SSE 流式)
4. AI 处理并流式返回结果
5. 循环步骤 2-4 直到所有节点完成
6. Run 完成 → 可继续 ChatAfterRun
```

---

### 5.3 🃏 卡片渲染引擎

**位置**: `biz/card/`  
**核心技术**: Chrome Headless (chromedp) 渲染 HTML → WebP 图片

#### 渲染器层级

| 渲染器 | 说明 | 大小 |
|--------|------|------|
| `renderer.go` | 基础渲染器，管理 Chrome 实例池 | 13KB |
| `flow_renderer.go` | 流式分页渲染器，支持长文分页 | 26KB |
| `lightweight_renderer.go` | 轻量渲染器，减少资源占用 | 20KB |
| `cover_renderer.go` | 封面卡片渲染器 | 20KB |
| `cover_optimizer.go` | 封面布局优化 | 10KB |

#### 渲染流程

```
Markdown 文本
  → Goldmark 解析为 HTML
  → 注入 CSS 样式 (可配置字体/间距/颜色)
  → 分页算法计算每页内容
  → chromedp 截图为 WebP
  → 上传到 COS
  → 返回图片 URL 列表
```

#### 配置系统 (config_local.yaml → card)

支持极细粒度的配置：
- 尺寸 (1080×1440)
- 字体族和大小 (标题/副标题/正文/列表...)
- 行高和间距
- 颜色和对齐
- 页码格式
- 分页算法参数

---

### 5.4 📓 笔记系统 (Book)

**位置**: `biz/book/`  
**核心文件**: `async_processor.go` (132KB，整个项目最大的文件)

| 功能 | 说明 |
|------|------|
| **异步处理流水线** | 上传文本 → AI 处理(去广告/整理结构) → 生成 Markdown → 渲染卡片 |
| **长图生成** | 整篇笔记渲染为一张长图 |
| **分页图生成** | 按内容自动分页，生成多张卡片图 |
| **分类管理** | 笔记归类到用户自定义分类 |
| **RAG 向量化** | (暂时注释) 笔记内容向量化，支持 RAG 对话 |

---

### 5.5 👤 用户体系

**位置**: `biz/user/` + `biz/customer/` + `biz/admin/`

#### 多层级架构

```
管理员 (Admin)
  └── 一级用户 (企业客户/Parent)
        └── 二级用户 (子客户/SubUser)
```

#### 功能清单

| 功能 | 说明 |
|------|------|
| **微信登录** | 小程序 code → 获取 OpenID → JWT |
| **Web 登录** | 用户名/密码 → JWT |
| **管理员登录** | 独立的管理端认证 |
| **客户管理** | 一级用户创建/管理二级客户 |
| **模板授权** | 一级用户为其二级客户授权 SOP 模板 |
| **批量授权** | 批量为多个客户授权/撤销模板 |
| **会员体系** | 会员等级、使用次数限制 |

---

### 5.6 💰 支付与订单

**位置**: `biz/payment/` + `biz/order/` + `biz/wechat/`

| 功能 | 说明 |
|------|------|
| **微信 Native 支付** | PC 端扫码支付 |
| **微信小程序支付** | 小程序内支付 |
| **支付回调** | 接收微信支付通知 |
| **订单管理** | 创建/查询订单 |
| **会员购买** | 会员套餐支付流程 |
| **账户流水** | 消费记录、余额查询 |

---

### 5.7 🔍 通用 RAG

**位置**: `biz/rag/`

基于笔记内容的 RAG 对话，支持用户与自己的笔记库进行智能问答。（当前已暂停向量化，保留代码）

---

## 六、API 双系统架构

### 6.1 用户端 API

```
POST /v1/web/login              → 用户登录
GET  /v1/chat/ws                → WebSocket 对话 (无需鉴权)

[需要 user_token 鉴权]
├── /v1/images/*                → 图片 CRUD
├── /v1/books/*                 → 笔记 CRUD + 渲染
├── /v1/cards/*                 → 卡片 CRUD
├── /v1/categories/*            → 分类 CRUD
├── /v1/templates/*             → 模板 CRUD
├── /v1/feedbacks/*             → 反馈
├── /v1/chat/*                  → 对话管理
├── /v1/articles/*              → 文章抓取/管理
├── /v1/pagination/*            → 分页渲染
├── /v1/sop/*                   → SOP 工作流 (约30个端点)
├── /v1/sales-rag/*             → 销售智能体 (约20个端点)
├── /v1/rag/*                   → 通用 RAG
├── /v1/customers/*             → 客户管理
├── /v1/users/*                 → 用户信息
├── /v1/pay/*                   → 支付
├── /v1/order/*                 → 订单
├── /v1/account/*               → 账户
├── /v1/membership/*            → 会员
├── /v1/ali/*                   → 阿里云服务
└── /v1/pdf/*                   → 文档转换
```

### 6.2 管理端 API

```
POST /v1/admin/login            → 管理员登录

[需要 admin_token 鉴权]
├── /v1/admin/sop/templates/*   → 模板管理
├── /v1/admin/sop/nodes/*       → 节点管理
├── /v1/admin/sop/runs/*        → 执行记录查看
└── /v1/admin/sop/notes/*       → 笔记查看
```

---

## 七、中间件与横切关注点

| 中间件 | 文件 | 功能 |
|--------|------|------|
| `AuthMiddleware` | middleware.go | JWT Token 验证 + 用户注入 |
| `AdminMiddleware` | admin_middleware.go | 管理端 JWT 认证 |
| `RequestID` | requestid.go | 请求追踪 ID |
| `NoCache` | header.go | 防缓存头 |
| `Cors` | header.go | CORS 跨域 |
| `Secure` | header.go | 安全头 |
| `TokenBlacklist` | token_blacklist.go | 登出 Token 失效 |
| `GzipCompression` | compression.go | Gzip 压缩 (暂禁用) |

---

## 八、配置管理

### 8.1 多环境配置

| 文件 | 环境 | 说明 |
|------|------|------|
| `config_local.yaml` | 本地开发 | 指向 Dev 数据库，Debug 模式 |
| `config_dev.yaml` | 开发环境 | Dev 服务器 (49.233.219.254:9200) |
| `config_qa.yaml` | 测试环境 | QA 服务器 (49.233.219.254:9201) |
| `config_prod.yaml` | 生产环境 | Prod 服务器 (129.28.125.51) |

### 8.2 配置层级（从高到低）

```
环境变量 (NUMIND_*) → config_local.yaml → config_dev.yaml → ...
```

### 8.3 主要配置项

- `addr` / `runmode`: 服务地址/运行模式
- `db.*`: MySQL 连接
- `redis.*`: Redis 连接
- `jwt.*`: JWT 密钥和过期时间
- `volc.*`: 火山引擎 API
- `ali.*`: 阿里云 API (文本/图像/视觉/DashVector)
- `cos.*`: 腾讯云 COS
- `vikingdb.*`: 向量数据库
- `card.*`: 卡片渲染 (尺寸/字体/间距/颜色/分页算法)
- `ai_prompts.*`: AI 提示词
- `wechat.*`: 微信支付

---

## 九、部署架构

```
┌─────────────────────────────────────────┐
│              Nginx (80/443)              │
│         反向代理 + SSL 终止              │
├─────────────────────────────────────────┤
│          numind-server (9091)            │
│            Go + Gin + GORM              │
├──────────┬──────────┬───────────────────┤
│ MySQL    │ Redis    │ 外部服务           │
│ (13306)  │ (26739)  │ COS/DashVector/   │
│          │          │ Volc/Ali/DMXAPI   │
├──────────┴──────────┴───────────────────┤
│        Prometheus (9090)                 │
│        Grafana (3000)                   │
└─────────────────────────────────────────┘
```

### 部署触发规则

| 分支/标签 | 部署环境 | 地址 |
|-----------|----------|------|
| push develop | Dev | 49.233.219.254:9200 |
| push release | QA | 49.233.219.254:9201 |
| tag v* | Prod | youshu.asia (129.28.125.51) |

---

## 十、关键数据模型（35+ 个表）

### 核心表

| 模型 | 表名 | 说明 |
|------|------|------|
| User | users | 用户 (含分层: user_type, parent_id) |
| Admin | admins | 管理员 |
| Book | books | 笔记 (含 ProcessedText, OriginalText) |
| Card | cards | 卡片 |
| SopTemplate | sop_templates | SOP 模板 |
| SopNode | sop_nodes | SOP 节点 |
| SopRun | sop_runs | SOP 执行记录 |
| SopNodeRun | sop_node_runs | 节点执行记录 |
| SopNote | sop_notes | SOP 笔记 |
| SopBookmark | sop_bookmarks | 节点书签 |
| SalesSession | sales_sessions | 销售会话 |
| SalesMessage | sales_messages | 销售消息 |
| KnowledgeDocument | knowledge_documents | 知识文档 |
| KnowledgeChunk | knowledge_chunks | 知识切片 |
| LanguageStyle | language_styles | 语言风格 |
| ChatSession | chat_sessions | 对话会话 |
| ChatMessage | chat_messages | 对话消息 |
| Category | categories | 分类 |
| Template | templates | 卡片模板 |
| Payment | payments | 支付记录 |
| Order | orders | 订单 |
| AccountRecord | account_records | 账户流水 |
| UserTemplatePermission | user_template_permissions | 用户模板权限 |
| Article | articles | 文章 |
| Feedback | feedbacks | 反馈 |
| Image | images | 图片 |

---

## 十一、代码量统计（估算）

| 模块 | 估算行数 | 占比 |
|------|----------|------|
| **SalesRAG** (biz/salesrag) | ~8,000+ | ~25% |
| **SOP 引擎** (biz/sop) | ~5,000+ | ~15% |
| **卡片渲染** (biz/card) | ~4,000+ | ~12% |
| **笔记系统** (biz/book) | ~5,000+ | ~15% |
| **Controller 层** | ~3,000+ | ~10% |
| **Store 层** | ~2,500+ | ~8% |
| **Model 层** | ~2,000+ | ~6% |
| **中间件/工具** | ~1,500+ | ~5% |
| **路由/启动** | ~1,000+ | ~3% |
| **总计** | **~32,000+** | 100% |

---

## 十二、总结：核心特征

1. **AI-First**：几乎所有核心功能都与 AI 大模型深度集成（RAG 检索、流式对话、文档分析、图片理解、文本处理）
2. **流式优先**：大量使用 SSE (Server-Sent Events) 实现流式响应，提升用户体验
3. **多 LLM 支持**：同时集成火山引擎、阿里云、DMXAPI 三大 LLM 提供商
4. **SOP 工作流**：独创的逐步执行 + 书签系统，让 AI 辅助的标准化流程可复用
5. **六边形架构**：SalesRAG 模块采用端口-适配器模式，向量数据库可替换
6. **细粒度配置**：卡片渲染系统支持像素级别的配置控制
7. **双系统架构**：用户端 + 管理端完全分离，各有独立的认证和路由
8. **多层级用户**：管理员 → 企业客户 → 子客户的三层体系
