# Prod 产品功能构建开关 — H2 验证

## 范围

- 用户前端 Prod 构建打开通知中心。
- 用户前端 Prod 构建打开文档系统。
- 会议副驾在 Prod 保持关闭。
- 说话人分离在 Prod 保持关闭。

## TDD 证据

- RED commit：`c83e922 test(cicd): lock prod product feature flags`
  - 旧脚本缺少 `VITE_ENABLE_NOTIFICATIONS=true`。
  - 旧脚本缺少 `VITE_ENABLE_DOCUMENT_SYSTEM=true`。
  - 会议副驾和说话人分离的 Prod 空值断言通过。
- GREEN commit：`07a9436 fix(cicd): enable prod notification and document flags`

## 自动验证

- `bash scripts/cicd/test-prod-feature-flags.sh`：PASS。
- `bash scripts/cicd/test-push-guard.sh`：PASS。
- `bash -n scripts/cicd/build-and-push.sh scripts/cicd/test-prod-feature-flags.sh`：PASS。
- `npm run lint`：PASS，0 errors；7 个既有 unused warnings。
- `npm run type-check`：PASS。
- Prod 等价变量执行 `npm run build-only`：PASS。
- `npm run test:unit`：102 个 test files PASS；1145 tests PASS、11 skipped、3 todo。

## 结论

H2 通过，可以进入 H3：合并到用户前端 develop、部署 Dev，并按实际入口确认通知中心和文档系统可见，同时会议副驾保持现状。
