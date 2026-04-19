# Document Parsing Evaluation Tool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Python CLI at `numind-server/scripts/parse_eval/` that evaluates three document parsing pipelines (SOP / Chatbot KB / SalesRAG) across synthetic + real samples, producing HTML reports with per-sample scores across encoding / structure / completeness dimensions.

**Architecture:** Python package with three sub-modules — `pipelines/` (HTTP clients for each backend route), `metrics/` (pure scoring functions, unit-tested with TDD), `report/` (Jinja2 HTML renderer + diff generation). Runner orchestrates the matrix `pipelines × samples`, collects outputs, scores them, emits `reports/YYYYMMDD_HHMMSS/`.

**Tech Stack:** Python 3.10+, `requests` (HTTP), `pytest` (tests), `jinja2` (HTML), `difflib` (diffs). No heavy ML deps — only frequency-table based gibberish detection.

**Spec:** `numind-server/docs/superpowers/specs/2026-04-15-document-parsing-evaluation-design.md`

**Working directory for all steps:** `numind-server/` (commands relative to repo root unless noted).

---

### Task 1: Scaffolding + dependencies + gitignore

**Files:**
- Create: `scripts/parse_eval/requirements.txt`
- Create: `scripts/parse_eval/pyproject.toml` (for pytest discovery)
- Create: `scripts/parse_eval/__init__.py`
- Create: `scripts/parse_eval/pipelines/__init__.py`
- Create: `scripts/parse_eval/metrics/__init__.py`
- Create: `scripts/parse_eval/report/__init__.py`
- Create: `scripts/parse_eval/tests/__init__.py`
- Create: `scripts/parse_eval/samples/synthetic/.gitkeep`
- Create: `scripts/parse_eval/samples/real/.gitkeep`
- Modify: `.gitignore` (append parse_eval rules)

- [ ] **Step 1: Create directory skeleton**

```bash
cd numind-server
mkdir -p scripts/parse_eval/{pipelines,metrics,report,tests,samples/synthetic,samples/real,reports}
touch scripts/parse_eval/{__init__.py,pipelines/__init__.py,metrics/__init__.py,report/__init__.py,tests/__init__.py}
touch scripts/parse_eval/samples/synthetic/.gitkeep
touch scripts/parse_eval/samples/real/.gitkeep
```

- [ ] **Step 2: Write requirements.txt**

Create `scripts/parse_eval/requirements.txt`:

```
requests>=2.31
jinja2>=3.1
pytest>=7.4
pytest-cov>=4.1
```

- [ ] **Step 3: Write pyproject.toml**

Create `scripts/parse_eval/pyproject.toml`:

```toml
[tool.pytest.ini_options]
testpaths = ["tests"]
python_files = ["test_*.py"]
```

- [ ] **Step 4: Append gitignore rules**

Append to `numind-server/.gitignore`:

```
# parse_eval tool
scripts/parse_eval/samples/real/*
!scripts/parse_eval/samples/real/.gitkeep
scripts/parse_eval/reports/
scripts/parse_eval/__pycache__/
scripts/parse_eval/**/__pycache__/
scripts/parse_eval/.pytest_cache/
```

- [ ] **Step 5: Install deps locally to verify**

```bash
cd scripts/parse_eval
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
pytest --version
```

Expected: `pytest 7.x.x` printed. Add `.venv/` to step 4's gitignore if not already covered.

- [ ] **Step 6: Commit**

```bash
cd numind-server
git add scripts/parse_eval .gitignore
git commit -m "chore: scaffold parse_eval tool directory structure"
```

---

### Task 2: Encoding metric (TDD)

**Files:**
- Create: `scripts/parse_eval/metrics/encoding.py`
- Test: `scripts/parse_eval/tests/test_encoding.py`

- [ ] **Step 1: Write failing tests**

Create `scripts/parse_eval/tests/test_encoding.py`:

```python
from parse_eval.metrics.encoding import score_encoding, EncodingResult


def test_clean_text_scores_full():
    result = score_encoding("你好，世界！Hello world 123.")
    assert result.score == 40
    assert result.failed is False
    assert result.replacement_char_rate == 0.0


def test_replacement_char_triggers_fail():
    text = "正常文本" + "\ufffd" * 50 + "更多文本"
    result = score_encoding(text)
    assert result.failed is True
    assert result.score == 0


def test_control_chars_deduct():
    text = "正常" + "\x01\x02\x03\x04\x05" * 20 + "尾部"
    result = score_encoding(text)
    assert result.score < 40
    assert result.control_char_rate > 0


def test_cjk_broken_detected():
    text = "你 好 世 界 这 是 中 文 " * 5
    result = score_encoding(text)
    assert result.cjk_broken_rate > 0.5
    assert result.score < 40


def test_newline_tab_not_penalised():
    result = score_encoding("第一行\n第二行\t缩进")
    assert result.control_char_rate == 0.0
    assert result.score == 40
```

- [ ] **Step 2: Run tests, confirm failure**

```bash
cd scripts/parse_eval && source .venv/bin/activate
PYTHONPATH=.. pytest tests/test_encoding.py -v
```

Expected: `ModuleNotFoundError: No module named 'parse_eval.metrics.encoding'`

- [ ] **Step 3: Implement encoding.py**

Create `scripts/parse_eval/metrics/encoding.py`:

```python
from dataclasses import dataclass
import re

CJK_RANGE = re.compile(r"[\u4e00-\u9fff]")
CJK_WITH_SPACE = re.compile(r"[\u4e00-\u9fff]\s[\u4e00-\u9fff]")
CONTROL_CHARS = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f]")


@dataclass
class EncodingResult:
    score: float
    failed: bool
    replacement_char_rate: float
    control_char_rate: float
    cjk_broken_rate: float
    notes: list[str]


def score_encoding(text: str) -> EncodingResult:
    notes: list[str] = []
    if not text:
        return EncodingResult(0.0, True, 0.0, 0.0, 0.0, ["empty output"])

    n = len(text)

    try:
        text.encode("utf-8")
    except UnicodeEncodeError:
        return EncodingResult(0.0, True, 0.0, 0.0, 0.0, ["utf8_invalid"])

    rep_count = text.count("\ufffd")
    rep_rate = rep_count / n
    ctrl_count = len(CONTROL_CHARS.findall(text))
    ctrl_rate = ctrl_count / n

    cjk_chars = CJK_RANGE.findall(text)
    cjk_broken_rate = 0.0
    if len(cjk_chars) >= 4:
        broken_pairs = len(CJK_WITH_SPACE.findall(text))
        cjk_broken_rate = broken_pairs / max(1, len(cjk_chars) - 1)

    if rep_rate > 0.005:
        notes.append(f"replacement_char_rate={rep_rate:.2%} exceeds 0.5%")
        return EncodingResult(0.0, True, rep_rate, ctrl_rate, cjk_broken_rate, notes)

    score = 40.0
    score -= min(10.0, ctrl_rate * 1000 * 2)
    if cjk_broken_rate > 0.3:
        score -= 30.0
        notes.append(f"cjk_broken_rate={cjk_broken_rate:.2%}")
    elif cjk_broken_rate > 0.1:
        score -= 10.0
        notes.append(f"cjk_broken_rate={cjk_broken_rate:.2%}")

    score = max(0.0, score)
    return EncodingResult(score, False, rep_rate, ctrl_rate, cjk_broken_rate, notes)
```

- [ ] **Step 4: Run tests, confirm pass**

```bash
PYTHONPATH=.. pytest tests/test_encoding.py -v
```

