#!/usr/bin/env python3
"""ndf-migrate-manifest: 把 build-manifest.yaml 拆分到 .ndf/ 结构

迁移规则：
1. completed > 7 天的 feature → 归档到 .ndf/archived/{yyyy-mm}-completed.yaml
2. cancelled feature → 归档到 .ndf/archived/cancelled.yaml
3. 其他保留在新 .ndf/manifest.yaml
4. decisions[] 中超过 200 字符的条目 → 拆到 .ndf/decisions/{feature-id}/000N-{slug}.md
5. 短决策（≤200 字符）保留在 manifest 里
6. 备份原 manifest 为 build-manifest.yaml.legacy.{date}
7. 初始化空 .ndf/state.json

用法:
  python3 ndf-migrate-manifest.py --dry-run     # 看预期产出
  python3 ndf-migrate-manifest.py --apply       # 实际执行
"""

import argparse
import json
import re
import shutil
import sys
from datetime import datetime, timezone
from pathlib import Path

try:
    import yaml
except ImportError:
    print("Error: pyyaml not installed. Run: pip install pyyaml", file=sys.stderr)
    sys.exit(1)

# Paths
SCRIPT_DIR = Path(__file__).resolve().parent
REPO_ROOT = SCRIPT_DIR.parent.parent   # numind-server/
NDF_DIR = REPO_ROOT / '.ndf'
OLD_MANIFEST = REPO_ROOT / 'build-manifest.yaml'
NEW_MANIFEST = NDF_DIR / 'manifest.yaml'
ARCHIVED_DIR = NDF_DIR / 'archived'
DECISIONS_DIR = NDF_DIR / 'decisions'
STATE_FILE = NDF_DIR / 'state.json'
SCHEMA_DIR = NDF_DIR / 'schema'

# Thresholds
ARCHIVE_THRESHOLD_DAYS = 7
DECISION_LENGTH_THRESHOLD = 200


def parse_date(s):
    """尝试多种日期格式"""
    if not s or s in ('null', 'None'):
        return None
    if isinstance(s, datetime):
        return s
    s = str(s)
    for fmt in ['%Y-%m-%dT%H:%M:%S%z', '%Y-%m-%d %H:%M:%S%z', '%Y-%m-%d']:
        try:
            dt = datetime.strptime(s, fmt)
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=timezone.utc)
            return dt
        except ValueError:
            pass
    return None


def slugify(text, max_len=40):
    """转 filename-safe slug，保留中文和英文字"""
    # 取第一个句子或前 80 字符
    text = re.split(r'[。.：:!\(]', text, maxsplit=1)[0]
    text = text[:80]
    # 删特殊字符（保留中英文字母数字、空格、连字符）
    text = re.sub(r'[^\w一-鿿\s-]', '', text)
    # 空格→连字符
    text = re.sub(r'\s+', '-', text.strip())
    return text[:max_len] or 'untitled'


def extract_decision_topic(dec):
    """从 decision 字符串提取主题：例如 '2026-05-19 (S2 用户协作偏好确立 — 跨 feature 适用): ...' """
    # 标准格式: 'YYYY-MM-DD (主题): 内容'
    m = re.match(r'^(\d{4}-\d{2}-\d{2})\s*\((.*?)\):\s*(.+)', dec, re.DOTALL)
    if m:
        return m.group(1), m.group(2).strip(), m.group(3).strip()
    return None, dec[:60], dec


