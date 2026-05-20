# S5 Acceptance: `agent-mode-configurator-ux` (#10/14)

**Status**: placeholder — S5 阶段填入实际验证结果

**Feature ID**: `agent-mode-configurator-ux`
**S0 commit**: `7837395b`
**S1 commit**: `e4574609`
**S2 commit**: `a41cded5`
**S3 commit**: `7416ed83`
**S4 commits**: M1+M2 `71bc7e5` / M3 `92456c7` / M4 `0e56f0f` / M5a+M5b `d4194c1` / M6 `f303251` / M7 `4f1ea84` / M8 `9374187` / M11 `2d28236` / M12 `fe16f3d` / M9a (TBD) / M9b (TBD) / M10 (TBD) / M13a (TBD)
**S6 merge commit**: (TBD — by 主 session ndf-done OR 手动 merge)

---

## S5 验证策略（per S3 plan §5）

### 5.1 静态校验

| 项 | 命令 | 结果 |
|---|------|------|
| Lint | `cd numind-admin-web && npm run lint` | 0 errors, 2 baseline warnings ✅ |
| Type-check | `npm run type-check` | exit 0 ✅ |
| No direct axios import | `grep -r "import axios" src/views/agent/ src/api/agent.ts src/stores/agent.ts` | 0 hits ✅ |
| No LLM keyword leak | `grep -rE "openai\|anthropic\|dashscope\|sk-\|API_KEY" src/views/agent/ src/api/agent.ts src/stores/agent.ts` | 0 hits ✅ |
| No external UI framework | `grep -E "element-plus\|ant-design-vue\|vant\|naive-ui" package.json` | 0 new entries ✅ |

### 5.2 单测（vitest）

| Test file | # tests | Status |
|----|----|----|
| src/api/__tests__/request.spec.ts | 3 | TBD |
| src/stores/__tests__/agent.spec.ts | 13 | TBD |
| src/components/common/__tests__/CheckboxGroup.spec.ts | TBD | TBD |
| src/components/common/__tests__/NoticeBanner.spec.ts | TBD | TBD |
| src/views/agent/components/__tests__/ChipInput.spec.ts | TBD | TBD |
| src/views/agent/components/__tests__/CreditSlider.spec.ts | TBD | TBD |
| src/views/agent/components/__tests__/AvatarPicker.spec.ts | TBD | TBD |
| src/views/agent/components/__tests__/QuestionnaireForm.spec.ts | TBD | TBD |
| src/views/agent/components/__tests__/validation.spec.ts | 50 | TBD |
| src/views/agent/__tests__/AgentList.spec.ts | TBD | TBD |
| src/views/agent/__tests__/AgentBuilder.spec.ts | TBD | TBD |
| src/views/agent/__tests__/AgentHistoryTab.spec.ts | TBD | TBD |
| src/views/agent/__tests__/AgentAdvancedEdit.spec.ts | TBD | TBD |
| **Total** | **TBD** | **TBD** |

### 5.3 E2E (Playwright)

```bash
cd numind-admin-web && npm run dev    # terminal 1
BASE_URL=http://localhost:5174 \
  E2E_USERNAME=$E2E_USERNAME \
  E2E_PASSWORD=$E2E_PASSWORD \
  npm run test:e2e -- e2e/agent-*.spec.ts   # terminal 2
```

| Spec | Status |
|------|------|
| e2e/agent-template-derive.spec.ts | TBD |
| e2e/agent-scratch-create.spec.ts | TBD |
| e2e/agent-advanced-toggle.spec.ts | TBD |
| e2e/agent-history-restore.spec.ts | TBD |

### 5.4 Manual visual QA (dev 部署后)

After `/deploy-dev` 部署 dev 环境 `http://49.233.219.254:9100`：

- [ ] dev 父账户登录 → sidebar 看到 "AI 助手" + "Agent 监控"
- [ ] 创建第一个 Agent（从模板派生），弹试聊 Modal，点 [暂时跳过] 回详情
- [ ] 详情 3 Tab 切换正常
- [ ] 历史 Tab 显示 v1 "首次发布"
- [ ] 编辑改字段 → 保存 → 历史 v2 显示 changes_summary
- [ ] 历史 [恢复 v1] → ConfirmModal → 确认 → v3 出现 "从 v1 恢复"
- [ ] 高级切换：编辑 → 右下角链接 → 警示 Modal → 确认 → URL /edit 切到只读 body + NoticeBanner
- [ ] 监控页：访 /agent-monitoring → NoticeBanner + DataTable 空状态
- [ ] 下架某 agent：列表 → [下架] → ConfirmModal → agent 从列表消失
- [ ] **manual 子账户 403**：用 curl 调一次 `${DEV_API_URL}/v1/agent/skills` 用子账户 token → 应得 HTTP 403；用 sub 账户进 UI 显示 "仅父账户可配置 AI 助手"

### 5.5 0 prod 影响验证

- [ ] `git -C numind-server diff develop` 仅 docs commits（无代码改动）
- [ ] `git -C numind-admin-web log --oneline -20` 所有 commits 在 feature/agent-mode-configurator-ux 分支
- [ ] 0 git tag 新建（无 `v_*`）
- [ ] 0 /deploy-prod 调用
- [ ] `git status numind-admin-web/config_prod.yaml` 未修改（如该文件存在）

---

## Reviewer 累计统计

- S0: PASS_WITH_CONDITIONS, 2 P0 + 4 P1 + 4 P2 全修
- S1: PASS_WITH_CONDITIONS, 3 P0 + 6 P1 + 6 P2 全修
- S2: PASS_WITH_CONDITIONS, 4 P0 + 6 P1 + 6 P2 全修
- S3: PASS_WITH_CONDITIONS, 2 P0 + 3 P1 + 6 P2 全修
- 跨阶段累计：**11 P0 + 19 P1 + 22 P2 全修**

---

## 已知 v1 限制（已记录到 follow-up issues）

1. **custom_skill_body 编辑不支持** — backend `PatchRequest` 缺字段
   → `follow-ups/agent-mode-skill-system-advanced-mode-edit.md`

2. **监控后台无真实数据源** — backend 缺 `GET /v1/agent/sessions/active`
   → `follow-ups/agent-mode-skill-system-monitoring-api.md`

3. **试聊功能 toast 占位** — 依赖 #12 admin_test source_type + #14 真实 LLM

4. **Langfuse trace 跳转** — 依赖 #14 真实 trace 集成

5. **下架后无法重新上架** — backend `PatchRequest` 不接受 `is_active` 字段；
   v1 删除 = 永久隐藏；用户须从历史恢复创建新 agent（接受的 v1 trade-off）

---

## 结论

(S5 阶段填入)

**Overall verdict**: TBD
**Ready for S6 ndf-done**: TBD