Expected: 5 passed.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add scripts/parse_eval/metrics/encoding.py scripts/parse_eval/tests/test_encoding.py
git commit -m "feat(parse_eval): add encoding quality scorer with unit tests"
```

---

### Task 3: Completeness metric (TDD)

**Files:**
- Create: `scripts/parse_eval/metrics/completeness.py`
- Test: `scripts/parse_eval/tests/test_completeness.py`

- [ ] **Step 1: Write failing tests**

Create `scripts/parse_eval/tests/test_completeness.py`:

```python
from parse_eval.metrics.completeness import score_completeness, CompletenessResult


def test_exact_match_scores_full():
    golden = "这是一份测试文档。它有两段。" * 10
    output = golden
    result = score_completeness(output, golden, keywords=["测试", "文档", "两段"])
    assert result.score == 30
    assert 0.85 <= result.char_count_ratio <= 1.15
    assert result.keyword_recall == 1.0


def test_heavy_content_loss_scores_low():
    golden = "完整内容 " * 200
    output = "完整内容"
    result = score_completeness(output, golden, keywords=["完整", "内容"])
    assert result.score < 10
    assert result.char_count_ratio < 0.5


def test_keyword_recall_partial():
    golden = "苹果 香蕉 橙子 葡萄 西瓜"
    output = "苹果 香蕉 橙子"
    result = score_completeness(output, golden, keywords=["苹果", "香蕉", "橙子", "葡萄", "西瓜"])
    assert result.keyword_recall == 0.6
```

- [ ] **Step 2: Run tests, confirm failure**

```bash
PYTHONPATH=.. pytest tests/test_completeness.py -v
```

Expected: `ModuleNotFoundError`.

- [ ] **Step 3: Implement completeness.py**

Create `scripts/parse_eval/metrics/completeness.py`:

```python
from dataclasses import dataclass


@dataclass
class CompletenessResult:
    score: float
    char_count_ratio: float
    keyword_recall: float
    notes: list[str]


def score_completeness(output: str, golden: str, keywords: list[str]) -> CompletenessResult:
    notes: list[str] = []
    if not golden:
        return CompletenessResult(0.0, 0.0, 0.0, ["no golden reference"])

    ratio = len(output) / len(golden)
    if ratio < 0.5:
        char_score = 0.0
        notes.append(f"char_count_ratio={ratio:.2f} < 0.5 (severe content loss)")
    elif 0.85 <= ratio <= 1.15:
        char_score = 20.0
    elif 0.7 <= ratio < 0.85 or 1.15 < ratio <= 1.3:
        char_score = 14.0
    else:
        char_score = 7.0

    if keywords:
        hits = sum(1 for kw in keywords if kw in output)
        recall = hits / len(keywords)
    else:
        recall = 1.0

    kw_score = 10.0 * recall
    if recall < 1.0:
        missed = [kw for kw in keywords if kw not in output]
        notes.append(f"missed_keywords={missed}")

    return CompletenessResult(char_score + kw_score, ratio, recall, notes)
```

- [ ] **Step 4: Run tests, confirm pass**

```bash
PYTHONPATH=.. pytest tests/test_completeness.py -v
```

Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/parse_eval/metrics/completeness.py scripts/parse_eval/tests/test_completeness.py
git commit -m "feat(parse_eval): add completeness scorer with char ratio + keyword recall"
```

---

### Task 4: Structure metric (TDD)

**Files:**
- Create: `scripts/parse_eval/metrics/structure.py`
- Test: `scripts/parse_eval/tests/test_structure.py`

- [ ] **Step 1: Write failing tests**

Create `scripts/parse_eval/tests/test_structure.py`:

```python
from parse_eval.metrics.structure import score_structure, StructureResult


def test_headings_preserved():
    golden = "# 标题一\n内容1\n## 标题二\n内容2"
    output = "# 标题一\n内容1\n## 标题二\n内容2"
    result = score_structure(output, golden, has_table=False)
    assert result.heading_recall == 1.0


def test_missing_headings():
    golden = "# A\nx\n# B\ny\n# C\nz"
    output = "# A\nx\nB\ny\nC\nz"
    result = score_structure(output, golden, has_table=False)
    assert result.heading_recall < 0.5


def test_table_detected_when_expected():
    output = "| a | b |\n|---|---|\n| 1 | 2 |"
    result = score_structure(output, "| a | b |", has_table=True)
    assert result.table_detected is True


def test_table_missing_when_expected_deducts():
    output = "a b\n1 2"
    result = score_structure(output, "| a | b |", has_table=True)
    assert result.table_detected is False
    assert result.score < 30


def test_paragraph_ratio_in_range():
    golden = "第一段\n\n第二段\n\n第三段"
    output = "第一段\n\n第二段\n\n第三段"
    result = score_structure(output, golden, has_table=False)
    assert 0.7 <= result.paragraph_count_ratio <= 1.3
```

- [ ] **Step 2: Run tests, confirm failure**

```bash
PYTHONPATH=.. pytest tests/test_structure.py -v
```

Expected: `ModuleNotFoundError`.

- [ ] **Step 3: Implement structure.py**

Create `scripts/parse_eval/metrics/structure.py`:

```python
from dataclasses import dataclass
import re

HEADING_RE = re.compile(r"^#{1,6}\s+(.+)$", re.MULTILINE)
TABLE_ROW_RE = re.compile(r"\|.+\|")
LIST_ITEM_RE = re.compile(r"^\s*(?:[-*]\s+|\d+\.\s+)", re.MULTILINE)


@dataclass
class StructureResult:
    score: float
    heading_recall: float
    table_detected: bool
    paragraph_count_ratio: float
    list_recall: float
    notes: list[str]


def _extract_headings(text: str) -> list[str]:
    return [m.group(1).strip() for m in HEADING_RE.finditer(text)]


def _count_paragraphs(text: str) -> int:
    return len([p for p in text.split("\n\n") if p.strip()])


def _count_list_items(text: str) -> int:
    return len(LIST_ITEM_RE.findall(text))


def score_structure(output: str, golden: str, has_table: bool) -> StructureResult:
    notes: list[str] = []

    gold_headings = _extract_headings(golden)
    out_headings = _extract_headings(output)
    if gold_headings:
        hits = sum(1 for h in gold_headings if h in out_headings)
        heading_recall = hits / len(gold_headings)
    else:
        heading_recall = 1.0

    table_detected = bool(TABLE_ROW_RE.search(output))
    if has_table and not table_detected:
        notes.append("expected table but none detected in output")

    gold_paras = max(1, _count_paragraphs(golden))
    out_paras = _count_paragraphs(output)
    para_ratio = out_paras / gold_paras

    gold_lists = _count_list_items(golden)
    out_lists = _count_list_items(output)
    list_recall = min(1.0, out_lists / gold_lists) if gold_lists else 1.0

    score = 0.0
    score += 12.0 * heading_recall
    score += 8.0 if (not has_table or table_detected) else 0.0
    score += 6.0 if 0.7 <= para_ratio <= 1.3 else 3.0
    score += 4.0 * list_recall

    return StructureResult(
        score, heading_recall, table_detected, para_ratio, list_recall, notes
    )
```

- [ ] **Step 4: Run tests, confirm pass**

```bash
PYTHONPATH=.. pytest tests/test_structure.py -v
```

Expected: 5 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/parse_eval/metrics/structure.py scripts/parse_eval/tests/test_structure.py
git commit -m "feat(parse_eval): add structure scorer (headings/tables/paragraphs/lists)"
```

---

### Task 5: Aggregate scoring + result models

**Files:**
- Create: `scripts/parse_eval/metrics/scoring.py`
- Test: `scripts/parse_eval/tests/test_scoring.py`

- [ ] **Step 1: Write failing tests**

Create `scripts/parse_eval/tests/test_scoring.py`:

```python
from parse_eval.metrics.scoring import aggregate, Grade, ScoreBundle


