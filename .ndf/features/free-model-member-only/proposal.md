# 免费模型仅限会员、零扣费 — 提案

## §1 方案概述 [客户可见]
给"定价为 0 的模型"（如 agnes / 有数 AI）加一条专属规则，让它成为**会员权益**：

- **会员**（在期订阅 sub 或在期体验包 trial）：可以随便用这个模型，**哪怕积分为 0、哪怕积分用光，都能用，且完全不扣积分**（因为这个模型本身定价就是 0）。
- **免费用户**（既没在期订阅、也没在期体验包）：**一律不能用**这个模型，会看到"该模型仅限会员使用，请先开通会员"的提示。
- **混合场景仍然正确**：如果一次操作里既用到这个 0 价模型、又顺带用到别的收费模型（比如销售问答里中途的改写/检索环节），**收费的那部分照常扣积分，积分不够照常提示"积分不足"**。只有 0 价模型那一次调用是免费的。

效果：0 价模型变成"开通会员就能畅用"的钩子，拉动会员转化；同时不被免费用户白嫖，也不破坏现有积分计费。

## §2 报价与周期 [客户可见]
- 性质：内部产品规则调整（非对客交付），无对外报价。
- 预估工作量：约 **1 人日**（纯后端）。
- 交付：本次 session 自主推进到 **dev 部署**；prod 上线为独立授权（不在本次范围）。

## §3 技术可行性 [AI 内部]
### 现有功能复用
- **拦截点已存在**：`internal/pkg/aiservice/middleware/context_budget.go` 的 `ContextBudgetCredits` 中间件，在预扣前已拿到解析后的 `route.ServiceKey`（模型）+ `route.Provider.Name`。
- **会员判定已存在**：`membership.MembershipService.GetMembershipState()` 返回 `SubActive` / `TrialActive` + `SubExpiresAt` / `TrialExpiresAt`。
- **"跳过扣费"开关已存在**：`credit.PreCheckResult.SkipDeduction`（T1 legacy-tier 下线后恒为 false 的预留 no-op），`ReserveBudget`（`credit_service.go:481`）和中间件（`context_budget.go:806`）都已 honor 它 → 复用它表达"本次调用免费，跳过预扣"。
- **错误码已存在**：`errno.ErrMembershipRequired`（HTTP 403，已定义、当前未使用）→ 复用并 `SetMessage` 改成模型语境文案。
- **价格查询已存在**：`pricing` calculator + `pricing_rule` 表（`input_price_per_m_tok` / `output_price_per_m_tok` / `price_per_call` / tiered）。

### 技术风险与缓解
1. **trial 0 积分会员被误判为免费用户**（最关键）：现有 `TrialActive = trial!=nil && ExpiresAt.After(now) && CreditsRemaining>0`。trial 积分用光后 `TrialActive=false`。但 PRD 要求"trial 会员积分用光也能用免费模型"。
   - 缓解：本 feature 的"会员"判定**不复用 TrialActive**，改用**有效期判定**：`isMember = (sub 未过期) || (trial 未过期)`，**忽略剩余积分**。
2. **"查不到价格"被误判为免费**：`CalculateCost` 对缺失 pricing_rule 返回 error（不是 0）。
   - 缓解：`IsFreeModel` 仅在"命中 rule 且价格分量全 0"时返回 true；查不到 → false（走现状 flat-estimate + 正常预扣）。
3. **错误在 SSE 流式响应中的传播**：免费用户被拒时，错误发生在 LLM 调用前的预扣点，需保证以 403/可读消息抵达前端（流式与非流式都要）。S2 查实错误传播链。
4. **SOP `CreateRun` 粗粒度余额预检**（sop.go:385-403）在模型解析前就 `totalRemain<=0` 拦截，会挡住"0 余额会员跑全 0 价 SOP"。
   - 缓解：把粗检改为会员感知 `if !isMember && totalRemain<=0 { reject }`。副作用：0 余额会员跑"含收费节点"的 SOP 会创建 run 后在该节点失败（orphan pending run）——可接受的小代价（换取 0 价会员可用）。
