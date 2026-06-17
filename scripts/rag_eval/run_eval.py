#!/usr/bin/env python3
"""RAG 检索评估打分工具（rag-eval-harness）。

读 golden YAML，对每题调 admin 检索调试端点 (/v1/admin/rag-eval/retrieve)，
按"期望文档有没有进前 k 名 / 排第几"算 recall@k、MRR、nDCG@k，输出报告。

用法:
  python3 run_eval.py --golden golden.yaml \
      --base-url http://49.233.219.254:9099 \
      --user admin --password admin123456 --k 5

golden YAML 每条:
  - id: q1
    query: "创业要经过哪几个阶段?"
    type: in_kb_single            # in_kb_single|in_kb_multi|exact_term|paraphrase|out_of_kb
    scope: { user_id: 0, document_ids: [113] }   # 评估锚定的语料范围
    expected_doc_ids: [113]       # 正确答案应来自哪篇(out_of_kb 留空 [])
  ...
"""
import argparse
import math
import sys
from collections import defaultdict

import requests
import yaml


def login(base_url, user, password):
    r = requests.post(f"{base_url}/v1/admin/login",
                      json={"username": user, "password": password}, timeout=15)
    r.raise_for_status()
    data = r.json().get("data") or {}
    token = data.get("token") or data.get("access_token") or data.get("Token")
    if not token:
        sys.exit(f"login failed: {r.text[:200]}")
    return token


def retrieve(base_url, token, query, scope, k):
    body = {
        "query": query,
        "user_id": scope.get("user_id", 0),
        "document_ids": scope.get("document_ids", []),
        "all_enabled": scope.get("all_enabled", False),
        "top_k": max(k, 10),
        "rerank_top_n": max(k, 10),
        # 默认 RerankMinScore=0：量原始排序召回(不被 0.6 阈值丢)。
    }
    r = requests.post(f"{base_url}/v1/admin/rag-eval/retrieve", json=body,
                      headers={"Authorization": f"Bearer {token}"}, timeout=60)
    r.raise_for_status()
    chunks = (r.json().get("data") or {}).get("chunks") or []
    # 去重保序：同一文档多 chunk 命中，文档级 rank 取首次出现。
    seen, doc_rank = set(), []
    for ch in chunks:
        did = ch.get("document_id")
        if did not in seen:
            seen.add(did)
            doc_rank.append(did)
    return doc_rank


def score_one(expected, ranked_docs, k):
    """返回 (hit@k, reciprocal_rank, ndcg@k)。expected 为空=out_of_kb。"""
    if not expected:
        # 应拒答：理想是没有"被判为相关"的文档；这里记 ranked 为空则视为正确。
        return (1.0 if not ranked_docs else 0.0), 0.0, 0.0
    exp = set(expected)
    topk = ranked_docs[:k]
    hit = 1.0 if any(d in exp for d in topk) else 0.0
    rr = 0.0
    for i, d in enumerate(ranked_docs):
        if d in exp:
            rr = 1.0 / (i + 1)
            break
    dcg = sum((1.0 / math.log2(i + 2)) for i, d in enumerate(topk) if d in exp)
    idcg = sum((1.0 / math.log2(i + 2)) for i in range(min(len(exp), k)))
    ndcg = (dcg / idcg) if idcg > 0 else 0.0
    return hit, rr, ndcg


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--golden", required=True)
    ap.add_argument("--base-url", default="http://49.233.219.254:9099")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--password", default="admin123456")
    ap.add_argument("--k", type=int, default=5)
    args = ap.parse_args()

    golden = yaml.safe_load(open(args.golden, encoding="utf-8"))
    if not isinstance(golden, list):
        sys.exit("golden YAML 根必须是题目列表")

    token = login(args.base_url, args.user, args.password)

    by_type = defaultdict(lambda: {"hit": 0.0, "rr": 0.0, "ndcg": 0.0, "n": 0})
    tot = {"hit": 0.0, "rr": 0.0, "ndcg": 0.0, "n": 0}
    rows = []
    for q in golden:
        ranked = retrieve(args.base_url, token, q["query"], q.get("scope", {}), args.k)
        hit, rr, ndcg = score_one(q.get("expected_doc_ids", []), ranked, args.k)
        t = q.get("type", "unknown")
        for agg in (by_type[t], tot):
            agg["hit"] += hit; agg["rr"] += rr; agg["ndcg"] += ndcg; agg["n"] += 1
        rows.append((q.get("id", "?"), t, hit, rr, ranked[:args.k]))

    print(f"\n=== RAG 检索评估 (k={args.k}, {tot['n']} 题) ===")
    print(f"{'id':<10}{'type':<16}{'hit@k':<7}{'RR':<7}top{args.k}_docs")
    for rid, t, hit, rr, top in rows:
        print(f"{rid:<10}{t:<16}{int(hit):<7}{rr:<7.2f}{top}")

    def line(name, a):
        n = max(a["n"], 1)
        print(f"{name:<18} recall@{args.k}={a['hit']/n:.3f}  MRR={a['rr']/n:.3f}  nDCG@{args.k}={a['ndcg']/n:.3f}  (n={a['n']})")
    print("\n--- 分类型 ---")
    for t in sorted(by_type):
        line(t, by_type[t])
    print("--- 总体 ---")
    line("ALL", tot)


if __name__ == "__main__":
    main()