def test_synthetic_sample_full_score():
    bundle = ScoreBundle(encoding=40, structure=30, completeness=30, encoding_failed=False)
    agg = aggregate(bundle, has_golden=True)
    assert agg.total == 100
    assert agg.grade == Grade.EXCELLENT


def test_encoding_fail_overrides_grade():
    bundle = ScoreBundle(encoding=0, structure=30, completeness=30, encoding_failed=True)
    agg = aggregate(bundle, has_golden=True)
    assert agg.grade == Grade.FAIL


def test_real_sample_skips_structure():
    bundle = ScoreBundle(encoding=40, structure=0, completeness=30, encoding_failed=False)
    agg = aggregate(bundle, has_golden=False)
    assert agg.total == 40 * (0.6 / 0.4) + 30 * (0.4 / 0.3)
    assert agg.grade in (Grade.EXCELLENT, Grade.GOOD)


def test_needs_review_band():
    bundle = ScoreBundle(encoding=25, structure=20, completeness=20, encoding_failed=False)
    agg = aggregate(bundle, has_golden=True)
    assert 60 <= agg.total < 75
    assert agg.grade == Grade.NEEDS_REVIEW
```

- [ ] **Step 2: Run tests, confirm failure**

```bash
PYTHONPATH=.. pytest tests/test_scoring.py -v
```

Expected: `ModuleNotFoundError`.

- [ ] **Step 3: Implement scoring.py**

Create `scripts/parse_eval/metrics/scoring.py`:

```python
from dataclasses import dataclass
from enum import Enum


class Grade(str, Enum):
    EXCELLENT = "Excellent"
    GOOD = "Good"
    NEEDS_REVIEW = "NeedsReview"
    FAIL = "Fail"


@dataclass
class ScoreBundle:
    encoding: float
    structure: float
    completeness: float
    encoding_failed: bool


@dataclass
class Aggregate:
    total: float
    grade: Grade


def aggregate(bundle: ScoreBundle, has_golden: bool) -> Aggregate:
    if has_golden:
        total = bundle.encoding + bundle.structure + bundle.completeness
    else:
        total = (bundle.encoding / 40) * 60 + (bundle.completeness / 30) * 40

    if bundle.encoding_failed:
        return Aggregate(total, Grade.FAIL)

    if total >= 90:
        grade = Grade.EXCELLENT
    elif total >= 75:
        grade = Grade.GOOD
    elif total >= 60:
        grade = Grade.NEEDS_REVIEW
    else:
        grade = Grade.FAIL

    return Aggregate(total, grade)
```

- [ ] **Step 4: Run tests, confirm pass**

```bash
PYTHONPATH=.. pytest tests/test_scoring.py -v
```

Expected: 4 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/parse_eval/metrics/scoring.py scripts/parse_eval/tests/test_scoring.py
git commit -m "feat(parse_eval): add score aggregation + grading"
```

---

### Task 6: Sample manifest + golden file format

**Files:**
- Create: `scripts/parse_eval/samples/synthetic/manifest.yaml`
- Create: `scripts/parse_eval/samples/loader.py`
- Test: `scripts/parse_eval/tests/test_loader.py`

Goldens follow this convention: each sample `foo.pdf` has a sibling `foo.golden.txt` (plain text reference) and a shared `manifest.yaml` entry listing `keywords`, `has_table`, etc.

- [ ] **Step 1: Write manifest.yaml schema (placeholder entry, real files added in Task 11)**

Create `scripts/parse_eval/samples/synthetic/manifest.yaml`:

```yaml
samples:
  - file: cn_en_mixed.pdf
    description: 中英混排 + 数字 + 半角/全角标点
    keywords: [产品, 销售, Product, Revenue, 2026]
    has_table: false
  - file: scanned_image.pdf
    description: 纯扫描件（图片型，无文本层）
    keywords: [合同, 甲方, 乙方]
    has_table: false
  - file: complex_table.docx
    description: 合并单元格、嵌套表格
    keywords: [项目, 金额, 合计]
    has_table: true
  - file: multi_column.pdf
    description: 双栏/三栏排版
    keywords: [摘要, 结论, 参考文献]
    has_table: false
  - file: long_doc_80p.pdf
    description: 长文档，测漏页 / 页眉页脚重复
    keywords: [第一章, 第八十页, 附录]
    has_table: false
  - file: legacy.doc
    description: 老 Word 格式
    keywords: [通知, 全体员工]
    has_table: false
  - file: data.xlsx
    description: 多 sheet + 公式 + 合并单元格
    keywords: [营收, 利润, 季度]
    has_table: true
  - file: slides.pptx
    description: 含备注、SmartArt、中文字体
    keywords: [愿景, 路线图]
    has_table: false
```

- [ ] **Step 2: Write failing tests**

Create `scripts/parse_eval/tests/test_loader.py`:

```python
from pathlib import Path
from parse_eval.samples.loader import load_synthetic_manifest, SampleSpec


def test_load_manifest_returns_specs(tmp_path):
    manifest = tmp_path / "manifest.yaml"
    manifest.write_text(
        "samples:\n"
        "  - file: a.pdf\n"
        "    description: test\n"
        "    keywords: [x, y]\n"
        "    has_table: true\n"
    )
    specs = load_synthetic_manifest(manifest)
    assert len(specs) == 1
    assert specs[0].file == "a.pdf"
    assert specs[0].keywords == ["x", "y"]
    assert specs[0].has_table is True


def test_golden_path_resolution():
    spec = SampleSpec(file="foo.pdf", description="d", keywords=[], has_table=False)
    assert spec.golden_name() == "foo.golden.txt"
```

- [ ] **Step 3: Add PyYAML to requirements**

Append to `scripts/parse_eval/requirements.txt`:

```
pyyaml>=6.0
```

Run: `pip install -r requirements.txt`

- [ ] **Step 4: Implement loader**

Create `scripts/parse_eval/samples/__init__.py` (empty) and `scripts/parse_eval/samples/loader.py`:

```python
from dataclasses import dataclass, field
from pathlib import Path
import yaml


@dataclass
class SampleSpec:
    file: str
    description: str
    keywords: list[str] = field(default_factory=list)
    has_table: bool = False

    def golden_name(self) -> str:
        stem = Path(self.file).stem
        return f"{stem}.golden.txt"


def load_synthetic_manifest(path: Path) -> list[SampleSpec]:
    with open(path, "r", encoding="utf-8") as f:
        data = yaml.safe_load(f)
    return [SampleSpec(**entry) for entry in data.get("samples", [])]
```

- [ ] **Step 5: Run tests, confirm pass**

```bash
PYTHONPATH=.. pytest tests/test_loader.py -v
```

Expected: 2 passed.

- [ ] **Step 6: Commit**

```bash
git add scripts/parse_eval/samples scripts/parse_eval/tests/test_loader.py scripts/parse_eval/requirements.txt
git commit -m "feat(parse_eval): add sample manifest schema + loader"
```

---

### Task 7: Pipeline base interface + SOP client

**Files:**
- Create: `scripts/parse_eval/pipelines/base.py`
- Create: `scripts/parse_eval/pipelines/sop.py`
- Create: `scripts/parse_eval/pipelines/auth.py`

SOP pipeline: POST `POST {LOCAL_API_URL}/v1/pdf/convert-to-text` with `run_id`, `node_id`, and the file as multipart. Response body is the unified format `{code, message, data: {content: string}}`.

