# 飞书个人工作空间 S2 真实租户 Spike

- 日期：2026-07-13
- lark-cli：1.0.68
- 身份：user
- 测试环境：独立临时 HOME（目录 `0700`，敏感配置 `0600`）
- 结果：PASS

## 验证范围

1. 由 `lark-cli config init --new` 创建个人租户自建应用，用户只在飞书官方页面确认；未手工复制 App ID/App Secret。
2. 初始用户授权只申请 `offline_access`，连接成功后没有 Docs/Base/Wiki/IM scope。
3. 在没有业务 scope 的状态下真实调用 Docs/Base/Wiki，记录固定版本 CLI 的结构化错误。
4. 对 Docs create/read/update 分三次增量申请 exact scopes，并重放原命令。
5. 最终重新读取文档，确认追加内容和 revision 已更新。

## 结构化错误证据

三类命令均返回 exit code `3`、`ok=false`、`type=authorization`、`subtype=missing_scope`、`identity=user`，并带 `missing_scopes` 与可执行的 auth hint；请求在业务副作用前被拒绝。

| 操作 | `missing_scopes` |
|---|---|
| Docs create | `docx:document:create` |
| Docs fetch | `docx:document:readonly` |
| Docs update/append | `docx:document:write_only`, `docx:document:readonly` |
| Base create | `base:app:create`, `base:table:read`, `base:table:create`, `base:table:update`, `base:table:delete` |
| Wiki space list | `wiki:space:retrieve` |

Base 结果确认了 lark-cli 1.0.68 的 `+base-create` shortcut 确实需要 table delete scope。首版仍必须在服务端拒绝所有 delete 命令和参数，授权 UI 如实说明 scope 粒度。

## 授权后重放结果

- Docs create：增量授权 `docx:document:create` 后，同一命令成功，返回 document ID、revision 3 和 URL。
- Docs fetch：增量授权 `docx:document:readonly` 后成功读取测试文档。
- Docs update：增量授权 `docx:document:write_only` + readonly 后，原 append 命令成功，revision 从 3 更新为 4。
- 复读验证：正文包含初始段落和新追加段落，证明 create/read/update 闭环成功。

本测试租户没有出现独立管理员审批步骤；lark-cli 个人自建应用路径自动协调了本次 Docs scopes。产品状态机仍保留 `waiting_app_approval`，用于企业租户策略、敏感权限或错误返回 `console_url` 的场景。

## 安全检查

- 未在工件中记录 App Secret、Token、device code、完整 open_id 或授权 URL。
- 临时 HOME 位于项目仓库外；根目录与 `.lark-cli` 为 `0700`，`config.json` 和 auth log 为 `0600`。
- 测试应用、凭据和测试文档保留到完整 E2E；S6/解绑验收后统一清理。

## S2 结论

真实租户验证支持设计中的关键假设：

- 可以先只建立 `offline_access` 长期连接，再按业务命令增量授权；
- CLI 提供稳定、机器可判定的 user missing-scope envelope；
- missing-scope 在业务副作用前返回，可以在授权完成后精确重放；
- Docs create/read/update 的 exact scopes 与设计一致；
- Base create 的 delete scope 限制必须作为产品和安全策略中的显式例外处理。

S2 技术设计与真实 spike 均通过，可以进入 S3 实施计划。
