# 统一文档解析器 — 迁移 Spec

**日期**: 2026-04-19
**范围**: numind-server 单仓库
**类型**: 重构（消除重复代码，不改变外部 API 行为）

## 1. 目标

将 `controller/v1/pdf/pdf.go` 中 ~600 行自有解析代码替换为调用共享的 `EnhancedParser`，消除"两套解析器"问题。

## 2. 当前状态

```
controller/v1/pdf/pdf.go (1147 行)
├── ConvertToText()     ← SOP 端点，带 run_id + COS 上传 + DB 保存
├── ExtractText()       ← 轻量端点，/chatbot 文件上传用
├── extractTextFromPDF/DOC/DOCX/RTF + Legacy fallbacks  ← 自有解析 (~600 行)
├── runDocumentParser() ← 调 Python document_parser.py
└── formatPdfText/formatText ← 文本清洗

biz/salesrag/adapter/enhanced_parser.go (680 行)
├── Parse(ctx, io.Reader, filename) (string, error)  ← 公共入口
├── extractTextFromPDF/DOC/DOCX + Legacy fallbacks    ← 完整解析 + fallback
├── extractTextFromPDFEnhanced() ← 调同一个 Python document_parser.py
└── formatPdfText/formatText     ← 相同逻辑的文本清洗
```

**问题**: 两套代码做同一件事。pdf.go 支持 6 格式，EnhancedParser 支持 9 格式。

## 3. 目标状态

```
internal/pkg/parser/document_parser.go (从 enhanced_parser.go 搬迁，改包名)
├── Parse(ctx, io.Reader, filename) (string, error)  ← 唯一真相源
└── 所有内部解析 + fallback + 格式化

controller/v1/pdf/pdf.go (~300 行，只保留 controller 职责)
├── ConvertToText()  ← 参数校验 + 调 parser.Parse() + COS + DB
├── ExtractText()    ← 参数校验 + 调 parser.Parse() + 返回
└── sanitizeUTF8ForDatabase() ← 数据库安全清洗（保留）
```

## 4. 迁移步骤

### T1: 搬迁 EnhancedParser → internal/pkg/parser/

- 复制 `biz/salesrag/adapter/enhanced_parser.go` → `internal/pkg/parser/document_parser.go`
- 改 `package adapter` → `package parser`
- 类型名从 `EnhancedParser` 改为 `DocumentParser`（更通用）
- 构造函数 `NewDocumentParser()` 
- 公共方法签名不变: `Parse(ctx, io.Reader, filename) (string, error)`

### T2: 更新 SalesRAG + biz 层 import

- `biz/salesrag/service/strategy_service.go`: import 改为 `internal/pkg/parser`
- `biz/biz.go`: import 改为 `internal/pkg/parser`
- 类型引用 `adapter.EnhancedParser` → `parser.DocumentParser`

### T3: 改造 pdf.go

**ExtractText handler**:
- 删除 `supportedExts` 硬编码白名单 — 改为调 `parser.DocumentParser.Parse()`，由 parser 内部判断格式
- 删除 switch/case 分发 — 一行替代: `text, err := dp.Parse(c.Request.Context(), src, file.Filename)`
- 保留: 文件大小校验、`sanitizeUTF8ForDatabase`、MaxTextContentLength 截断

**ConvertToText handler**:
- 同上替换解析部分
- 保留: run_id/node_id 校验、COS 上传、SopFile DB 记录创建
- 扩展 supportedExts 提示信息（告知用户现在支持 xlsx/pptx 等）

### T4: 删除 pdf.go 废弃代码

删除以下函数（全部已被 parser 包替代）:
- `runDocumentParser`
- `extractTextFromPDF` / `extractTextFromPDFLegacy`
- `extractTextFromDOC` / `extractTextFromDOCLegacy`
- `extractTextFromDOCX` / `extractTextFromDOCXLegacy` / `extractTextFromDOCXXML`
- `extractTextFromRTF`
- `extractPrintableText`
- `formatPdfText` / `formatText`

保留:
- `sanitizeFileName`
- `sanitizeUTF8ForDatabase`
- `MaxFileSize` / `MaxTextContentLength` 常量

### T5: 删除 biz/salesrag/adapter/ 中的旧文件

- 删除 `enhanced_parser.go`（已搬到 `internal/pkg/parser/`）
- 确认 adapter 包中其他文件（如 `dmxapi_client.go`）不受影响

## 5. 不变的行为

- 外部 API path / method / request / response 格式完全不变
- COS 上传逻辑不变
- SopFile DB 记录不变
- 认证/鉴权不变
- 已有的 SalesRAG / Chatbot KB 解析行为不变

## 6. 变化的行为

- `/v1/files/extract-text` 和 `/v1/pdf/convert-to-text` 新增支持: `.xlsx`, `.pptx`, `.html`
- `.docx` 解析质量提升（MarkItDown 完整 converter 替代 Go XML fallback）
- `.rtf` 解析由 MarkItDown 处理（替代 pdf.go 自写正则，质量更高）
- 纯图片 PDF（scanned_image）返回空文本 + 明确错误信息，不再 500 crash

## 7. 验证

改造完成后用 parse_eval 工具跑基线 + `--compare` 对比:
```bash
cd scripts/parse_eval && source .venv/bin/activate
LOCAL_API_URL=$DEV_API_URL PYTHONPATH=.. python run_eval.py --pipeline sop --sample synthetic
PYTHONPATH=.. python run_eval.py --compare 20260419_124109 <new_run_id>
```

预期: xlsx/pptx 从 0 → >60；docx 保持 97；其余不退化。