> **Engineer note:** verify the exact endpoint path + response shape by grepping `numind-server/internal/numind/controller/v1/pdf/pdf.go::ConvertToText` and `numind-server/internal/numind/router.go` before writing the client. If the real endpoint differs from the assumed path, update `SOP_ENDPOINT` in the client to match.

- [ ] **Step 1: Implement auth helper**

Create `scripts/parse_eval/pipelines/auth.py`:

```python
import os
import requests


def login(base_url: str) -> str:
    """Call /v1/web/login with E2E_USERNAME/E2E_PASSWORD, return user_token."""
    username = os.environ["E2E_USERNAME"]
    password = os.environ["E2E_PASSWORD"]
    resp = requests.post(
        f"{base_url}/v1/web/login",
        json={"username": username, "password": password},
        timeout=10,
    )
    resp.raise_for_status()
    body = resp.json()
    if body.get("code") != 0:
        raise RuntimeError(f"login failed: {body}")
    return body["data"]["token"]
```

- [ ] **Step 2: Implement base interface**

Create `scripts/parse_eval/pipelines/base.py`:

```python
from abc import ABC, abstractmethod
from dataclasses import dataclass
from pathlib import Path


@dataclass
class ParseResult:
    pipeline: str
    sample_path: Path
    output_text: str
    error: str | None = None
    elapsed_seconds: float = 0.0


class Pipeline(ABC):
    name: str

    @abstractmethod
    def parse(self, sample_path: Path) -> ParseResult: ...
```

- [ ] **Step 3: Implement SOP client**

Create `scripts/parse_eval/pipelines/sop.py`:

```python
import time
import uuid
from pathlib import Path

import requests

from .base import Pipeline, ParseResult

SOP_ENDPOINT = "/v1/pdf/convert-to-text"


class SopPipeline(Pipeline):
    name = "sop"

    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.token = token

    def parse(self, sample_path: Path) -> ParseResult:
        started = time.time()
        run_id = f"eval-{uuid.uuid4().hex[:8]}"
        node_id = "eval-node"
        try:
            with open(sample_path, "rb") as f:
                resp = requests.post(
                    f"{self.base_url}{SOP_ENDPOINT}",
                    headers={"Authorization": f"Bearer {self.token}"},
                    data={"run_id": run_id, "node_id": node_id},
                    files={"file": (sample_path.name, f)},
                    timeout=120,
                )
            resp.raise_for_status()
            body = resp.json()
            if body.get("code") != 0:
                return ParseResult(self.name, sample_path, "", f"api_error: {body}", time.time() - started)
            content = body.get("data", {}).get("content", "")
            return ParseResult(self.name, sample_path, content, None, time.time() - started)
        except Exception as e:
            return ParseResult(self.name, sample_path, "", f"exception: {e}", time.time() - started)
```

- [ ] **Step 4: Manual smoke test**

```bash
cd scripts/parse_eval && source .venv/bin/activate
export LOCAL_API_URL=http://localhost:9091  # adjust to your local port
export E2E_USERNAME=$E2E_USERNAME
export E2E_PASSWORD=$E2E_PASSWORD
PYTHONPATH=.. python -c "
from parse_eval.pipelines.auth import login
from parse_eval.pipelines.sop import SopPipeline
from pathlib import Path
import os
token = login(os.environ['LOCAL_API_URL'])
print('token ok, len=', len(token))
"
```

Expected: `token ok, len= <some number>`. If fails, debug endpoint/credentials before proceeding.

- [ ] **Step 5: Commit**

```bash
git add scripts/parse_eval/pipelines
git commit -m "feat(parse_eval): add pipeline base + SOP client"
```

---

### Task 8: Chatbot KB pipeline client

**Files:**
- Create: `scripts/parse_eval/pipelines/chatbot.py`

Chatbot KB uploads go to the knowledge base endpoint; after upload we need to fetch the stored document content. The simplest approach: create a temp knowledge base per run, upload, read back the `KnowledgeDocument.content`, then delete the KB.

> **Engineer note:** grep `numind-server/internal/numind/controller/v1/config/knowledge_base.go` for the exact endpoints. Expected pattern: `POST /v1/knowledge-bases`, `POST /v1/knowledge-bases/:id/documents` (multipart), `GET /v1/knowledge-bases/:id/documents/:docId` (returns content), `DELETE /v1/knowledge-bases/:id`. Update the URLs below if they differ.

- [ ] **Step 1: Implement chatbot client**

Create `scripts/parse_eval/pipelines/chatbot.py`:

```python
import time
import uuid
from pathlib import Path

import requests

from .base import Pipeline, ParseResult


class ChatbotPipeline(Pipeline):
    name = "chatbot"

    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.headers = {"Authorization": f"Bearer {token}"}

    def _create_kb(self) -> int:
        resp = requests.post(
            f"{self.base_url}/v1/knowledge-bases",
            headers=self.headers,
            json={"name": f"eval-{uuid.uuid4().hex[:8]}", "description": "parse_eval temp"},
            timeout=10,
        )
        resp.raise_for_status()
        return resp.json()["data"]["id"]

    def _delete_kb(self, kb_id: int) -> None:
        try:
            requests.delete(
                f"{self.base_url}/v1/knowledge-bases/{kb_id}",
                headers=self.headers,
                timeout=10,
            )
        except Exception:
            pass

    def parse(self, sample_path: Path) -> ParseResult:
        started = time.time()
        kb_id = None
        try:
            kb_id = self._create_kb()
            with open(sample_path, "rb") as f:
                up = requests.post(
                    f"{self.base_url}/v1/knowledge-bases/{kb_id}/documents",
                    headers=self.headers,
                    files={"file": (sample_path.name, f)},
                    timeout=120,
                )
            up.raise_for_status()
            body = up.json()
            if body.get("code") != 0:
                return ParseResult(self.name, sample_path, "", f"api_error: {body}", time.time() - started)
            doc_id = body["data"]["id"]

            det = requests.get(
                f"{self.base_url}/v1/knowledge-bases/{kb_id}/documents/{doc_id}",
                headers=self.headers,
                timeout=10,
            )
            det.raise_for_status()
            content = det.json().get("data", {}).get("content", "")
            return ParseResult(self.name, sample_path, content, None, time.time() - started)
        except Exception as e:
            return ParseResult(self.name, sample_path, "", f"exception: {e}", time.time() - started)
        finally:
            if kb_id is not None:
                self._delete_kb(kb_id)
```

- [ ] **Step 2: Smoke test**

```bash
PYTHONPATH=.. python -c "
from parse_eval.pipelines.auth import login
from parse_eval.pipelines.chatbot import ChatbotPipeline
from pathlib import Path
import os
token = login(os.environ['LOCAL_API_URL'])
c = ChatbotPipeline(os.environ['LOCAL_API_URL'], token)
# Use any existing PDF in the repo for smoke test
r = c.parse(Path('../../README.md'))
print('ok' if r.error is None else r.error, len(r.output_text))
"
```

Expected: `ok <N>` where N>0. If the endpoint path is wrong, fix and retry before committing.

- [ ] **Step 3: Commit**

```bash
git add scripts/parse_eval/pipelines/chatbot.py
git commit -m "feat(parse_eval): add Chatbot KB pipeline client"
```

---

### Task 9: SalesRAG pipeline client (async polling)

**Files:**
- Create: `scripts/parse_eval/pipelines/salesrag.py`

SalesRAG ingests asynchronously: upload → get `docID` → poll status until `COMPLETED` → read assembled content from chunks.

> **Engineer note:** grep `numind-server/internal/numind/controller/v1/salesrag/sales_rag.go` for the ingest + status + chunk endpoints. Expected: `POST /v1/salesrag/ingest` (returns `{doc_id}`), `GET /v1/salesrag/documents/:doc_id` (returns `{status, ...}`), `GET /v1/salesrag/documents/:doc_id/chunks` (returns list of chunks with `content`). Update URLs/field names below to match.

