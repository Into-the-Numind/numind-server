# 周会员转年会员订阅修复实施计划

## Task 1 — 客户问题复现测试

- 文件：`internal/numind/biz/membership/subscription_test.go`
- 新增有效周会员开通月会员被拒绝、过期周会员重新开通月会员重置套餐字段的测试。
- 验收：在未修复代码上至少一个断言失败；单独提交 `test(qa): reproduce weekly to annual membership conversion`。

## Task 2 — 月会员开通路径修复

- 文件：`internal/numind/biz/membership/subscription.go`、`internal/numind/biz/membership/subscription_test.go`
- 有效周会员保护；在 new/reopen/renew 路径显式维护月会员和 2,000 积分字段。
- 验收：Task 1 测试通过，既有订阅测试通过。

## 验证

- `go test ./internal/numind/biz/membership`
- `go test ./...`
- `task lint`
- 双重代码审查：规格符合性与代码质量均无 P0。
