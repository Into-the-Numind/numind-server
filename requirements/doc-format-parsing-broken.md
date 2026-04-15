# .doc（旧版 Word）文档解析严重失败 — Bug 记录

**发现日期**：2026-04-15
**修复日期**：2026-04-16
**优先级**：P0（客户上传的 .doc 文件产出完全无法使用的内容）
**影响范围**：**仅 SOP 链路**（`/v1/pdf/convert-to-text` 和 `/v1/files/extract-text`）。SalesRAG 和 Chatbot KB 共用的 `EnhancedParser` 实现正确，不受影响
**状态**：✅ 已修复（见文末 Resolution 节）

---

## 1. 现象

通过 `parse_eval` 工具（dev 环境，`/v1/files/extract-text` 端点）测试一个普通 `.doc` 文件：

- **测试文件**：`/Users/zhiyuchen/Downloads/为什么找我来买房.doc`（11.5 KB，WPS Office 生成的房产中介话术文档）
- **解析输出**（前 500 字节）：

```
ࡱ>
Root EntryF g@SummaryInformation(DocumentSummaryInformation8WordDocument3
!"#$%&'()*+,-./0123456789:;<
+'px
LenovoNormal.dotmLenovo@A?O@yf@>WPS Office NNHr_0.0.0.0_{F1E327BC-269C-435d-A152-05C5408002CA}
՜.+,D՜.+,HPX`hpx8|KSOProductBuildVerKSOTemplateDocerSaveRecordICV2052-12.1.0.21915=eyJoZGlkIjoiYmY5NTJkNTRkMDdkNWM2ODM1NDFhNTZjODA0ODUxZTYifQ==$A6091F210ACC4FC9ACE02C8383BCC665_12,AA.A&0Table
Data
WpsCustomData0PKSKS3]``Or~
$hYBc@X'(:NHN~bbpN?b1HQb!kOgR/f2*NNNN/f*NNgR`O/fte*NgR`O0NN*NNv|R/fgPv(WN*N$R̀T/f
```

- 输出长度：1457 字符。几乎全部是：
  - **OLE2 复合文档结构名**：`Root Entry`, `SummaryInformation`, `DocumentSummaryInformation`, `WordDocument`, `Table`, `Data`, `WpsCustomData`
  - **WPS Office 元数据**：版本号、`KSOProductBuildVer`, `KSOTemplateDocer`, base64 编码的 GUID 串
  - **字体/模板名**：`Calibri`, `SimSun`, `Times New Roman`, `Lenovo`, `Normal.dotm`
  - 中间夹杂的"类中文字符"（如 `NN~bbpN?b`, `pencvRgZS_̑vZS_`）其实是 GBK/CP936 编码的中文字节被错误当作 UTF-8 读取产生的 mojibake

**结论**：解析器没有真正"解析" .doc 二进制结构，只是把整个文件当作文本 dump 了出来。

## 2. 根因分析（假设）

.doc 是 OLE2 Compound Document 格式（二进制），内部的正文存储在 `WordDocument` stream 里，需要专门的解析库才能提取。

当前三条链路的解析后端：

| 链路 | 后端 |
|------|------|
| 主路径 | Python `MarkItDown`（`scripts/document_parser.py`）|
| 降级 | Go `go-fitz`（`gen2brain/go-fitz`，基于 MuPDF）|

**怀疑**：
- MarkItDown 对 .doc 的支持依赖外部工具（如 `libreoffice`、`antiword`、`catdoc`）。如果后端 Docker 镜像里没装这些依赖，MarkItDown 会 fallback 到"按文本读取"
- go-fitz 本身只支持 PDF/XPS/EPUB 等，对 .doc 完全不支持，但没有正确报错，直接返回了原始字节

需要进一步验证：
1. 看后端 log 中 MarkItDown 调用是否抛异常，以及是否进入了 fallback 分支
2. 看生产 Docker 镜像里是否安装了 `libreoffice-core` 或 `antiword`
3. go-fitz 对 .doc 的错误处理路径

## 3. 影响范围

- **SOP**：`/v1/pdf/convert-to-text` 和轻量 `/v1/files/extract-text` 均走同一解析器
- **Chatbot 知识库**：`/v1/config/knowledge-bases/:id/documents` 上传 → 共用 `EnhancedParser`
- **SalesRAG**：`/v1/sales-rag/ingest` → 同一解析路径 + 后续 embedding/检索被污染

**业务影响**：
- 客户上传 .doc 文件建立 SalesRAG 销售知识库 → 知识库里全是二进制噪音，检索回来的 chunk 对 LLM 毫无价值，回答质量崩塌
- SOP 流程里读取 .doc 文档作为输入节点 → LLM 收到的上下文是 OLE2 元数据，下游卡片/决策全部错误
- 知识库功能的客户试用会立即发现问题

## 4. 修复方向

| 方案 | 成本 | 可靠性 |
|------|------|--------|
| A. 后端 Docker 镜像安装 `libreoffice-core`，MarkItDown 会自动用它转 .docx 再解析 | 中（镜像体积 +200MB） | 高 |
| B. 增加 `antiword`/`catdoc` 作为轻量 .doc 专用工具，Python 侧显式调用 | 低（二进制 <5MB） | 中（不支持表格） |
| C. 前端/后端拒绝 .doc 上传，强制要求用户转为 .docx | 0 | —（回避问题） |

建议走 **A**，LibreOffice 同时能提升 .doc/.docx/.xls/.xlsx/.ppt/.pptx 等旧格式的解析质量，是一次性投资。

## 5. 验证方式

修复后用 `parse_eval` 工具重新对 `为什么找我来买房.doc` 跑一次：
```bash
cd numind-server/scripts/parse_eval && source .venv/bin/activate
LOCAL_API_URL=$DEV_API_URL PYTHONPATH=.. python run_eval.py \
  --pipeline all \
  --sample "/Users/zhiyuchen/Downloads/为什么找我来买房.doc"