- [ ] **Step 1: Implement salesrag client**

Create `scripts/parse_eval/pipelines/salesrag.py`:

```python
import time
from pathlib import Path

import requests

from .base import Pipeline, ParseResult

POLL_INTERVAL = 3
POLL_TIMEOUT = 300  # 5 minutes


class SalesRagPipeline(Pipeline):
    name = "salesrag"

    def __init__(self, base_url: str, token: str):
        self.base_url = base_url.rstrip("/")
        self.headers = {"Authorization": f"Bearer {token}"}

    def parse(self, sample_path: Path) -> ParseResult:
        started = time.time()
        try:
            with open(sample_path, "rb") as f:
                up = requests.post(
                    f"{self.base_url}/v1/salesrag/ingest",
                    headers=self.headers,
                    files={"file": (sample_path.name, f)},
                    timeout=120,
                )
            up.raise_for_status()
            body = up.json()
            if body.get("code") != 0:
                return ParseResult(self.name, sample_path, "", f"api_error: {body}", time.time() - started)
            doc_id = body["data"]["doc_id"]

            deadline = time.time() + POLL_TIMEOUT
            status = "PENDING"
            while time.time() < deadline:
                st = requests.get(
                    f"{self.base_url}/v1/salesrag/documents/{doc_id}",
                    headers=self.headers,
                    timeout=10,
                )
                st.raise_for_status()
                status = st.json().get("data", {}).get("status", "UNKNOWN")
                if status == "COMPLETED":
                    break
                if status == "FAILED":
                    return ParseResult(self.name, sample_path, "", "pipeline_failed", time.time() - started)
                time.sleep(POLL_INTERVAL)
            if status != "COMPLETED":
                return ParseResult(self.name, sample_path, "", f"poll_timeout status={status}", time.time() - started)

            ch = requests.get(
                f"{self.base_url}/v1/salesrag/documents/{doc_id}/chunks",
                headers=self.headers,
                timeout=30,
            )
            ch.raise_for_status()
            chunks = ch.json().get("data", {}).get("list", [])
            content = "\n\n".join(c.get("content", "") for c in chunks)
            return ParseResult(self.name, sample_path, content, None, time.time() - started)
        except Exception as e:
            return ParseResult(self.name, sample_path, "", f"exception: {e}", time.time() - started)
```

- [ ] **Step 2: Smoke test**

```bash
PYTHONPATH=.. python -c "
from parse_eval.pipelines.auth import login
from parse_eval.pipelines.salesrag import SalesRagPipeline
from pathlib import Path
import os
token = login(os.environ['LOCAL_API_URL'])
s = SalesRagPipeline(os.environ['LOCAL_API_URL'], token)
r = s.parse(Path('../../README.md'))
print('ok' if r.error is None else r.error, len(r.output_text), 'elapsed=', r.elapsed_seconds)
"
```

Expected: `ok <N> elapsed= <seconds>`.

- [ ] **Step 3: Commit**

```bash
git add scripts/parse_eval/pipelines/salesrag.py
git commit -m "feat(parse_eval): add SalesRAG pipeline client with async polling"
```

---

### Task 10: Evaluator (ties everything together)

**Files:**
- Create: `scripts/parse_eval/evaluator.py`
- Test: `scripts/parse_eval/tests/test_evaluator.py`

- [ ] **Step 1: Write failing tests**

Create `scripts/parse_eval/tests/test_evaluator.py`:

```python
from pathlib import Path
from parse_eval.evaluator import evaluate_parse_result, EvalRow
from parse_eval.pipelines.base import ParseResult
from parse_eval.samples.loader import SampleSpec


def test_synthetic_evaluation_produces_row(tmp_path):
    sample = tmp_path / "a.pdf"
    sample.write_bytes(b"dummy")
    golden = tmp_path / "a.golden.txt"
    golden.write_text("你好 世界 测试 内容")
    spec = SampleSpec(file="a.pdf", description="t", keywords=["你好", "测试"], has_table=False)
    pr = ParseResult("sop", sample, "你好 世界 测试 内容", None, 1.0)

    row = evaluate_parse_result(pr, spec, golden_dir=tmp_path)
    assert row.pipeline == "sop"
    assert row.sample == "a.pdf"
    assert row.total_score > 90
    assert row.error is None


def test_parse_error_produces_failed_row(tmp_path):
    sample = tmp_path / "a.pdf"
    spec = SampleSpec(file="a.pdf", description="t", keywords=[], has_table=False)
    pr = ParseResult("sop", sample, "", "api_error: 500", 0.5)

    row = evaluate_parse_result(pr, spec, golden_dir=tmp_path)
    assert row.error == "api_error: 500"
    assert row.total_score == 0
```

- [ ] **Step 2: Run tests, confirm failure**

```bash
PYTHONPATH=.. pytest tests/test_evaluator.py -v
```

Expected: `ModuleNotFoundError`.

- [ ] **Step 3: Implement evaluator.py**

Create `scripts/parse_eval/evaluator.py`:

```python
from dataclasses import dataclass, asdict
from pathlib import Path

from parse_eval.metrics.encoding import score_encoding
from parse_eval.metrics.structure import score_structure, StructureResult
from parse_eval.metrics.completeness import score_completeness, CompletenessResult
from parse_eval.metrics.scoring import aggregate, ScoreBundle, Grade
from parse_eval.pipelines.base import ParseResult
from parse_eval.samples.loader import SampleSpec


@dataclass
class EvalRow:
    pipeline: str
    sample: str
    total_score: float
    grade: str
    encoding_score: float
    structure_score: float
    completeness_score: float
    encoding_failed: bool
    char_count_ratio: float
    keyword_recall: float
    heading_recall: float
    table_detected: bool
    notes: list[str]
    elapsed_seconds: float
    error: str | None


def evaluate_parse_result(pr: ParseResult, spec: SampleSpec, golden_dir: Path) -> EvalRow:
    if pr.error:
        return EvalRow(
            pipeline=pr.pipeline, sample=spec.file, total_score=0.0, grade=Grade.FAIL.value,
            encoding_score=0.0, structure_score=0.0, completeness_score=0.0,
            encoding_failed=True, char_count_ratio=0.0, keyword_recall=0.0,
            heading_recall=0.0, table_detected=False, notes=[pr.error],
            elapsed_seconds=pr.elapsed_seconds, error=pr.error,
        )

    enc = score_encoding(pr.output_text)

    golden_path = golden_dir / spec.golden_name()
    has_golden = golden_path.exists()

    if has_golden:
        golden = golden_path.read_text(encoding="utf-8")
        comp = score_completeness(pr.output_text, golden, spec.keywords)
        struct = score_structure(pr.output_text, golden, spec.has_table)
    else:
        comp = CompletenessResult(0.0, 0.0, 0.0, [])
        struct = StructureResult(0.0, 0.0, False, 0.0, 0.0, [])

    bundle = ScoreBundle(
        encoding=enc.score, structure=struct.score, completeness=comp.score,
        encoding_failed=enc.failed,
    )
    agg = aggregate(bundle, has_golden)

    return EvalRow(
        pipeline=pr.pipeline, sample=spec.file, total_score=agg.total, grade=agg.grade.value,
        encoding_score=enc.score, structure_score=struct.score, completeness_score=comp.score,
        encoding_failed=enc.failed, char_count_ratio=comp.char_count_ratio,
        keyword_recall=comp.keyword_recall, heading_recall=struct.heading_recall,
        table_detected=struct.table_detected,
        notes=enc.notes + struct.notes + comp.notes,
        elapsed_seconds=pr.elapsed_seconds, error=None,
    )


def row_to_dict(row: EvalRow) -> dict:
    return asdict(row)
```

