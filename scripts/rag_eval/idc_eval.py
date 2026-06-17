import sys, json, math, requests, yaml, os
LOGIN="http://localhost:19099"; BASE="http://localhost:19091"; K=5
gold=yaml.safe_load(open('/tmp/golden_idc.yaml',encoding='utf-8'))
tok=requests.post(f"{LOGIN}/v1/admin/login",json={"username":"admin","password":"admin123456"},timeout=15).json()["data"]["token"]
H={"Authorization":f"Bearer {tok}"}
DOCS=[26,29,37,41,49,50,55,56,57,58,61,97,98]

def capture():
    cap=[]
    for q in gold:
        body={"query":q["query"],"user_id":350,"document_ids":DOCS,"top_k":20,"rerank_top_n":20,
              "rewrite_query":False,"rerank_min_score":0.01,"rerank_no_floor":True}
        r=requests.post(f"{BASE}/v1/admin/rag-eval/retrieve",json=body,headers=H,timeout=60).json()
        ch=(r.get("data") or {}).get("chunks") or []
        cap.append({"id":q["id"],"type":q["type"],"expected":q.get("expected_doc_ids",[]),
                    "query":q["query"],"ranked":[{"doc":c["document_id"],"score":round(c["score"],4)} for c in ch]})
    json.dump(cap,open('/tmp/idc_capture.json','w'),ensure_ascii=False)
    return cap

cap = capture()
print("captured", len(cap), "queries")

def docrank(ranked):  # chunks (score-desc) -> doc list, first occurrence
    seen=[]
    for c in ranked:
        if c["doc"] not in seen: seen.append(c["doc"])
    return seen

def score(rank_fn):
    rec=rr=ndcg=0.0; inkb=0; oob=0; ref=0
    for d in cap:
        docs=rank_fn(d)[:K]; exp=set(d["expected"])
        if not exp:
            oob+=1; ref+= 1 if not docs else 0
        else:
            inkb+=1
            hit=1 if any(x in exp for x in docs) else 0; rec+=hit
            for i,x in enumerate(docs):
                if x in exp: rr+=1/(i+1); break
            dcg=sum(1/math.log2(i+2) for i,x in enumerate(docs) if x in exp)
            idcg=sum(1/math.log2(i+2) for i in range(min(len(exp),K)))
            ndcg+= dcg/idcg if idcg>0 else 0
    return dict(recall=round(rec/inkb,3),mrr=round(rr/inkb,3),ndcg=round(ndcg/inkb,3),
               oob_refusal=round(ref/oob,3) if oob else None, inkb=inkb, oob=oob)

# ---- threshold sweep (vector+rerank only) ----
def thr(T): return lambda d: docrank([c for c in d["ranked"] if c["score"]>=T])
print("\n### A) VECTOR+RERANK threshold sweep (real iDC data) ###")
for T in [0.0,0.2,0.3,0.4,0.5,0.6,0.7]:
    print(f"  T={T:.1f}: ", score(thr(T)))

# ---- hybrid: BM25(lexical) + vector, RRF fusion ----
import jieba
from rank_bm25 import BM25Okapi
exp_chunks=json.load(open('/tmp/idc_export.json'))  # id, document_id, content
def tok(s):
    s=s.lower()
    return [w for w in jieba.cut(s) if w.strip()]
corpus_docs=[c['document_id'] for c in exp_chunks]
bm25=BM25Okapi([tok(c['content']) for c in exp_chunks])
def bm25_docrank(query):
    sc=bm25.get_scores(tok(query))
    bydoc={}
    for i,c in enumerate(exp_chunks):
        bydoc[c['document_id']]=max(bydoc.get(c['document_id'],-1),sc[i])
    return [d for d,_ in sorted(bydoc.items(),key=lambda x:-x[1])]
def rrf(d, kk=60):
    vec=docrank(d["ranked"])
    lex=bm25_docrank(d["query"])
    fused={}
    for r,doc in enumerate(vec): fused[doc]=fused.get(doc,0)+1/(kk+r+1)
    for r,doc in enumerate(lex): fused[doc]=fused.get(doc,0)+1/(kk+r+1)
    return [doc for doc,_ in sorted(fused.items(),key=lambda x:-x[1])]
print("\n### B) HYBRID (BM25 + vector, RRF) vs vector-only (no threshold, in-KB recall ceiling) ###")
print("  vector-only(raw):", score(lambda d: docrank(d["ranked"])))
print("  bm25-only       :", score(lambda d: bm25_docrank(d["query"])))
print("  hybrid RRF      :", score(rrf))
