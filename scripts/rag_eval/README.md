# RAG 检索评估集（rag-eval-harness）

把 RAG 检索质量变成**可量化分数**,让后续每项改进(混合检索/查询处理/阈值调优/上下文组装)都能"改前改后跑分对比",这是把 RAG 打造成业界一流的地基。

## 组成

- **打分引擎**:admin-gated 端点 `POST /v1/admin/rag-eval/retrieve`(注册在**用户服务 9091**,非 admin 服务——检索栈需要 AI gateway + 挂载的 sqlite-vec 卷,二者只在用户服务进程/容器存在;仍用 AdminAuthMiddleware 守卫),复用真实 chatbot 检索栈(query 改写→多路向量检索→rerank),返回排序后的 chunk(doc_id+score)。**只读、不在任何生产用户流程**。
- **打分脚本**:`run_eval.py` 读黄金集 → 逐题调端点 → 算 recall@k / MRR / nDCG@k → 出报告(分题型 + 总体)。
- **黄金集**:`golden.yaml`(问题 + 正确答案该来自哪篇),由业务方抽查确认。`golden.example.yaml` 是模板。

## 怎么用

```bash
pip install requests pyyaml          # 一次性
export NUMIND_ADMIN_PASSWORD=<pw>    # 密码走环境变量,不硬编码进脚本/命令历史
# 检索端点在【用户服务 9091】,admin 登录在【admin 服务 9099】→ 分别指定:
python3 run_eval.py --golden golden.yaml \
    --login-url http://49.233.219.254:9099 \
    --base-url  http://49.233.219.254:9091 --user admin --k 5
# (也可 --token <T> 直接传 token 跳过登录。)
# 默认对齐 chatbot 产线(rerank 0.6 阈值 + no_floor + 原话检索);
# 加 --raw 看不带阈值的原始排序召回(诊断"是召回不到还是被阈值丢了")。
```

> dev 的 9091/9099 不对公网开放时,先开 SSH 隧道(`ssh -L 19091:localhost:9091 -L 19099:localhost:9099 ...`)再把上面地址换成 localhost:1909x。
> 跑出来的基线见 **[BASELINE.md](BASELINE.md)**。

## 指标含义(大白话)

三项均在 **top-k 截断处**计算,口径一致(便于横向比较):

- **recall@k**:该找到的文档,有没有进前 k 名(召回够不够)。首要。
- **MRR@k**:正确文档在前 k 名里排第几(越靠前越好;掉出 k 名记 0)。
- **nDCG@k**:综合命中 + 排名位置的质量分。
- **out_of_kb 题**:期望"检索不到相关"(prod 模式下 rerank 0.6 阈值+no_floor 返回空)→ 只计**拒答准确率**(检索为空=正确),**不并入** MRR/nDCG 平均(否则会虚低拉平总分)。

## 科学闭环(怎么用它打造一流)

1. 跑一次 → 记**基线分**。
2. 上一项改进(如混合检索)→ 再跑 → **涨了留、降了回退**。
3. 逐项迭代(混合检索→查询处理→阈值调优→上下文组装),每步用本工具验证,直到指标达标。

## 评估语料(已就位)

锚定 dev 现有真实 KB:**user_id=25** 的莫小派销售 KB 4 篇(`document_ids=[127,128,129,146]` = 产品手册/案例库/百问百答/陪跑优势),已逐篇核对向量库实际 chunk 内容。`golden.yaml` 已起草 **22 题覆盖 5 种题型**,标注基于真实 chunk。

**仍建议业务方抽查 `golden.yaml` 标注**(尺子准不准全看这步);要扩样本就继续从真实 Langfuse 聊天记录补题到 30-50 题。

## 扩到新语料(可选)

1. 选一个真实账号的连贯 KB(避免重复/转录类长文档),`sqlite3 sales_vector.db "SELECT user_id,document_id,COUNT(*) FROM chunks GROUP BY 1,2"` 看哪些 doc 真有 chunk(只在 MySQL 有 document 行、向量库无 chunk = 检索不到)。
2. 把 user_id + doc_id 填进 `golden.yaml` 的 scope/expected_doc_ids,逐篇抽 chunk 内容起草问题。
3. **业务方抽查确认标注** → 跑出新基线。