- [ ] **Step 4: Run tests, confirm pass**

```bash
PYTHONPATH=.. pytest tests/test_evaluator.py -v
```

Expected: 2 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/parse_eval/evaluator.py scripts/parse_eval/tests/test_evaluator.py
git commit -m "feat(parse_eval): add evaluator that joins parse results with scoring"
```

---

### Task 11: Build synthetic samples + goldens

**Files:**
- Create: `scripts/parse_eval/samples/synthetic/cn_en_mixed.pdf` + `.golden.txt`
- Create: `scripts/parse_eval/samples/synthetic/scanned_image.pdf` + `.golden.txt`
- Create: `scripts/parse_eval/samples/synthetic/complex_table.docx` + `.golden.txt`
- Create: `scripts/parse_eval/samples/synthetic/multi_column.pdf` + `.golden.txt`
- Create: `scripts/parse_eval/samples/synthetic/long_doc_80p.pdf` + `.golden.txt`
- Create: `scripts/parse_eval/samples/synthetic/legacy.doc` + `.golden.txt`
- Create: `scripts/parse_eval/samples/synthetic/data.xlsx` + `.golden.txt`
- Create: `scripts/parse_eval/samples/synthetic/slides.pptx` + `.golden.txt`

This task is hand work — generate or source each file so it exhibits the scenario listed in the manifest. Use Word/Excel/PPT/Keynote/LibreOffice locally; for the scanned PDF, print a page to PDF then convert to image-only with `magick` or similar. Long-doc PDF can be generated via LaTeX or by concatenating repeated content.

Each `.golden.txt` is a hand-written ideal text output — the expected content the parser SHOULD return. It does not need perfect whitespace match; scoring is tolerant.

- [ ] **Step 1: Generate / source each of the 8 sample files**

Place each file at `scripts/parse_eval/samples/synthetic/<name>` per manifest.

For the scanned PDF: take any text page, print to PDF, then run:

```bash
magick -density 200 input.pdf scanned_image.pdf
```

(or use Preview on macOS: Export as PDF > Quartz Filter: "Reduce File Size" then re-open and re-export as image-based PDF.)

For the 80-page long doc: create a `.tex` or Word file with repeated content across 80 pages, including unique page-1 / page-40 / page-80 markers matching the manifest keywords.

- [ ] **Step 2: Hand-write each golden.txt**

For each sample, write the ideal text output to `<stem>.golden.txt`. Include:
- All visible text (titles, paragraphs, table contents as tab-separated or Markdown)
- All keywords from the manifest entry (critical — keyword_recall uses these)
- Preserve headings using `# ` / `## ` markdown syntax
- Preserve lists using `- ` or `1. ` syntax

For `complex_table.docx` and `data.xlsx`, write tables in Markdown pipe format `| a | b |\n|---|---|`.

- [ ] **Step 3: Verify manifest entries match real files**

```bash
cd scripts/parse_eval/samples/synthetic
for f in $(python -c "
import yaml
data = yaml.safe_load(open('manifest.yaml'))
for s in data['samples']:
    print(s['file'])
"); do
  test -f "$f" && echo "OK $f" || echo "MISSING $f"
  stem="${f%.*}"
  test -f "${stem}.golden.txt" && echo "OK ${stem}.golden.txt" || echo "MISSING ${stem}.golden.txt"
done
```

Expected: every line starts with `OK`.

- [ ] **Step 4: Commit**

```bash
cd numind-server
git add scripts/parse_eval/samples/synthetic/
git commit -m "feat(parse_eval): add 8 synthetic test samples with golden references"
```

---

### Task 12: Runner orchestration (pipelines × samples)

**Files:**
- Create: `scripts/parse_eval/runner.py`

- [ ] **Step 1: Implement runner**

Create `scripts/parse_eval/runner.py`:

```python
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
import json

from parse_eval.evaluator import evaluate_parse_result, EvalRow, row_to_dict
from parse_eval.pipelines.base import Pipeline
from parse_eval.samples.loader import SampleSpec


@dataclass
class RunContext:
    run_id: str
    report_dir: Path
    synthetic_dir: Path
    real_dir: Path


def make_run_context(base_dir: Path) -> RunContext:
    run_id = datetime.now().strftime("%Y%m%d_%H%M%S")
    report_dir = base_dir / "reports" / run_id
    (report_dir / "diffs").mkdir(parents=True, exist_ok=True)
    return RunContext(
        run_id=run_id,
        report_dir=report_dir,
        synthetic_dir=base_dir / "samples" / "synthetic",
        real_dir=base_dir / "samples" / "real",
    )


def run_matrix(
    pipelines: list[Pipeline],
    synthetic_specs: list[SampleSpec],
    real_files: list[Path],
    ctx: RunContext,
) -> list[EvalRow]:
    rows: list[EvalRow] = []

    for spec in synthetic_specs:
        sample_path = ctx.synthetic_dir / spec.file
        if not sample_path.exists():
            print(f"[skip] missing sample: {sample_path}")
            continue
        for pipeline in pipelines:
            print(f"[run] {pipeline.name} × {spec.file}")
            pr = pipeline.parse(sample_path)
            row = evaluate_parse_result(pr, spec, ctx.synthetic_dir)
            rows.append(row)
            _write_output(ctx, pipeline.name, spec.file, pr.output_text)

    for real_path in real_files:
        spec = SampleSpec(file=real_path.name, description="real sample", keywords=[], has_table=False)
        for pipeline in pipelines:
            print(f"[run] {pipeline.name} × {real_path.name}")
            pr = pipeline.parse(real_path)
            row = evaluate_parse_result(pr, spec, ctx.real_dir)
            rows.append(row)
            _write_output(ctx, pipeline.name, real_path.name, pr.output_text)

    summary = [row_to_dict(r) for r in rows]
    (ctx.report_dir / "summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    return rows


def _write_output(ctx: RunContext, pipeline: str, sample: str, text: str) -> None:
    outdir = ctx.report_dir / "diffs"
    outdir.mkdir(parents=True, exist_ok=True)
    (outdir / f"{pipeline}__{sample}.out.txt").write_text(text, encoding="utf-8")
```

- [ ] **Step 2: Quick import sanity check**

```bash
PYTHONPATH=.. python -c "from parse_eval.runner import make_run_context; print('ok')"
```

Expected: `ok`.

- [ ] **Step 3: Commit**

```bash
git add scripts/parse_eval/runner.py
git commit -m "feat(parse_eval): add runner for pipelines × samples matrix"
```

---

### Task 13: HTML report renderer

**Files:**
- Create: `scripts/parse_eval/report/html_renderer.py`
- Create: `scripts/parse_eval/report/templates/report.html.j2`

- [ ] **Step 1: Create Jinja template**

Create `scripts/parse_eval/report/templates/report.html.j2`:

