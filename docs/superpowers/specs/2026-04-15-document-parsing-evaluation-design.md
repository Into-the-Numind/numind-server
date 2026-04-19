# 文档解析质量评测工具 — 设计文档

**创建日期**：2026-04-15
**状态**：Draft, 待实施
**作者**：brainstorming session
**仓库**：numind-server（工具位于 `scripts/parse_eval/`）

---

## 1. 背景与动机

系统中存在三条文档解析链路，均服务于用户上传的客户文档，但在不同业务场景下使用：

| 链路 | 入口 | 后处理 | 同步/异步 |
|------|------|--------|----------|
| **SOP** | `controller/v1/pdf/pdf.go::ConvertToText` | `formatPdfText` | 同步，返回文本 |
| **智能体（Chatbot KB）** | `controller/v1/config/knowledge_base.go::UploadDocuments` | `EnhancedParser` + DB 存储 | 同步 |
| **SalesRAG** | `controller/v1/salesrag/sales_rag.go::Ingest` | 解析→切分→打标→embedding | 异步管线 |

三条链路共用底层解析后端（Python MarkItDown 主路径 + go-fitz 降级），但各自有独立的后处理逻辑。生产反馈存在乱码、漏内容、结构丢失等问题，需要一套可复用的评测工具，在每次调整解析链路后快速回归，定位问题出在解析层还是后处理层。

## 2. 目标

- **主目标**：建立一套可复用的 CLI 评测工具，对三条链路 × 多种格式样本输出量化分数与 diff 报告，覆盖乱码/结构/完整性三个维度
- **次目标**：支持两次运行结果对比，任何样本分数退化 >10 分时告警
- **非目标**：
  - 不做 Web UI / 实时 dashboard
  - 不做自动修复建议
  - 不集成到 CI
  - 不做图像 OCR 效果专项评估

## 3. 工具架构

### 3.1 技术栈选型

使用 **Python** 实现，位于 `numind-server/scripts/parse_eval/`。理由：
- 与现有 `scripts/document_parser.py` 同一生态，可直接复用依赖
- 文本相似度、n-gram 困惑度、HTML 报告生成等任务 Python 生态更成熟
- 评测工具不进生产链路，不需要 Go 的性能与部署要求

### 3.2 目录结构

```
scripts/parse_eval/
├── run_eval.py              # CLI 入口
├── pipelines/               # 三条链路的调用封装
│   ├── __init__.py
│   ├── sop.py               # 通过 HTTP 调本地后端 SOP 接口
│   ├── chatbot.py           # 通过 HTTP 调智能体知识库接口
│   └── salesrag.py          # 通过 HTTP 调 SalesRAG + 轮询异步状态
├── metrics/                 # 三维度打分器
│   ├── __init__.py
│   ├── encoding.py          # 乱码维度
│   ├── structure.py         # 结构维度
│   └── completeness.py      # 完整性维度
├── samples/
│   ├── synthetic/           # 8 份合成样本 + golden/ 金标准 txt（进 Git）
│   └── real/                # ~5 份真实样本（.gitignore，本地放）
├── reports/                 # 输出目录（.gitignore）
│   └── 20260415_143000/
│       ├── report.html
│       ├── summary.json
│       └── diffs/
├── requirements.txt
└── README.md
```

### 3.3 调用策略

所有链路都走**真实的后端 HTTP API**，而非直接调用 MarkItDown 脚本。原因：只有走真实路径才能覆盖各自的后处理层（`formatPdfText`、`HybridSplitter` 等）。

前置要求：
- 本地后端已启动（`task dev`）
- 凭据从环境变量读取：`E2E_USERNAME`, `E2E_PASSWORD`, `LOCAL_API_URL`
- SalesRAG 异步管线通过轮询等待 `status=COMPLETED`，超时 5 分钟

## 4. 样本集

### 4.1 合成样本（8 份，进 Git）

每份样本附带手工标注的 `<name>.golden.txt` 作为完整性与结构基准。

| # | 文件名 | 格式 | 覆盖场景 |
|---|--------|------|---------|
| 1 | `cn_en_mixed.pdf` | PDF | 中英混排 + 数字 + 半角/全角标点 |
| 2 | `scanned_image.pdf` | PDF | 纯扫描件（图片型，无文本层） |
| 3 | `complex_table.docx` | DOCX | 合并单元格、嵌套表格 |
| 4 | `multi_column.pdf` | PDF | 双栏/三栏排版 |
| 5 | `long_doc_80p.pdf` | PDF | 长文档，测漏页 / 页眉页脚重复 |
| 6 | `legacy.doc` | DOC | 老 Word 格式（MarkItDown 弱项） |
| 7 | `data.xlsx` | XLSX | 多 sheet + 公式 + 合并单元格 |
| 8 | `slides.pptx` | PPTX | 含备注、SmartArt、中文字体 |

### 4.2 真实样本（~5 份，本地）

从生产 COS 按维度分层抽样：
- 2 份客户常用 PDF
- 1 份 DOCX
- 1 份 XLSX
- 1 份历史出过问题的文档

脱敏处理（打码手机号/姓名）后放入 `samples/real/`。**不做金标准**，仅用于乱码指标自动检测 + 人工抽检结构。

### 4.3 .gitignore 调整

