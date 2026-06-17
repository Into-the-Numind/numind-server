# RAG 检索评估集（rag-eval-harness）

把 RAG 检索质量变成**可量化分数**,让后续每项改进(混合检索/查询处理/阈值调优/上下文组装)都能"改前改后跑分对比",这是把 RAG 打造成业界一流的地基。

## 组成

- **打分引擎**:admin 端点 `POST /v1/admin/rag-eval/retrieve`(admin 服务 9099),复用真实 chatbot 检索栈(query 改写→多路向量检索→rerank),返回排序后的 chunk(doc_id+score)。**只读、不在任何生产用户流程**。
- **打分脚本**:`run_eval.py` 读黄金集 → 逐题调端点 → 算 recall@k / MRR / nDCG@k → 出报告(分题型 + 总体)。
- **黄金集**:`golden.yaml`(问题 + 正确答案该来自哪篇),由业务方抽查确认。`golden.example.yaml` 是模板。

## 怎么用

```bash
pip install requests pyyaml   # 一次性
python3 run_eval.py --golden golden.yaml \
    --base-url http://49.233.219.254:9099 --user admin --password <pw> --k 5
```

## 指标含义(大白话)

- **recall@k**:该找到的文档,有没有进前 k 名(召回够不够)。首要。
- **MRR**:正确文档排第几(越靠前越好)。
- **nDCG@k**:综合命中 + 排名位置的质量分。
- **out_of_kb 题**:期望"检索不到相关"→ 考验阈值 + 防编造。

## 科学闭环(怎么用它打造一流)

1. 跑一次 → 记**基线分**。
2. 上一项改进(如混合检索)→ 再跑 → **涨了留、降了回退**。
3. 逐项迭代(混合检索→查询处理→阈值调优→上下文组装),每步用本工具验证,直到指标达标。

## 待办(建评估语料 + 黄金集)

1. 建**干净评估专用 KB**:挑代表性真实业务文档(产品手册/案例/百问百答/创始人手册等)传到一个专用账号,记下 user_id + 各 doc_id。
2. 把 doc_id 填进 `golden.yaml` 的 scope/expected_doc_ids。
3. 从真实 Langfuse 聊天记录 + 该语料起草 30-50 题,覆盖 5 种题型。
4. **业务方抽查确认标注** → 跑出基线。