```html
<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>Parse Eval Report — {{ run_id }}</title>
<style>
body { font-family: -apple-system, sans-serif; margin: 24px; color: #1a1a1a; }
h1 { font-size: 22px; }
table { border-collapse: collapse; margin: 16px 0; }
th, td { padding: 8px 12px; border: 1px solid #ddd; font-size: 13px; text-align: center; }
th { background: #f5f5f5; }
td.excellent { background: #d4edda; }
td.good { background: #fff3cd; }
td.needsreview { background: #fbdcb0; }
td.fail { background: #f8d7da; color: #721c24; font-weight: bold; }
.sample { margin: 24px 0; padding: 12px; border: 1px solid #e0e0e0; border-radius: 6px; }
.sample h3 { margin: 0 0 8px 0; }
.cols { display: grid; grid-template-columns: repeat(3, 1fr); gap: 12px; }
.col { background: #fafafa; padding: 8px; font-size: 12px; font-family: ui-monospace, monospace; white-space: pre-wrap; max-height: 400px; overflow: auto; }
.notes { color: #856404; font-size: 12px; }
</style>
</head>
<body>
<h1>Document Parsing Evaluation — {{ run_id }}</h1>
<p>Pipelines: {{ pipelines | join(', ') }} | Samples: {{ samples | length }}</p>

<h2>Score Matrix</h2>
<table>
<thead>
<tr><th>Sample</th>{% for p in pipelines %}<th>{{ p }}</th>{% endfor %}</tr>
</thead>
<tbody>
{% for sample in samples %}
<tr>
<td style="text-align:left">{{ sample }}</td>
{% for p in pipelines %}
{% set row = matrix[sample][p] %}
<td class="{{ row.grade | lower }}">{{ '%.1f' | format(row.total_score) }}<br><small>{{ row.grade }}</small></td>
{% endfor %}
</tr>
{% endfor %}
</tbody>
</table>

<h2>Per-Sample Detail</h2>
{% for sample in samples %}
<div class="sample">
<h3>{{ sample }}</h3>
<div class="cols">
{% for p in pipelines %}
{% set row = matrix[sample][p] %}
<div>
<strong>{{ p }}</strong> — {{ '%.1f' | format(row.total_score) }} ({{ row.grade }})<br>
<small>enc {{ '%.1f' | format(row.encoding_score) }} | struct {{ '%.1f' | format(row.structure_score) }} | comp {{ '%.1f' | format(row.completeness_score) }} | {{ '%.1fs' | format(row.elapsed_seconds) }}</small>
{% if row.notes %}<div class="notes">{{ row.notes | join('; ') }}</div>{% endif %}
<div class="col">{{ outputs[sample][p] | truncate(3000) }}</div>
</div>
{% endfor %}
</div>
</div>
{% endfor %}

</body>
</html>
```

- [ ] **Step 2: Implement renderer**

Create `scripts/parse_eval/report/html_renderer.py`:

```python
from pathlib import Path
from jinja2 import Environment, FileSystemLoader, select_autoescape

from parse_eval.evaluator import EvalRow


def render_html(run_id: str, rows: list[EvalRow], outputs: dict, report_dir: Path) -> None:
    tpl_dir = Path(__file__).parent / "templates"
    env = Environment(loader=FileSystemLoader(tpl_dir), autoescape=select_autoescape())
    tpl = env.get_template("report.html.j2")

    pipelines = sorted({r.pipeline for r in rows})
    samples = sorted({r.sample for r in rows})
    matrix: dict = {s: {} for s in samples}
    for r in rows:
        matrix[r.sample][r.pipeline] = r

    html = tpl.render(
        run_id=run_id, pipelines=pipelines, samples=samples,
        matrix=matrix, outputs=outputs,
    )
    (report_dir / "report.html").write_text(html, encoding="utf-8")


def load_outputs(report_dir: Path) -> dict:
    """Load parsed outputs from diffs/ directory produced by runner."""
    outputs: dict = {}
    diffs_dir = report_dir / "diffs"
    if not diffs_dir.exists():
        return outputs
    for f in diffs_dir.glob("*.out.txt"):
        pipeline, rest = f.name.split("__", 1)
        sample = rest[:-len(".out.txt")]
        outputs.setdefault(sample, {})[pipeline] = f.read_text(encoding="utf-8")
    return outputs
```

- [ ] **Step 3: Import check**

```bash
PYTHONPATH=.. python -c "from parse_eval.report.html_renderer import render_html; print('ok')"
```

Expected: `ok`.

- [ ] **Step 4: Commit**

```bash
git add scripts/parse_eval/report
git commit -m "feat(parse_eval): add HTML report renderer with Jinja template"
```

---

### Task 14: Comparator (--compare mode)

**Files:**
- Create: `scripts/parse_eval/report/comparator.py`
- Test: `scripts/parse_eval/tests/test_comparator.py`

- [ ] **Step 1: Write failing tests**

Create `scripts/parse_eval/tests/test_comparator.py`:

```python
from parse_eval.report.comparator import compare_summaries, Regression


def test_detects_score_drop_over_10():
    baseline = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 90.0}]
    latest = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 70.0}]
    regs = compare_summaries(baseline, latest)
    assert len(regs) == 1
    assert regs[0].delta == -20.0
    assert regs[0].is_regression is True


def test_small_drop_not_regression():
    baseline = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 90.0}]
    latest = [{"pipeline": "sop", "sample": "a.pdf", "total_score": 85.0}]
    regs = compare_summaries(baseline, latest)
    assert regs[0].is_regression is False


def test_new_sample_no_baseline():
    baseline = []
    latest = [{"pipeline": "sop", "sample": "new.pdf", "total_score": 80.0}]
    regs = compare_summaries(baseline, latest)
    assert regs == []
```

- [ ] **Step 2: Run tests, confirm failure**

```bash
PYTHONPATH=.. pytest tests/test_comparator.py -v
```

Expected: `ModuleNotFoundError`.

- [ ] **Step 3: Implement comparator**

Create `scripts/parse_eval/report/comparator.py`:

```python
from dataclasses import dataclass

REGRESSION_THRESHOLD = 10.0


@dataclass
class Regression:
    pipeline: str
    sample: str
    baseline_score: float
    latest_score: float
    delta: float
    is_regression: bool


def compare_summaries(baseline: list[dict], latest: list[dict]) -> list[Regression]:
    base_map = {(r["pipeline"], r["sample"]): r["total_score"] for r in baseline}
    out: list[Regression] = []
    for r in latest:
        key = (r["pipeline"], r["sample"])
        if key not in base_map:
            continue
        b = base_map[key]
        l = r["total_score"]
        delta = l - b
        out.append(Regression(
            pipeline=r["pipeline"], sample=r["sample"],
            baseline_score=b, latest_score=l, delta=delta,
            is_regression=(delta <= -REGRESSION_THRESHOLD),
        ))
    return out
```

- [ ] **Step 4: Run tests, confirm pass**

```bash
PYTHONPATH=.. pytest tests/test_comparator.py -v
```

Expected: 3 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/parse_eval/report/comparator.py scripts/parse_eval/tests/test_comparator.py
git commit -m "feat(parse_eval): add comparator for regression detection"
```

---

### Task 15: CLI entry point (run_eval.py)

**Files:**
- Create: `scripts/parse_eval/run_eval.py`

- [ ] **Step 1: Implement CLI**

Create `scripts/parse_eval/run_eval.py`:

```python
import argparse
import json
import os
import sys
from pathlib import Path

from parse_eval.pipelines.auth import login
from parse_eval.pipelines.sop import SopPipeline
from parse_eval.pipelines.chatbot import ChatbotPipeline
from parse_eval.pipelines.salesrag import SalesRagPipeline
from parse_eval.samples.loader import load_synthetic_manifest
from parse_eval.runner import make_run_context, run_matrix
from parse_eval.report.html_renderer import render_html, load_outputs
from parse_eval.report.comparator import compare_summaries

BASE_DIR = Path(__file__).parent


def _build_pipelines(names: list[str]):
    base_url = os.environ.get("LOCAL_API_URL", "http://localhost:9091")
    token = login(base_url)
    registry = {
        "sop": lambda: SopPipeline(base_url, token),
        "chatbot": lambda: ChatbotPipeline(base_url, token),
        "salesrag": lambda: SalesRagPipeline(base_url, token),
    }
    return [registry[n]() for n in names]


