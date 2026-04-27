# Context Budget max_output_tokens Backfill (2026-04-27)

一次性 backfill：将 prod 环境 `ai_service` 表中所有 `service_type='llm'`、`max_output_tokens=NULL` 或 `max_output_tokens=0` 的行，设置正确的 `max_output_tokens` 值。

## 背景

F-1 Context Budget Middleware 在每次 LLM 调用前需要从 `capability_json.max_output_tokens` 读取模型上限。  
该字段为 NULL/0 时，middleware 抛出 `ErrContextConfigInvalid`，Context Budget feature 完全失效。

S5 验证（2026-04-27）在 dev 发现 12/14 LLM service 该字段为 NULL，用 32768 blanket 临时 backfill 通过测试。  
**Production 上线前必须换成每个模型的真实合理值**。

详细调研和每个模型的推荐值见：  
`docs/superpowers/research/2026-04-27-llm-max-output-tokens-table.md`

## 范围

| 操作 | 说明 |
|------|------|
| 目标数据库 | `numind-prod`（生产库） |
| 目标表 | `ai_service` |
| 目标行 | `service_type='llm'` AND (`max_output_tokens IS NULL` OR `max_output_tokens = 0`) |
| 影响字段 | `capability_json`（仅写入 `$.max_output_tokens` 子路径，其他 JSON 字段不变） |

## 执行顺序（在 prod 上）

```bash
# 1. Dry-run（只读，列出 NEEDS BACKFILL 的行）
docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod \
  < 01-dry-run.sql > dry-run-output.txt 2>&1

# 2. 对照 dry-run-output.txt 的 model_key 列表，核对 02-apply.sql 中的 UPDATE 语句
#    如有未覆盖的 model_key，参考调研文档 §3.1 添加对应 UPDATE 块后再执行

# 3. 备份（rollback 安全网，强烈建议）
mysqldump -uroot -p<PASSWORD> --single-transaction --no-tablespaces \
  numind-prod ai_service > ai_service_backup_$(date +%Y%m%d_%H%M%S).sql

# 4. Apply（事务，含 per-model-family UPDATE）
docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod \
  < 02-apply.sql

# 5. Verify（期望 0 行输出）
docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod \
  < 03-verify.sql

# 6. (仅紧急情况) Rollback — 优先从 mysqldump 恢复，次选 04-rollback.sql
docker exec -i numind-mysql-prod mysql -uroot -p<PASSWORD> numind-prod \
  < 04-rollback.sql
```

## 跑前检查

- [ ] 确认当前数据库是 `numind-prod`（不是 dev 或 qa）
- [ ] 确认 01-dry-run.sql 输出中有 `NEEDS BACKFILL` 的行（否则无需操作）
- [ ] 对照调研文档核对每个 model_key 的推荐 max_output_tokens 值
- [ ] mysqldump 备份已完成并保存到安全位置
- [ ] 02-apply.sql 的 UPDATE 块已覆盖所有 NEEDS BACKFILL 的 model_key

## 跑后检查

- [ ] 03-verify.sql 输出 **0 行**（`NEEDS BACKFILL` 清零，无 max_output_tokens >= cw 的行）
- [ ] 管理端 AI Service 列表页抽查 2-3 个模型，`max_output_tokens` 字段已填充
- [ ] Context Budget feature 接口 smoke test 正常（无 `ErrContextConfigInvalid` 错误）

## 风险点 + Rollback 触发条件

| 风险 | 影响 | 触发 Rollback 条件 |
|------|------|-----------------|
| max_output_tokens 设置过高（超出 provider cap）| LLM API 调用返回 4xx | 设置后出现大量 API 400/422 错误 |
| max_output_tokens < reserved_output_tokens（默认 16384）| ErrContextConfigInvalid，feature 废 | 03-verify.sql 输出非零行（含 cw 约束检查） |
| JSON_SET 误覆盖其他 capability_json 字段 | capability_json 部分字段丢失 | 管理端 AI Service 详情页数据异常 |

**Rollback 优先级**：

1. **首选**：从 mysqldump 恢复 `ai_service` 表（干净，无副作用）
2. **次选**：运行 `04-rollback.sql`（仅移除本次写入的 `max_output_tokens`，不影响其他字段）

## 设计要点

- **原子性**：02-apply.sql 用 `START TRANSACTION; ... COMMIT;` 包裹所有写入
- **幂等性**：所有 UPDATE 的 WHERE 子句包含 `(IS NULL OR = 0)` 过滤，重复执行不会覆盖已有值
- **最小化变更**：使用 `JSON_SET(capability_json, '$.max_output_tokens', value)` 仅写入单个 JSON 路径，其余字段不变
- **LIKE 模板覆盖**：02-apply.sql 使用 `model_key LIKE 'prefix%'` 模式，而非精确匹配，确保 prod 环境中额外的模型变种也被覆盖

## 后续操作

Backfill 使用保守最小值（conservative floor）。Production 稳定后，admin 可通过管理端 AI Service 编辑页面将每个模型调高至实际广告上限。详见调研文档 §6 升级路径。