在 `numind-server/.gitignore` 追加：
```
scripts/parse_eval/samples/real/
scripts/parse_eval/reports/
```

## 5. 评分方法

每条链路对每份样本输出综合分 **0-100**，由三个维度加权。

### 5.1 维度 A — 乱码（权重 40%，硬红线）

无需金标准，自动计算：

| 指标 | 定义 | 扣分规则 |
|------|------|---------|
| `utf8_valid` | 输出是否合法 UTF-8 | 不合法直接计 0 分（整体 FAIL） |
| `replacement_char_rate` | `�` (U+FFFD) 占总字符比例 | >0.5% 扣满 40 分 |
| `control_char_rate` | 非常规控制字符（排除 `\n\r\t`） | 每 0.1% 扣 2 分 |
| `cjk_broken_rate` | 中文字符被切碎（单字间插空格） | 启发式检测，命中扣 10-30 分 |
| `gibberish_ngram` | 中英 bigram 频率困惑度 | 超阈值扣 10-20 分 |

任一硬指标触发 → 整体降级为 **FAIL**，报告红色标注。

### 5.2 维度 B — 结构（权重 30%，启发式）

仅对有金标准的合成样本计算：

| 指标 | 定义 |
|------|------|
| `heading_preserved` | 金标准中标题行（`#`/`##`）是否在输出里出现 |
| `table_detected` | 样本若含表格，输出是否含 Markdown 表格 `\|...\|` 或 tab 分隔 |
| `paragraph_count_ratio` | 输出段落数 / 金标准段落数（0.7-1.3 合格） |
| `list_preserved` | `- ` / `1. ` 列表项保留率 |

真实样本跳过此维度。

### 5.3 维度 C — 完整性（权重 30%）

| 指标 | 定义 |
|------|------|
| `char_count_ratio` | `len(output) / len(golden)`（0.85-1.15 合格，<0.5 视为严重漏内容） |
| `keyword_recall` | 从金标准抽 10 个高频名词，输出命中率 |
| `page_coverage` | （PDF 专属）每页首行关键字是否都出现在输出中，检测漏页 |

### 5.4 总分公式

- 合成样本：`Score = 0.4·A + 0.3·B + 0.3·C`
- 真实样本：`Score = 0.6·A + 0.4·C`

### 5.5 等级划分

| 分数 | 等级 |
|------|------|
| 90-100 | ✅ Excellent |
| 75-89 | 🟡 Good |
| 60-74 | 🟠 Needs Review |
| <60 或乱码 FAIL | 🔴 Fail |

## 6. CLI 设计

```bash
cd numind-server/scripts/parse_eval

# 跑三条链路 × 全部样本
python run_eval.py --pipeline all

# 只跑 SOP × 合成样本
python run_eval.py --pipeline sop --sample synthetic

# 单文件评测
python run_eval.py --pipeline salesrag --sample samples/real/xxx.pdf

# 两次报告对比（退化检测）
python run_eval.py --compare 20260410_143000 20260415_143000
```

## 7. 报告产出

输出到 `reports/YYYYMMDD_HHMMSS/`：

- **`report.html`**：
  - 顶部总览矩阵（3 链路 × 13 样本 = 39 格，每格显示分数 + 彩色状态）
  - 每个样本的展开区：原文预览 + 三条链路输出 side-by-side + diff 高亮乱码字符
  - 聚合视图：按文件类型分组的平均分

- **`summary.json`**：机器可读结果，供历史对比使用

- **`diffs/<pipeline>_<sample>.txt`**：纯文本 diff，便于分享调试

### 7.1 关键视图

1. **链路对比**：同一样本在三条链路上的分数并排，快速定位问题是解析层还是后处理层
2. **格式分布**：按 PDF/DOCX/XLSX/PPTX 等聚合平均分，回答"哪类格式是弱项"
3. **退化检测**：`--compare` 显示两次运行的分差，任何样本掉分 >10 标红告警

## 8. 隐私与安全

- 真实样本不进 Git，放 `samples/real/`，加入 `.gitignore`
- 真实样本使用前手工脱敏（打码手机号、姓名、公司名等 PII）
- 报告目录同样不进 Git（可能含真实样本内容片段）
- 凭据通过环境变量注入，禁止硬编码

## 9. 未覆盖 / 未来扩展

- 图像 OCR 效果评估（扫描件中的文字识别准确率）
- qwen-long / 百度 OCR 等第三方解析服务的对比接入
- CI 集成（等工具稳定 + 合成样本基线固化后再考虑）
- 自动修复建议（基于历史失败模式给出 `formatPdfText` 正则调整建议）

## 10. 交付清单

实施阶段需交付：

- [ ] `scripts/parse_eval/` 目录结构与所有 Python 模块
- [ ] 8 份合成样本 + 对应 `.golden.txt`
- [ ] 三条链路的 HTTP 客户端封装
- [ ] 三维度打分器实现
- [ ] HTML 报告模板与 diff 渲染
- [ ] `--compare` 两次运行对比
- [ ] `.gitignore` 追加规则
- [ ] `README.md` 使用文档（含环境变量要求、本地后端启动步骤）
- [ ] 一次完整的基线运行，产出 `reports/baseline_*/`，记录当前解析质量水位