def _cmd_run(args: argparse.Namespace) -> int:
    if args.pipeline == "all":
        names = ["sop", "chatbot", "salesrag"]
    else:
        names = [args.pipeline]
    pipelines = _build_pipelines(names)

    synthetic_specs = load_synthetic_manifest(BASE_DIR / "samples" / "synthetic" / "manifest.yaml")

    if args.sample == "synthetic":
        real_files = []
    elif args.sample == "real":
        synthetic_specs = []
        real_files = sorted((BASE_DIR / "samples" / "real").glob("*")) if (BASE_DIR / "samples" / "real").exists() else []
        real_files = [f for f in real_files if f.name != ".gitkeep"]
    elif args.sample == "all":
        real_files = sorted((BASE_DIR / "samples" / "real").glob("*")) if (BASE_DIR / "samples" / "real").exists() else []
        real_files = [f for f in real_files if f.name != ".gitkeep"]
    else:
        single = Path(args.sample)
        if single.exists():
            synthetic_specs = []
            real_files = [single]
        else:
            print(f"sample not found: {single}", file=sys.stderr)
            return 1

    ctx = make_run_context(BASE_DIR)
    rows = run_matrix(pipelines, synthetic_specs, real_files, ctx)
    outputs = load_outputs(ctx.report_dir)
    render_html(ctx.run_id, rows, outputs, ctx.report_dir)
    print(f"\n✓ report written to {ctx.report_dir}/report.html")
    return 0


def _cmd_compare(args: argparse.Namespace) -> int:
    base = json.loads((BASE_DIR / "reports" / args.baseline / "summary.json").read_text(encoding="utf-8"))
    latest = json.loads((BASE_DIR / "reports" / args.latest / "summary.json").read_text(encoding="utf-8"))
    regs = compare_summaries(base, latest)
    any_reg = False
    for r in regs:
        flag = "🔴 REGRESSION" if r.is_regression else "·"
        print(f"{flag} {r.pipeline} × {r.sample}: {r.baseline_score:.1f} → {r.latest_score:.1f} ({r.delta:+.1f})")
        any_reg = any_reg or r.is_regression
    return 2 if any_reg else 0


def main() -> int:
    p = argparse.ArgumentParser(prog="run_eval")
    sub = p.add_subparsers(dest="cmd", required=False)

    # default (run) args on top-level for convenience
    p.add_argument("--pipeline", default="all", choices=["all", "sop", "chatbot", "salesrag"])
    p.add_argument("--sample", default="all", help="'all' | 'synthetic' | 'real' | path/to/file")
    p.add_argument("--compare", nargs=2, metavar=("BASELINE", "LATEST"))

    args = p.parse_args()

    if args.compare:
        ns = argparse.Namespace(baseline=args.compare[0], latest=args.compare[1])
        return _cmd_compare(ns)

    return _cmd_run(args)


if __name__ == "__main__":
    sys.exit(main())
```

- [ ] **Step 2: Full test suite runs clean**

```bash
cd scripts/parse_eval && source .venv/bin/activate
PYTHONPATH=.. pytest -v
```

Expected: all tests pass (encoding 5 + completeness 3 + structure 5 + scoring 4 + loader 2 + evaluator 2 + comparator 3 = 24 passed).

- [ ] **Step 3: CLI smoke test (help)**

```bash
PYTHONPATH=.. python run_eval.py --help
```

Expected: argparse help text printed, no import errors.

- [ ] **Step 4: Commit**

```bash
git add scripts/parse_eval/run_eval.py
git commit -m "feat(parse_eval): add CLI entry point with run + compare modes"
```

---

### Task 16: README + baseline run

**Files:**
- Create: `scripts/parse_eval/README.md`
- Create (generated): `scripts/parse_eval/reports/baseline_<timestamp>/*`

- [ ] **Step 1: Write README**

Create `scripts/parse_eval/README.md`:

```markdown
# parse_eval — 文档解析质量评测工具

对 numind-server 三条文档解析链路（SOP / Chatbot KB / SalesRAG）进行乱码 / 结构 / 完整性三维度评估。

详细设计见 `numind-server/docs/superpowers/specs/2026-04-15-document-parsing-evaluation-design.md`。

## 前置条件

1. 本地后端已启动：`cd numind-server && task dev`
2. 环境变量已设置（读 `.claude/settings.local.json`）：
   - `LOCAL_API_URL` (默认 `http://localhost:9091`)
   - `E2E_USERNAME` / `E2E_PASSWORD`
3. Python 3.10+

## 安装

```bash
cd numind-server/scripts/parse_eval
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

## 使用

```bash
# 跑全部链路 × 全部样本
PYTHONPATH=.. python run_eval.py

# 只跑 SOP 链路 × 合成样本
PYTHONPATH=.. python run_eval.py --pipeline sop --sample synthetic

# 单文件评测
PYTHONPATH=.. python run_eval.py --pipeline salesrag --sample samples/real/xxx.pdf

# 对比两次运行（退化检测，分差 >10 标红）
PYTHONPATH=.. python run_eval.py --compare 20260415_143000 20260415_170000
```

## 输出

`reports/YYYYMMDD_HHMMSS/`：
- `report.html` — 总览矩阵 + 每样本详情（三链路 side-by-side）
- `summary.json` — 机器可读结果
- `diffs/<pipeline>__<sample>.out.txt` — 各链路原始输出

## 真实样本

放在 `samples/real/`（已 gitignore）。手工脱敏后使用（打码手机号、姓名、公司名）。

## 测试

```bash
PYTHONPATH=.. pytest -v
```
```

- [ ] **Step 2: Run baseline evaluation**

```bash
# Ensure backend is up
cd numind-server && task dev &
# Wait a few seconds for it to be ready, then:
cd scripts/parse_eval && source .venv/bin/activate
PYTHONPATH=.. python run_eval.py --pipeline all --sample synthetic
```

Expected: report generated at `reports/<timestamp>/report.html`. Open in browser, visually confirm matrix renders, scores look plausible.

If some samples fail with 0 scores, investigate:
- Auth/endpoint issues → check pipeline smoke tests from Tasks 7-9
- Sample file issues → re-check manifest vs actual files

- [ ] **Step 3: Tag baseline directory**

```bash
cd scripts/parse_eval
BASELINE=$(ls -1t reports | head -1)
cp -r "reports/$BASELINE" "reports/baseline_$BASELINE"
echo "Baseline saved to reports/baseline_$BASELINE"
```

This baseline copy is NOT committed (reports/ is gitignored) but serves as the reference for future `--compare` runs.

- [ ] **Step 4: Commit README**

```bash
cd numind-server
git add scripts/parse_eval/README.md
git commit -m "docs(parse_eval): add README with usage and baseline workflow"
```

- [ ] **Step 5: Merge to develop and push**

```bash
cd numind-server
git push origin develop
```

---

## Verification Summary

After all tasks complete, verify end-to-end:

```bash
cd numind-server/scripts/parse_eval && source .venv/bin/activate
PYTHONPATH=.. pytest -v                          # all unit tests pass
PYTHONPATH=.. python run_eval.py --help          # CLI help works
ls samples/synthetic/*.pdf samples/synthetic/*.docx  # samples present
# With backend running:
PYTHONPATH=.. python run_eval.py --pipeline sop --sample synthetic  # real run, SOP only
open reports/$(ls -1t reports | head -1)/report.html  # inspect HTML
```

All four checks should pass cleanly. Any pipeline that fails at baseline is an **actionable finding** from this tool — not a tool bug — and should be recorded as a separate follow-up task.
