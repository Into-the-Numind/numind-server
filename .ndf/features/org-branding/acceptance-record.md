# 验收记录 — 机构自定义品牌名（org-branding）

## 部署状态（dev，2026-06-16）
| 仓库 | image tag | 端口 | healthz |
|---|---|---|---|
| numind-server | 4da6e84f-dirty | 9091 | ✓ ok |
| numind-web-v3 | ed94046 | 9200 | ✓ healthy |
| numind-admin-web | d0b6416 | 9100 | ✓ healthy |

- DB migration：手工 SSH 在 dev MySQL（容器 numind-mysql-dev / 库 numind-dev）执行 `ALTER TABLE user ADD COLUMN company_name VARCHAR(100) NOT NULL DEFAULT ''`（AutoMigrate User 被注释掉，CI 不跑 migration，故手工跑）。列已确认存在。

## dev 端到端验证结果（用 E2E 父账号）
| AC | 验证方式 | 结果 |
|---|---|---|
| AC1 父账户设名→侧边栏显示 | gstack 浏览器：设置页填名 blur→侧边栏左上角响应式更新（"有数测试机构"/"测试机构改名B"）| ✓ PASS |
| AC2 子账户继承父名 | 后端 ResolveCompanyName Go 测 + 前端直读 company_name 无回查 | ✓ PASS（逻辑）|
| AC3 清空→兜底"有数AI" | API PUT ""→GET "" + store SQLite 测 + 真实 MySQL 验证 | ✓ PASS |
| AC4 未设置→"有数AI" | displayBrandName 兜底 + 登录页文字 | ✓ PASS |
| AC5 子账户无编辑框 | 设置页 v-if isParentUser；父账户实测可见编辑框 | ✓ PASS |
| AC6 子账户 PUT 被忽略 | 后端 biz 守卫 Go 测 | ✓ PASS |
| AC7 用户端登录页文字 | gstack 本地 + dev 部署，.login-logo="有数AI" 品牌绿 | ✓ PASS |
| AC8 管理端登录页文字 | gstack 本地 + dev 部署，logo="有数AI"+标题"有数AI管理后台" | ✓ PASS |
| AC9 GET /me 含 company_name | dev API 实测响应含字段 | ✓ PASS |

- 全程无 console error。E2E 测试账号 company_name 已复位为空（不污染共享账号）。
- 截图：/tmp/org_login_v3.png、/tmp/org_login_admin.png、/tmp/org_dev_sidebar.png、/tmp/org_dev_settings_company.png

## 待用户验收（S6 硬门禁）
用户在 dev（http://49.233.219.254:9200）用**自己的机构父账户**验证：
1. 登录 → 左上角显示"有数AI"（未设置时）。
2. 设置 → 个人信息 → 公司名称 → 填机构名 → 点别处（blur）保存 → 左上角变机构名。
3. 用该机构下的**子账户**登录 → 左上角也显示机构名，且设置页**无**公司名编辑框。
4. 清空公司名保存 → 左上角回退"有数AI"。

## prod 上线
**不在本次授权范围**。用户要求与 prod 打 tag 隔离、不部署 prod。上线时机由用户另行授权（S7：tag `v*` + `/deploy-prod server` + web-v3/admin-web tag；记得 prod DB 也要手工跑同一 migration）。
