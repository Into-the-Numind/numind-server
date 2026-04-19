# parse_eval — 文档解析质量评测工具

对 numind-server 三条文档解析链路（SOP / Chatbot KB / SalesRAG）做乱码 / 结构 / 完整性三维度打分，输出 HTML 报告。

设计：`docs/superpowers/specs/2026-04-15-document-parsing-evaluation-design.md`
实施计划：`docs/superpowers/plans/2026-04-15-document-parsing-evaluation.md`

## 前置条件

1. 本地后端已启动（`cd numind-server && task dev`）。默认 `http://localhost:9091`
2. 环境变量（建议放 `.claude/settings.local.json` 或 shell profile）：
   - `LOCAL_API_URL`（默认 `http://localhost:9091`）
   - `E2E_USERNAME` / `E2E_PASSWORD`
3. 登录账号需具备：
   - Chatbot KB：`FeatureKeySelfServiceConfig` 权限
   - SalesRAG：`FeatureKeySalesAgent` 权限
   - 若缺权限，对应链路会报 `api_error`，报告中 0 分
4. Python 3.12+（macOS: `brew install python@3.12`）

## 安装

```bash
cd numind-server/scripts/parse_eval
python3.12 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

## 样本

- `samples/synthetic/`（进 Git）：`manifest.yaml` 描述 8 份样本规格，实际 `.pdf/.docx/.xlsx/.pptx` 文件需手工制作（扫描件、多栏、带合并单元格表格、80 页长文档等），配套 `.golden.txt` 为人工标注的理想输出
- `samples/real/`（不进 Git）：从生产环境脱敏后放入的真实样本。不需要 golden

## 使用

激活 venv 后，从 `scripts/parse_eval/` 执行：

```bash
# 全部链路 × 全部样本
PYTHONPATH=.. python run_eval.py

# 只跑 SOP × 合成样本
PYTHONPATH=.. python run_eval.py --pipeline sop --sample synthetic

# 单文件
PYTHONPATH=.. python run_eval.py --pipeline salesrag --sample samples/real/xxx.pdf

# 退化检测（两次运行目录名）
PYTHONPATH=.. python run_eval.py --compare 20260415_143000 20260415_170000
```

## 输出

`reports/YYYYMMDD_HHMMSS/`：

- `report.html` — 总览矩阵 + 每样本详情（三链路 side-by-side，乱码染色）
- `summary.json` — 机器可读，供 `--compare`
- `diffs/<pipeline>__<sample>.out.txt` — 各链路原始解析输出

## 打分

| 维度 | 权重（合成样本）| 权重（真实样本）| 说明 |
|------|---------------|---------------|------|
| Encoding | 40% | 60% | UTF-8 合法性、`�` 率、控制字符、CJK 切碎 |
| Structure | 30% | — | 标题/表格/段落/列表保留（需 golden） |
| Completeness | 30% | 40% | 字符比、关键词召回 |

**等级**：≥90 Excellent / ≥75 Good / ≥60 NeedsReview / <60 Fail。Encoding FAIL（`�` 率 >0.5%）直接降为 Fail。

## 测试

```bash
PYTHONPATH=.. pytest -v
```

当前 24 passed。

## 故障排查

| 症状 | 可能原因 |
|------|---------|
| `login failed` | E2E_USERNAME/E2E_PASSWORD 错误或 LOCAL_API_URL 不通 |
| 全部 `api_error: {...code:1...}` | 账号缺少对应 feature 权限 |
| SalesRAG `poll_timeout` | 异步管线卡住（检查后端 log，embedding 服务可能未就绪） |
| 所有样本 0 分 | 确认样本文件实际存在于 `samples/synthetic/` 且文件名与 `manifest.yaml` 一致 |
