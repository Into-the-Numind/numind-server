# S5 Verification Strategy

> S3 plan §S5 + AC matrix (PRD.md), turned into an executable runbook.

## Approach

Two layers, mirroring `feedback_review_each_stage.md` and `.claude/rules/testing.md`:

| Layer | What | Permanence |
|---|---|---|
| **Go unit + integration tests** | `tool_read_skill`, `skill_catalog`, regression invariants, factory wire | Permanent (in repo, every CI run) |
| **gstack /qa real-task validation** | 4 skills × 2 runs on dev | One-shot (no regression guard; honest declaration) |

The Go layer guards the *architecture* (read_skill registered, catalog rendered, addendum coherent). The gstack /qa layer guards the *artifact quality* (the LLM actually writes runnable python-pptx given the rewritten SKILL.md).

## Pre-S5 Gate (must pass before dispatching gstack /qa)

```bash
cd /private/tmp/wt-skill-progressive-loader-numind-server

# 1) Tests
go test ./internal/numind/biz/agent/... -count=1   # must be all green
# 2) Lint
task lint                                          # must exit 0
# 3) SKILL.md size invariant
for f in skills/*/SKILL.md; do
    sz=$(wc -c < "$f")
    test "$sz" -le 4096 || { echo "OVER 4KB: $f ($sz)"; exit 1; }
done
# 4) No invoke_skill residue anywhere
! grep -rn "invoke_skill" internal/numind/biz/agent/*.go skills/*/SKILL.md \
  || { echo "residual invoke_skill"; exit 1; }
```

If any check fails → S5 cannot start; back to S4.

## Deploy step

`ndf-done` (merges feature branch to develop in worktree-atomic fashion), then:

```bash
bash scripts/cicd/release.sh dev server
# Wait for `✅ Deploy success: numind-server-dev is healthy`.
# Confirm image tag == HEAD merge commit (per `project_tcr_100_tag_limit_silent_deploy.md`).
```

## Dev acceptance via gstack /qa

Login with `E2E_USERNAME / E2E_PASSWORD` (env vars), navigate to `$DEV_SITE_URL`, then run each of:

| # | Prompt | Expects | AC |
|---|---|---|---|
| 1 | "做一份 5 页 PPT 介绍 X 公司本周融资亮点" | a downloadable `.pptx` URL in the answer | AC-1 |
| 2 | "生成一个 Excel 周报表，含趋势图" | a downloadable `.xlsx` URL | AC-1 |
| 3 | "写一份 Word 业务方案，3 个章节" | a downloadable `.docx` URL | AC-1 |
| 4 | "把这段 markdown 转 PDF" | a downloadable `.pdf` URL | AC-1 |

Each prompt runs **twice**, target **≥7/8 success** (PRD AC-1 threshold).

For each run record:
- (a) Did the agent call `read_skill` first? (verify in Langfuse trace span list — span `tool.read_skill.execute` MUST appear)
- (b) Did the agent call `run_python` afterward? (span `tool.run_python.execute`)
- (c) Did the final answer contain a `/agent-outputs/<userID>/` URL?
- (d) Did the file open correctly when downloaded?

If (a) is missed even once → bug: skill catalog or addendum text isn't motivating enough.
If (b) succeeds but (d) fails → SKILL.md template has a bug; iterate the SKILL.md (allowed within S5 as inline fix).
If (a-d) all pass → PASS.

## Forbidden phrases check (PRD AC-2)

For each of the 8 dev runs, scan the final assistant message for any of these regression-indicator phrases:

- "PPT 技能环境有问题"
- "技能环境" + "有问题"
- "文件系统受限"
- "skill ran but returned exit code"
- `ModuleNotFoundError`

Hit count must be `0`. Any hit = failure (architecture regression).

## Langfuse audit (PRD AC-3)

For each of the 8 runs, open the corresponding trace in Langfuse and confirm:

- ≥ 1 span named `tool.read_skill.execute`
- ≥ 1 span named `tool.run_python.execute`
- **0** spans named `tool.invoke_skill.execute`

Any tracked agent run with `tool.invoke_skill.execute` indicates the old code path is somehow still alive (e.g. partial deploy) — investigate before declaring S5 PASS.

## Regression protection — honest declaration

Per `.claude/rules/testing.md`: gstack /qa is a one-shot test. The 8 manual runs above produce no Playwright spec file. **Future protection** lives in:

- The Go regression test `TestRegression_NoInvokeSkillInProgressiveLoaderSurfaces` (4 invariants pinned at the load-bearing surfaces).
- The 13 unit tests in `tool_read_skill_test.go`.
- The 6 catalog tests in `skill_catalog_test.go`.

If skill quality regresses in 3 months and the unit tests still pass, root cause is most likely a *sandbox image* change (python-pptx version, font availability) or a *model* change (LLM forgetting to call read_skill). Neither is in this feature's control surface. Investigate and file a follow-up at that time.

## Failure → S4 rollback path

If any of:
- Pre-S5 Go gate fails
- gstack /qa < 7/8 success
- Forbidden phrases hit > 0
- Langfuse audit shows `tool.invoke_skill.execute`

Then: do NOT `ndf-done` to prod-tag. Open issue, return to S4, iterate. The dev branch can stay merged in `develop` if the failure mode is purely UI-side (the deploy itself is reversible by rolling back to the previous TCR tag).

## Acceptance signoff

S5 PASS when all of these are true:
- [ ] Pre-S5 Go gate passes
- [ ] `ndf-done` succeeded (merge + push + worktree cleanup verified per `feedback_ndf_done_verify_push.md`)
- [ ] `/deploy-dev server` succeeded with image tag matching merge commit
- [ ] 4 skills × 2 runs = 8 manual prompts: ≥ 7/8 success
- [ ] 0 forbidden phrases across 8 runs
- [ ] 0 Langfuse traces with `tool.invoke_skill.execute`
- [ ] PRD AC-1 through AC-8 all checked
