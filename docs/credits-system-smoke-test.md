# credits-system Dev Smoke Test Runbook

**目的：** S6 人工验收 + 合并 T3.1 Langfuse / E2E / gstack /qa 验证。
**环境：** dev（`$DEV_SITE_URL` / `$DEV_API_URL`）
**前提：** CI 已部署最新 develop 到 dev（容器 `numind-server-dev` + `numind-web-v3-dev` + `numind-admin-web-dev`）。

---

## 0. 部署状态确认（必做第一步）

```bash
# 1. API healthy + 新 endpoints 已注册
curl -s "$DEV_API_URL/healthz" | grep '"status":"ok"'           # 应有输出
curl -s -o /dev/null -w "%{http_code}\n" \
    -X POST "$DEV_API_URL/v1/credits/estimate"                   # 应 401（新 endpoint）
curl -s -o /dev/null -w "%{http_code}\n" \
    "$DEV_API_URL/v1/admin/estimation-coefficients"              # 应 401
curl -s -o /dev/null -w "%{http_code}\n" "$DEV_SITE_URL/"        # 应 200

# 如 estimate/admin endpoint 返回 404，则 dev 仍是旧镜像 → 走"手动部署"
```

### 手动部署 workaround（CI 挂时）

```bash
# 在本地 numind-server 目录
cd numind-server
docker build -t pmtmyaggy/numind-server:develop .
docker push pmtmyaggy/numind-server:develop  # 需要登录 docker hub

# SSH 到 dev server，触发 pull + restart
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
    "docker pull pmtmyaggy/numind-server:develop && \
     docker restart numind-server-dev && \
     sleep 5 && docker logs --tail 20 numind-server-dev"

# web-v3 和 admin-web 同理（对应 image 名）
```

---

## 1. 测试前置（seed 测试账号状态）

### 1.1 测试账号配置
- `$E2E_USERNAME` / `$E2E_PASSWORD` — 普通用户
- Admin 账号（如果测 admin UI）

### 1.2 用 admin 工具重置测试账号为 free（以便从头测）

通过 admin UI：`$DEV_SITE_URL/admin` → "用户管理" → 找 `$E2E_USERNAME` → 手动调整：
- `user_tier` = free
- `tier_expires` = NULL
- `billing_mode` = credits

或者直接 SQL（SSH 到 dev mysql）：
```sql
UPDATE `user` SET user_tier='free', tier_expires=NULL, billing_mode='credits',
    monthly_sop_runs=0
WHERE username='{E2E_USERNAME}';
```

---

## 2. 核心功能 Smoke Test（6 条关键路径）

### Path 1：free 用户账户中心 → 升级引导

**步骤：**
1. 登录 `$DEV_SITE_URL`（$E2E_USERNAME / $E2E_PASSWORD，tier=free）
2. 进 Settings 页

**预期：**
- `CreditBalanceCard` 展示 **"成为会员解锁 AI 能力"** 引导 + "升级会员"按钮
- `BoosterPurchaseCard` 展示**灰态** + "需成为正式会员后购买"提示
- 点击灰态 → tooltip 或跳转会员购买（不应崩溃）

### Path 2：新购 monthly 会员 → 双档余额 + 扣减

**步骤：**
1. 上步继续，点击"升级会员"→ 选月卡 ¥99
2. 走支付（mock 支付成功或真实微信测试账号）
3. 回 Settings 页

**预期：**
- `CreditBalanceCard` credits 模式：
  - "会员积分 **2000 / 2000**"
  - 副标题 "本月 MM-DD 过期" + 倒计时
- 没有加量包（booster 部分不渲染或 0/0）

然后：
4. 打开任一 SOP template → 点进运行详情页
5. 观察启动按钮上方

**预期：**
- `SopEstimateBar` 出现，展示 **"预估消耗 XX 积分（N 步）| 当前余额 2000"**
- 启动按钮可点击（余额充足）

6. 点击启动 → 跑完

**预期：**
- 执行完成无错
- 回 Settings 页，`sub_remain` 下降（e.g. 2000 → 1850）
- 如果用 admin DB 查看 `credit_reservation`：至少一条 `status='reconciled'` 记录

### Path 3：非会员购买 booster 被拒

**步骤：**
1. 换一个 free 测试账号（或把 $E2E_USERNAME reset 回 free）
2. 登录 Settings 页
3. 点击 booster 灰态卡片

**预期：** tooltip 或跳转会员购买，**不**直接进加量包订单流程

**API 直接测：**
```bash
# 先拿到 free 用户的 token
TOKEN=$(curl -s -X POST "$DEV_API_URL/v1/web/login" \
    -H "Content-Type: application/json" \
    -d "{\"username\":\"$E2E_USERNAME\",\"password\":\"$E2E_PASSWORD\"}" | jq -r '.data.token')

# 直接发 booster 订单 → 应返 403 + Membership.Required
curl -s -X POST "$DEV_API_URL/v1/orders" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"product_type":"booster"}'
```

**预期：** `{"code":"Membership.Required", ...}` 或等价 403

### Path 4：legacy_tier 老会员 SOP 零扣减

**步骤：**
1. Admin DB/UI 把测试账号改为：`user_tier='standard'`, `tier_expires=future`, `billing_mode='legacy_tier'`
2. 登录 Settings 页