def main():
    parser = argparse.ArgumentParser(description='NDF v1 → v2 manifest 迁移')
    parser.add_argument('--dry-run', action='store_true', help='只看预期产出不写文件')
    parser.add_argument('--apply', action='store_true', help='实际执行迁移')
    parser.add_argument('--archive-days', type=int, default=ARCHIVE_THRESHOLD_DAYS,
                        help=f'completed > N 天后归档（默认 {ARCHIVE_THRESHOLD_DAYS}）')
    args = parser.parse_args()

    if not args.dry_run and not args.apply:
        print("Error: 必须指定 --dry-run 或 --apply", file=sys.stderr)
        sys.exit(1)

    if not OLD_MANIFEST.exists():
        print(f"Error: {OLD_MANIFEST} 不存在", file=sys.stderr)
        sys.exit(1)

    print(f"=== NDF v1 → v2 manifest 迁移 ({'DRY RUN' if args.dry_run else 'APPLY'}) ===\n")

    # Load manifest（带预清洗：处理内嵌 ASCII 双引号）
    with open(OLD_MANIFEST) as f:
        raw = f.read()

    # 修复：扫描形如 `      - "..."` 的 list item，把内嵌的 ASCII " 替换成中文 curly quotes
    fixed_lines = []
    fixed_count = 0
    for line in raw.splitlines():
        m = re.match(r'^(\s+-\s+)"(.*)"(\s*)$', line)
        if m:
            prefix, content, suffix = m.group(1), m.group(2), m.group(3)
            # 统计未转义的 " 数量（跳过 \"）
            raw_quotes = 0
            i = 0
            while i < len(content):
                if content[i] == '\\' and i + 1 < len(content) and content[i+1] == '"':
                    i += 2  # 跳过 \"（合法 escape）
                elif content[i] == '"':
                    raw_quotes += 1
                    i += 1
                else:
                    i += 1
            if raw_quotes > 0:
                # 交替替换未转义的 " 为 中文 curly quote
                new_content = ""
                toggle = True
                i = 0
                while i < len(content):
                    if content[i] == '\\' and i + 1 < len(content) and content[i+1] == '"':
                        new_content += content[i:i+2]  # 保留 \"
                        i += 2
                    elif content[i] == '"':
                        new_content += "“" if toggle else "”"
                        toggle = not toggle
                        i += 1
                    else:
                        new_content += content[i]
                        i += 1
                fixed_count += 1
                fixed_lines.append(f'{prefix}"{new_content}"{suffix}')
                continue
        fixed_lines.append(line)
    if fixed_count > 0:
        print(f"ℹ️  预清洗：修复 {fixed_count} 行内嵌 ASCII 双引号（转中文 curly quote）")

    data = yaml.safe_load('\n'.join(fixed_lines))

    features = data.get('features', [])
    now = datetime.now(timezone.utc).astimezone()
    archive_cutoff = now.timestamp() - args.archive_days * 86400

    archived_by_month = {}
    cancelled_features = []
    active_features = []

    stats = {
        'total': len(features),
        'archived_completed': 0,
        'archived_cancelled': 0,
        'active_kept': 0,
        'decisions_total': 0,
        'decisions_extracted': 0,
        'decisions_kept_inline': 0,
    }

    extraction_plan = []  # [(feature_id, count, total_chars)]

    for feat in features:
        fid = feat.get('id', 'unknown')
        stage = feat.get('stage', 'unknown')
        completed_at = parse_date(feat.get('completed_at'))
        last_updated_at = parse_date(feat.get('last_updated_at'))
        # fallback：完成但没 completed_at → 用 last_updated_at
        effective_completed = completed_at or last_updated_at

        # 决定归档与否
        archive_reason = None
        if stage == 'cancelled':
            archive_reason = 'cancelled'
        elif stage == 'completed' and effective_completed:
            age_days = (now - effective_completed).days
            if age_days >= args.archive_days:
                archive_reason = f'completed-{age_days}d'

        # 拆 decisions[]
        decisions = feat.get('decisions') or []
        long_decisions = []
        short_decisions = []
        for dec in decisions:
            dec_str = str(dec).strip()
            if len(dec_str) > DECISION_LENGTH_THRESHOLD:
                long_decisions.append(dec_str)
            else:
                short_decisions.append(dec_str)

        stats['decisions_total'] += len(decisions)
        stats['decisions_extracted'] += len(long_decisions)
        stats['decisions_kept_inline'] += len(short_decisions)

        # 重建 feature dict（保持原顺序但替换 decisions）
        slim_feat = {}
        for k, v in feat.items():
            if k == 'decisions':
                slim_feat[k] = short_decisions if short_decisions else []
                if long_decisions:
                    slim_feat['decisions_archived'] = f'.ndf/decisions/{fid}/'
            else:
                slim_feat[k] = v

        # ADR 提取计划
        if long_decisions:
            extraction_plan.append((fid, len(long_decisions),
                                    sum(len(d) for d in long_decisions)))

        # 写 ADR 文件（仅 apply 模式）
        if args.apply and long_decisions:
            adr_dir = DECISIONS_DIR / fid
            adr_dir.mkdir(parents=True, exist_ok=True)
            for i, dec in enumerate(long_decisions, 1):
                date_str, topic, body = extract_decision_topic(dec)
                slug = slugify(topic)
                filename = f"{i:04d}-{slug}.md"
                adr_path = adr_dir / filename
                with open(adr_path, 'w') as f:
                    f.write(f"# {topic}\n\n")
                    if date_str:
                        f.write(f"**Date:** {date_str}\n\n")
                    f.write(f"**Feature:** `{fid}`\n\n")
                    f.write(f"**Migrated from:** `build-manifest.yaml` decisions[]\n\n")
                    f.write("---\n\n")
                    f.write(body)
                    f.write("\n")

        # 归档 vs active
        if archive_reason == 'cancelled':
            cancelled_features.append(slim_feat)
            stats['archived_cancelled'] += 1
        elif archive_reason and archive_reason.startswith('completed'):
            month = effective_completed.strftime('%Y-%m')
            archived_by_month.setdefault(month, []).append(slim_feat)
            stats['archived_completed'] += 1
        else:
            active_features.append(slim_feat)
            stats['active_kept'] += 1

    # === Dry-run 报告 ===
    print(f"Total features: {stats['total']}")
    print(f"  → Active (留 .ndf/manifest.yaml): {stats['active_kept']}")
    print(f"  → Archived completed (> {args.archive_days}d): {stats['archived_completed']}")
    print(f"  → Archived cancelled: {stats['archived_cancelled']}")
    print()
    print(f"Total decisions: {stats['decisions_total']}")
    print(f"  → Inline kept (≤ {DECISION_LENGTH_THRESHOLD} chars): {stats['decisions_kept_inline']}")
    print(f"  → Extract to ADR (> {DECISION_LENGTH_THRESHOLD} chars): {stats['decisions_extracted']}")
    print()

    if extraction_plan:
        print("Top features by extracted decisions:")
        for fid, count, chars in sorted(extraction_plan, key=lambda x: -x[1])[:10]:
            print(f"  {count:3d} ADRs ({chars:,} chars) → .ndf/decisions/{fid}/")
        print()

    if args.dry_run:
        # 估算新 manifest 大小
        approx = yaml.dump({'schema_version': '2.0', 'features': active_features},
                           allow_unicode=True, sort_keys=False)
        new_lines = len(approx.splitlines())
        new_bytes = len(approx.encode('utf-8'))
        old_lines = sum(1 for _ in open(OLD_MANIFEST))
        old_bytes = OLD_MANIFEST.stat().st_size
        print(f"=== Size estimation ===")
        print(f"Old manifest: {old_lines} lines ({old_bytes:,} bytes)")
        print(f"New manifest: ~{new_lines} lines ({new_bytes:,} bytes)")
        ratio = new_bytes / old_bytes * 100 if old_bytes else 0
        print(f"Ratio: {ratio:.1f}%  →  bytes saved: {old_bytes - new_bytes:,}")
        print()
        print("Dry run 完成，未写任何文件。确认无误后 --apply。")
        return

    # === Apply 模式：实际写文件 ===
    print("=== Applying ===")

    # 备份
    backup_name = REPO_ROOT / f"build-manifest.yaml.legacy.{datetime.now().strftime('%Y%m%d')}"
    shutil.copy2(OLD_MANIFEST, backup_name)
    print(f"✓ Backup: {backup_name}")

    # 确保目录
    NDF_DIR.mkdir(parents=True, exist_ok=True)
    ARCHIVED_DIR.mkdir(parents=True, exist_ok=True)
    DECISIONS_DIR.mkdir(parents=True, exist_ok=True)
    SCHEMA_DIR.mkdir(parents=True, exist_ok=True)

    # 写归档（completed 按月份）
    for month, feats in archived_by_month.items():
        archive_path = ARCHIVED_DIR / f"{month}-completed.yaml"
        with open(archive_path, 'w') as f:
            yaml.dump({'schema_version': '2.0', 'features': feats}, f,
                      allow_unicode=True, sort_keys=False, default_flow_style=False, width=200)
        print(f"✓ Archived {len(feats)} completed features → {archive_path}")

    if cancelled_features:
        cancelled_path = ARCHIVED_DIR / 'cancelled.yaml'
        with open(cancelled_path, 'w') as f:
            yaml.dump({'schema_version': '2.0', 'features': cancelled_features}, f,
                      allow_unicode=True, sort_keys=False, default_flow_style=False, width=200)
        print(f"✓ Archived {len(cancelled_features)} cancelled features → {cancelled_path}")

    # 写新 manifest
    with open(NEW_MANIFEST, 'w') as f:
        yaml.dump({'schema_version': '2.0', 'features': active_features}, f,
                  allow_unicode=True, sort_keys=False, default_flow_style=False, width=200)

    new_lines = sum(1 for _ in open(NEW_MANIFEST))
    old_lines = sum(1 for _ in open(OLD_MANIFEST))
    print(f"✓ New manifest: {NEW_MANIFEST} ({new_lines} lines vs old {old_lines} lines)")

    # 初始化 state.json
    if not STATE_FILE.exists():
        state = {'version': 'ndf-v2', 'active_feature': None, 'active': None}
        with open(STATE_FILE, 'w') as f:
            json.dump(state, f, indent=2, ensure_ascii=False)
        print(f"✓ Initialized state.json (empty active)")
    else:
        print(f"ℹ️  state.json already exists, leaving as-is")

    # 写 schema 文件
    schema_path = SCHEMA_DIR / 'manifest.schema.json'
    if not schema_path.exists():
        schema = {
            "$schema": "http://json-schema.org/draft-07/schema#",
            "title": "NDF v2 Manifest",
            "type": "object",
            "required": ["schema_version", "features"],
            "properties": {
                "schema_version": {"const": "2.0"},
                "features": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "required": ["id", "track", "stage"],
                        "properties": {
                            "id": {"type": "string"},
                            "track": {"enum": ["micro", "hotfix", "standard"]},
                            "stage": {"type": "string"},
                            "description": {"type": "string"},
                            "repos": {"type": "array", "items": {"type": "string"}},
                            "branches": {"type": "object"},
                            "progress": {"type": "object"},
                            "blockers": {"type": "array"},
                            "created_at": {"type": "string"},
                            "completed_at": {"type": ["string", "null"]},
                            "decisions": {"type": "array", "items": {"type": "string"}},
                            "decisions_archived": {"type": "string"}
                        }
                    }
                }
            }
        }
        with open(schema_path, 'w') as f:
            json.dump(schema, f, indent=2, ensure_ascii=False)
        print(f"✓ Schema: {schema_path}")

    # 迁移报告
    report_path = NDF_DIR / 'migration-report.md'
    with open(report_path, 'w') as f:
        f.write("# NDF v1 → v2 Migration Report\n\n")
        f.write(f"**Date:** {now.strftime('%Y-%m-%d %H:%M:%S %Z')}\n")
        f.write(f"**Backup:** `{backup_name.name}` (keep for 30 days)\n\n")
        f.write(f"## Summary\n\n")
        f.write(f"- Total features: {stats['total']}\n")
        f.write(f"- Active (留 manifest): {stats['active_kept']}\n")
        f.write(f"- Archived completed: {stats['archived_completed']}\n")
        f.write(f"- Archived cancelled: {stats['archived_cancelled']}\n")
        f.write(f"- Decisions extracted to ADR: {stats['decisions_extracted']}/{stats['decisions_total']}\n\n")
        f.write(f"## Size change\n\n")
        f.write(f"- Old `build-manifest.yaml`: {old_lines} lines\n")
        f.write(f"- New `.ndf/manifest.yaml`: {new_lines} lines\n")
        f.write(f"- Reduction: {(1 - new_lines/old_lines)*100:.1f}%\n\n")
        f.write(f"## Active features (kept in manifest)\n\n")
        for feat in active_features:
            f.write(f"- `{feat['id']}` (stage={feat.get('stage')}, track={feat.get('track')})\n")
        f.write(f"\n## Archived\n\n")
        for month, feats in archived_by_month.items():
            f.write(f"### {month}-completed.yaml ({len(feats)} features)\n")
            for feat in feats:
                f.write(f"- `{feat['id']}`\n")
            f.write("\n")
    print(f"✓ Migration report: {report_path}")

    print(f"\n=== 完成 ===")
    print(f"原 manifest 保留为 {backup_name.name}")
    print(f"新 NDF v2 状态：")
    print(f"  - {NEW_MANIFEST.relative_to(REPO_ROOT)} ({new_lines} 行 vs 旧 {old_lines} 行，省 {(1 - new_lines/old_lines)*100:.0f}%)")
    print(f"  - {DECISIONS_DIR.relative_to(REPO_ROOT)} ({stats['decisions_extracted']} 个 ADR)")
    print(f"  - {ARCHIVED_DIR.relative_to(REPO_ROOT)} (归档历史)")
    print(f"  - {STATE_FILE.relative_to(REPO_ROOT)} (空 active，等 ndf-start)")


if __name__ == '__main__':
    main()