5. **embed / rerank 子调用是否在积分拦截链上**（per-call 诉求的关键）：列为 S2 待查项（见 requirement.md）。

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3 — **暂不涉及**。前端 axios 拦截器对业务错误（code!=0）读 `message` 展示；403 非 401 不触发登录跳转。S2 会验证 403 ErrMembershipRequired 能正常展示文案；若发现前端把 403 也跳登录或吞消息，再追加前端 task。
- [ ] numind-admin-web

### AI 可观测性（涉及 LLM 调用）
- [x] 涉及 LLM 调用：是
- Trace 起点：不新增 trace（沿用 SOP/chatbot/salesrag 既有 `CreateTrace`）。
- Generation 点：不新增 generation。本 feature 只改"调用前的计费/权限决策"，不改 LLM 调用本身。
- 关键元数据：在 credits 的 `credit-estimate` span 上补记 `is_free_model: true` / `skip_reason: free_model_member`（便于线上排查"为什么这次没扣分"）。免费用户被拒时按 `.claude/rules/ai-service.md` §3 记一次决策可观测（不调 LLM，故记 span/trace metadata 即可，不强制 generation error）。

## §4 产品需求定义 — PRD [AI 内部]
### 用户故事
- 作为**会员（sub 或 trial 在期）**，我希望用 0 价模型时不受积分余额限制（哪怕余额为 0 / 用光），且不扣我积分，以便把 0 价模型当作会员畅用的权益。
- 作为**免费用户（无在期 sub 且无在期 trial）**，当我尝试用 0 价模型时被明确拒绝并提示开通会员，以便我知道这是会员功能。
- 作为**低余额会员**，当一次操作里混入收费模型子调用时，收费部分仍按积分扣减、不足时提示"积分不足"，以便计费保持正确。

### 验收标准（具体、可度量）
- [ ] **AC1**：会员 + 三池余额=0 + 0 价模型 → SOP run / chatbot 成功产出；事后 `credit_transaction` 无该调用扣减行、`credit_reservation` 无新行。
- [ ] **AC2**：trial 在期但 trial 积分=0（`CreditsRemaining=0`）的用户 + 0 价模型 → **可用**（验证"会员判定按有效期不按余额"）。
- [ ] **AC3**：免费用户（无 sub 且无 trial，或全部过期）+ 0 价模型 → 返回 403 `ErrMembershipRequired` + 可读中文提示；不调用 LLM、不创建可计费 reservation。
- [ ] **AC4**：任意用户 + 收费模型 → 行为与现状完全一致（余额足→正常跑并扣减；余额不足→"积分不足"拦截）。**回归不破坏**。
- [ ] **AC5**：查不到 `pricing_rule` 的模型 → 不被判为免费，走现状路径。
- [ ] **AC6**：会员 + 0 价主模型 + 收费子调用（chat 类，如 query 改写）且余额不足 → 收费子调用处提示"积分不足"（per-call 语义）。（若 S2 查实 embed/rerank 不在积分链上，则 AC6 限定为 chat 类子调用，并在 spec 显式声明范围。）
- [ ] **AC7**：以上决策矩阵有持久化 Go 单测覆盖（biz 层，mock store/pricing/membership）。

### 边界情况
- pricing_rule 缺失 / `CalculateCost` 报错 → 非免费（AC5）。
- trial 未过期但积分=0 → 算会员（AC2）。
- sub 已过期但还有 booster 余额（booster-only）→ **非会员**（用户定义仅 sub/trial；且 `BoosterFrozen` 在非会员时为 true）→ 0 价模型按免费用户拒绝。
- 0 价模型走"默认路径"（SOP 节点配置该模型、modelKey 为空，走 R2 char 预扣 `CheckAndEstimate`）也要同样生效，不只 gateway 路径。
- 并发：会员开通/到期与调用并发——以调用时刻 `GetMembershipState(now)` 为准，无需额外锁。

### 权限规则
- 用户端（user_token）所有 AI 入口生效：SOP / chatbot / salesrag / agent（凡走计费拦截点的调用）。
- 管理端 admin_test 试聊池（`PoolAdminTest`）：保持独立逻辑，不在本次范围（它走 `credit_admin_test_grant` 池，不是三池）。S2 确认不误伤。

### UI 行为规格
- 无新增页面。免费用户被拒：复用现有"业务错误 toast/提示"，展示后端 403 的 `message`（"该模型仅限会员使用，请先开通会员"）。S2 验证前端 403 展示路径无碍（不跳登录、不吞消息）。