**预期：**
- `CreditBalanceCard` legacy_tier 模式：**"本月已用 X/20"**（不是积分）
- `BoosterPurchaseCard` 灰态 + "老会员制暂不支持加量包"

3. 进 SOP 详情页

**预期：**
- **`SopEstimateBar` 不渲染**（legacy_tier skip_deduction 守卫）
- 启动按钮保留现有行为

4. 跑 SOP

**预期：**
- 执行成功，**无** `credit_reservation` 新记录
- `monthly_sop_runs` 从 X → X+1（数据库查看）

### Path 5：SalesRAG Chat 扣减（prod 漏洞修复验证）

**步骤：**
1. 把测试账号重置为 credits 会员（Path 2 状态）
2. 打开 SalesRAG 对话页
3. 发一条消息 → 等 LLM 响应

**预期：**
- 对话正常 streaming 返回
- 回 Settings 页，`sub_remain` 又下降（证明 SalesRAG chat 扣积分了——这是 prod 漏洞修复）
- `credit_reservation` 表新一条 `reference_type='salesrag_chat'` `status='reconciled'`

### Path 6：Admin UI 估算系数管理

**步骤：**
1. 登录 `$DEV_SITE_URL/admin`
2. 进"AI 服务管理 → 估算系数"菜单

**预期：**
- DataTable 展示种子系数（8 行：provider / model / operation / ratios / version / is_active）
- 点某行 → 历史版本 drawer 打开
- 点"新增" → modal 填字段 + 必填 ChangeReason → 提交成功

3. 进"系统工具 → 迁移工具"

**预期：**
- `MigrationsView` 根据 billing-mode-init status 显示：
  - PENDING 状态 → "待迁移 N 人" + 执行按钮启用
  - EXECUTED 状态 → 禁用按钮 + "已迁移于 YYYY-MM-DD"

---

## 3. T3.1 Langfuse Span 验证（合并做）

**步骤：**
1. Dev Langfuse UI（若有独立 URL，否则 dev 服务器 docker logs 看 langfuse 容器）
2. 跑 Path 2 的一次 SOP run + Path 5 一次 SalesRAG chat
3. 在 Langfuse trace 列表找对应 trace
4. 展开 trace，确认有以下 4 个 span：
   - `credit-estimate`（含 operation/prompt_chars/model/provider/billing_mode input + estimated_credits/sufficient/coefficient_id output）
   - `credit-reserve`（含 reservation_id/reserved_credits/idempotency_key + reserved_from_packages/sub_remain_after/booster_remain_after）
   - `credit-reconcile`（含 reservation_id/reserved/actual_cost_cents + delta/reconcile_direction/has_debt）
   - `credit-refund`（仅在失败场景产生，Path 2 正常流不会有）

**失败场景补测：** 手动让某次 SOP run 失败（如模型调用超时），观察是否产生 `credit-refund` span。

---

## 4. T2.5 Playwright E2E 执行（合并做）

**本地运行**（dev server 已部署新代码后）：

```bash
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD \
    npm run test:e2e -- credits-system 2>&1 | tee /tmp/e2e-credits.log
```

**预期：**
- Path 1-6 全 pass（6 tests，~30 秒-2 分钟）
- 如失败，看 `test-results/` 下 trace + screenshot

**注：** E2E spec 是 mock-first（page.route()），不需要 dev 后端真实数据。但 Path 4/6 涉及 billing_mode 切换——可能需要先用 admin API seed 测试用户 state。

---

## 5. gstack /qa 浏览器 QA（合并做）

```bash
# 如本地安装了 gstack
cd numind-web-v3
/qa $DEV_SITE_URL   # 自动截图 + 和 baseline 比对
```

**预期：** 无 P0 视觉/功能回归。比对 diff：
- 账户中心页新增 CreditBalanceCard + BoosterPurchaseCard（**新增不是回归**，OK）
- SOP 运行详情页顶部新增 SopEstimateBar（同上）
- 其他页面应与 baseline 一致

---

## 6. 完成 + S6 Gate

全部 smoke test 通过后：

1. Manifest 更新 `stage` → S6 done / S7 pending
2. 发"S6 Gate 通过"信号（用户确认产品可用）
3. 进 S7：`git checkout release && git merge develop && git push` 触发 QA

## 失败回滚

如发现 P0 bug：

### 快速 rollback（code）
```bash
git -C numind-server revert HEAD~N..HEAD  # N 为 credits-system commits 数量
git push origin develop
```

### Migration rollback（如已 apply 到 dev DB）
```bash
ssh $DEV_SSH_USER@$DEV_SSH_HOST
docker exec numind-mysql-dev bash -c "mysql -uroot -p<pwd> numind_dev < /migrations/20260419_100500_init_billing_mode_values_rollback.sql"
# 反向执行 6 个 rollback SQL
```

（Migration rollback 已在 T3.3 本地演练验证通过）

---

## 问题反馈

发现的任何问题：
- P0（阻塞上 prod）→ 立即报给主 AI，rollback
- P1（可 tolerate 但要修）→ 登记 tech debt，计划下 sprint 修
- P2（小瑕疵）→ 记录在 manifest deferred 列表

---

**Runbook version：** 2026-04-19 credits-system initial
**Spec reference：** `docs/superpowers/specs/2026-04-18-credits-system-design.md`
**Plan reference：** `docs/superpowers/plans/credits-system-plan.md`
