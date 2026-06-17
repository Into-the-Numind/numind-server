import sys, json, math, requests, yaml
LOGIN="http://localhost:19099"; BASE="http://localhost:19091"; K=5; T=0.3
DOCS=[26,29,37,41,49,50,55,56,57,58,61,97,98]
gold=yaml.safe_load(open('/tmp/golden_idc.yaml',encoding='utf-8'))
rw=json.load(open(sys.argv[1],encoding='utf-8')) if len(sys.argv)>1 else {}
tok=requests.post(f"{LOGIN}/v1/admin/login",json={"username":"admin","password":"admin123456"},timeout=15).json()["data"]["token"]
H={"Authorization":f"Bearer {tok}"}
def hit(qtext):
    body={"query":qtext,"user_id":350,"document_ids":DOCS,"top_k":20,"rerank_top_n":20,
          "rewrite_query":False,"rerank_min_score":T,"rerank_no_floor":True}
    r=requests.post(f"{BASE}/v1/admin/rag-eval/retrieve",json=body,headers=H,timeout=60).json()
    ch=(r.get("data") or {}).get("chunks") or []
    docs=[]
    for c in ch:
        if c["document_id"] not in docs: docs.append(c["document_id"])
    return docs[:K]
rec=rr=0.0; inkb=0; oob=0; ref=0; misses=[]
for q in gold:
    texts=rw.get(q["id"], q["query"]); texts=texts if isinstance(texts,list) else [texts]
    union=[]
    for t in texts:
        for d in hit(t):
            if d not in union: union.append(d)
    union=union[:K]
    exp=set(q.get("expected_doc_ids",[]))
    if not exp:
        oob+=1; ref+= 1 if not union else 0
    else:
        inkb+=1
        h=1 if any(x in exp for x in union) else 0; rec+=h
        if not h: misses.append(q["id"])
        for i,x in enumerate(union):
            if x in exp: rr+=1/(i+1); break
print(json.dumps(dict(recall=round(rec/inkb,3),mrr=round(rr/inkb,3),oob_refusal=round(ref/oob,3),misses=misses),ensure_ascii=False))
