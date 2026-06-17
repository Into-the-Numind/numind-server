#!/usr/bin/env python3
"""RAG 检索评估打分工具（rag-eval-harness）。

读 golden YAML，对每题调 admin 检索调试端点 (/v1/admin/rag-eval/retrieve)，
按"期望文档有没有进前 k 名 / 排第几"算 recall@k、MRR、nDCG@k，输出报告。

用法:
  export NUMIND_ADMIN_PASSWORD=<pw>   # 密码走环境变量,不进命令历史/源码
  python3 run_eval.py --golden golden.yaml \
      --base-url http://49.233.219.254:9091 --user admin --k 5
  # 端点在【用户服务 9091】(非 admin 9099)——检索栈在用户服务进程,详见 README。
  # 默认对齐 chatbot 产线(0.6 阈值+no_floor+原话);加 --raw 看原始排序召回。
# 指标均在 top-k 截断处计算(recall@k / MRR@k / nDCG@k),三者口径一致。
# out_of_kb 题只计"拒答准确率"(检索为空即正确),不并入 MRR/nDCG 平均。

golden YAML 每条:
  - id: q1
    query: "海外高势能IP陪跑服务的核心定位是什么?"
    type: in_kb_single            # in_kb_single|in_kb_multi|exact_term|paraphrase|out_of_kb
    scope: { user_id: 25, document_ids: [127,128,129,146] }   # 评估锚定的语料范围
    expected_doc_ids: [128]       # 正确答案应来自哪篇(out_of_kb 留空 [])
  ...
"""
import argparse
import math
import os
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


def retrieve(base_url, token, query, scope, k, mode):
    # mode: "prod"(默认,对齐 chatbot 产线 min=0.6+no_floor 原话检索) 或 "raw"(原始排序召回)。
    prod = (mode == "prod")
    body = {
        "query": query,
        "user_id": scope.get("user_id", 0),
        "document_ids": scope.get("document_ids", []),
        "all_enabled": scope.get("all_enabled", False),
        "top_k": max(k, 10),
        "rerank_top_n": max(k, 10),
        "rewrite_query": False,                 # chatbot post-F1 用原话检索
        "rerank_min_score": 0.6 if prod else 0,  # prod=0.6 阈值(真实);raw=0
        "rerank_no_floor": True if prod else False,  # prod=低于阈值返回空(库外题可正确为空)
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
    for i, d in enumerate(topk):  # MRR@k:与 recall@k / nDCG@k 同口径(top-k 截断)
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
    ap.add_argument("--base-url", default="http://49.233.219.254:9091",
                    help="检索端点地址(用户服务,端点在此),非 admin 9099")
    ap.add_argument("--login-url", default=None,
                    help="admin 登录地址(取 token);默认=base-url。端点迁到用户服务后,登录在 "
                         "admin(9099)、检索在用户服务(9091),须分别指定 --login-url 9099 + --base-url 9091")
    ap.add_argument("--token", default=None,
                    help="直接传 admin token,跳过登录(已有 token 时用,免暴露密码)")
    ap.add_argument("--user", default="admin")
    ap.add_argument("--password", default=os.environ.get("NUMIND_ADMIN_PASSWORD"),
                    help="admin 密码;默认读环境变量 NUMIND_ADMIN_PASSWORD(不硬编码)")
    ap.add_argument("--k", type=int, default=5)
    ap.add_argument("--raw", action="store_true",
                    help="原始排序召回模式;默认对齐 chatbot 产线(0.6 阈值+no_floor+原话检索)")
    args = ap.parse_args()

    mode = "raw" if args.raw else "prod"
    with open(args.golden, encoding="utf-8") as f:
        golden = yaml.safe_load(f)
    if not isinstance(golden, list):
        sys.exit("golden YAML 根必须是题目列表")

    if args.token:
        token = args.token
    else:
        if not args.password:
            sys.exit("缺少 admin 密码:用 --password / 环境变量 NUMIND_ADMIN_PASSWORD,或直接 --token")
        token = login(args.login_url or args.base_url, args.user, args.password)

    # n        = 题数(recall/拒答准确率分母,含 out_of_kb)
    # rank_n   = 库内题数(expected 非空;MRR/nDCG 分母,out_of_kb 不并入)
    by_type = defaultdict(lambda: {"hit": 0.0, "rr": 0.0, "ndcg": 0.0, "n": 0, "rank_n": 0})
    tot = {"hit": 0.0, "rr": 0.0, "ndcg": 0.0, "n": 0, "rank_n": 0}
    rows = []
    for q in golden:
        expected = q.get("expected_doc_ids", [])
        ranked = retrieve(args.base_url, token, q["query"], q.get("scope", {}), args.k, mode)
        hit, rr, ndcg = score_one(expected, ranked, args.k)
        t = q.get("type", "unknown")
        for agg in (by_type[t], tot):
            agg["hit"] += hit
            agg["n"] += 1
            if expected:  # 库内题才计入排序指标
                agg["rr"] += rr
                agg["ndcg"] += ndcg
                agg["rank_n"] += 1
        rows.append((q.get("id", "?"), t, hit, rr, ranked[:args.k]))

    print(f"\n=== RAG 检索评估 (mode={mode}, k={args.k}, {tot['n']} 题) ===")
    print(f"{'id':<10}{'type':<16}{'hit@k':<7}{'RR@k':<7}top{args.k}_docs")
    for rid, t, hit, rr, top in rows:
        print(f"{rid:<10}{t:<16}{int(hit):<7}{rr:<7.2f}{top}")

    def line(name, a):
        n, rn = max(a["n"], 1), a["rank_n"]
        recall = a["hit"] / n
        if rn > 0:  # 库内题:报 MRR@k / nDCG@k
            print(f"{name:<18} recall@{args.k}={recall:.3f}  MRR@{args.k}={a['rr']/rn:.3f}  "
                  f"nDCG@{args.k}={a['ndcg']/rn:.3f}  (n={a['n']}, 库内={rn})")
        else:       # out_of_kb:只报拒答准确率
            print(f"{name:<18} 拒答准确率={recall:.3f}  (MRR/nDCG 不适用)  (n={a['n']})")
    print("\n--- 分类型 ---")
    for t in sorted(by_type):
        line(t, by_type[t])
    print("--- 总体 ---")
    line("ALL", tot)


if __name__ == "__main__":
    main()