open reports/$(ls -1t reports | head -1)/report.html
```

**预期**：三条链路输出应为可读中文正文，包含"中介"、"服务"、"购房"等关键词；当前输出几乎全是二进制结构名。

## 6. 关联 Bug：评测工具 encoding metric 漏检

`parse_eval/metrics/encoding.py` 对本次失败输出打了满分（40/40）。原因：二进制 dump 后的 ASCII 字符串"技术上"是合法 UTF-8，没有 U+FFFD 替换字符，没有控制字符，也没有带空格的 CJK。

后续优化：
- 增加"二进制签名检测"：出现 `Root Entry`、`WordDocument`、`SummaryInformation`、`PK\x03\x04`（ZIP）、`%PDF` 等结构名但上下文非代码时，判为 dump 失败
- 增加"CJK 密度异常"：中文文档（根据文件名/上下文可推断）若 CJK 占比极低（<5%），提示可疑

## 7. Resolution（2026-04-16 复盘与修复）

**前述第 2 节的根因分析是错的**，实际不是缺依赖的问题，是调用代码的 bug。

### 真实根因

`internal/numind/controller/v1/pdf/pdf.go` 的 `extractTextFromPDFEnhanced` 函数内部用 `os.CreateTemp("", "pdf_upload_*.pdf")` **硬编码了 `.pdf` 扩展名**。`extractTextFromDOC` 和 `extractTextFromDOCX` 为图方便都复用了这个函数（见原代码注释"复用已有的外部脚本执行逻辑"）。

结果：
1. .doc 文件进来 → 写临时文件 `xxx.pdf` → 传给 `document_parser.py`
2. Python 脚本按扩展名分派：`if ext == ".doc": use antiword; else: use MarkItDown`
3. 看到 `.pdf` → 走 MarkItDown → MarkItDown 认不出 OLE2 二进制 → 失败
4. Go 端 fallback 到 `extractTextFromDOCLegacy` → `extractPrintableText` 在 OLE2 字节上跑 → 产出我们看到的"Root Entry / WordDocument"垃圾

### 为什么 SalesRAG / Chatbot KB 不受影响

它们共用 `internal/numind/biz/salesrag/adapter/enhanced_parser.go`，该文件有独立实现，`extractTextFromDOC` 正确使用 `os.CreateTemp("", "doc_upload_*.doc")`——`.doc` 后缀。Python 正确路由到 antiword，解析成功。

### 修复

将 `extractTextFromPDFEnhanced` 重命名为 `runDocumentParser(data []byte, ext string)`，让 3 个调用方各自传入正确的扩展名（`.pdf` / `.docx` / `.doc`）。

```diff
- func extractTextFromPDFEnhanced(data []byte) (string, int, error) {
-     tmpFile, err := os.CreateTemp("", "pdf_upload_*.pdf")
+ func runDocumentParser(data []byte, ext string) (string, int, error) {
+     tmpFile, err := os.CreateTemp("", "upload_*"+ext)
```

`antiword` 已在 `Dockerfile` line 54 安装（`apt-get install -y ... antiword`），不需要额外部署动作，镜像重建即可。

### 教训

- **假 P0 警告**：修复前我判断"影响三条链路"——因为假定底层解析器共用。实际上 SOP 自己实现了一套，和 SalesRAG/Chatbot 的 `EnhancedParser` 是两份代码，两者只是巧合地都叫"增强解析器"
- **Cargo-cult 复用**：`extractTextFromPDFEnhanced` 被 DOC/DOCX 复用时没人发现文件名后缀硬编码的影响
- **测试缺失**：SOP pipeline 没有针对 .doc 的回归测试；只有在我们用 `parse_eval` 工具手工跑时才暴露

这个优化独立于本 bug，应作为 `parse_eval` 工具的增强项另开 task。
