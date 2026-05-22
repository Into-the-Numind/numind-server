# Prod Migration Runbook — agent-mode v1.0-final + co-shipped hotfixes

> **Scope:** All forward-direction migrations dated **2026-05-20 .. 2026-05-22** that have NOT yet been applied to prod. Prod (`129.28.125.51:9095`) currently runs `release-no-agent-v2.1.32` and has none of these schema changes.
>
> **Operator:** Human only. Autopilot AI may PROVIDE this runbook but never runs `mysql` against prod or invokes `/deploy-prod` itself.
>
> **Companion docs:**
> - [deploy-checklist-feature-14.md](deploy-checklist-feature-14.md) — original feature-14 checklist (subset of this; this runbook supersedes it and adds the 2 hotfix migrations + MySQL-syntax fixes)
> - [go-live-checklist.md](go-live-checklist.md) — outer wrapper (decisions, timing, code freeze, post-deploy)
> - [runbook.md](runbook.md) — incident response if something goes wrong post-deploy

---

## §0 Pre-flight (do these BEFORE the deploy window)

### 0.1 Confirm dev/qa have run the same migrations cleanly

```bash
# Dev has them — wave1-dev-deploy-report.md confirms agent_* + tool_* + skill_template are populated.
# If qa is part of your pipeline, do the same dance there first.
```

### 0.2 Verify prod MySQL version

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "mysql -u <db_user> -p<db_pass> -e 'SELECT VERSION();'"
```

**Why this matters:** Two of the migrations below use `IF NOT EXISTS` on `ALTER TABLE ADD COLUMN` / `CREATE INDEX`, which is **not standard MySQL syntax**. It works on MariaDB and on some MySQL forks but **fails on stock MySQL 8.4** (dev hit this on 2026-05-22, see wave1 report §2). If prod runs the same MySQL 8.x line, use the **plain-form** rewrites in §2 for migrations #2 and #17.

### 0.3 Full mysqldump backup

```bash
# On the prod DB host (not the app host — check whether they're co-located)
ssh prod-db-host  # or via deploy machine
mysqldump -u <db_user> -p<db_pass> \
  --single-transaction \
  --routines --triggers --events \
  --set-gtid-purged=OFF \
  <db_name> | gzip > /backup/numind-prod-pre-agent-mode-$(date +%Y%m%d-%H%M%S).sql.gz

# Verify the file is readable + non-trivial
ls -lh /backup/numind-prod-pre-agent-mode-*.sql.gz
zcat /backup/numind-prod-pre-agent-mode-*.sql.gz | head -50  # should show CREATE TABLE statements
```

- [ ] Backup file size sanity: matches recent prod backup sizes (±10%)
- [ ] Backup decompresses cleanly
- [ ] Backup is in a location that survives the deploy window (separate disk / offsite)
- [ ] Restore procedure rehearsed at least once on a staging clone within the last quarter

### 0.4 Sanity-check what migrations are NEEDED on prod

Run these probes — if a table/column already exists, skip that migration (rare but possible if a prior emergency hotfix touched the same schema):

```sql
-- Probe each top-level table; expect 0 rows for every line below.
SELECT 'agent_run',                COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_run'
UNION ALL SELECT 'agent_definition',         COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_definition'
UNION ALL SELECT 'agent_definition_history', COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_definition_history'
UNION ALL SELECT 'agent_sandbox_session',    COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_sandbox_session'
UNION ALL SELECT 'agent_session_memory',     COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_session_memory'
UNION ALL SELECT 'agent_permission_config',  COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_permission_config'
UNION ALL SELECT 'compliance_rule',          COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='compliance_rule'
UNION ALL SELECT 'compliance_audit_log',     COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='compliance_audit_log'
UNION ALL SELECT 'user_global_memory',       COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='user_global_memory'
UNION ALL SELECT 'tool_definition',          COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='tool_definition'
UNION ALL SELECT 'tool_factory_registry',    COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='tool_factory_registry'
UNION ALL SELECT 'skill_template',           COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='skill_template'
UNION ALL SELECT 'credit_admin_test_grant',  COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='credit_admin_test_grant';
```

Any unexpected `1`s → STOP and investigate before proceeding.

---

## §1 The 19 forward migrations (sorted by execution order)

The deploy window applies migrations in this exact order. Skip rows marked **DEV-ONLY**.

| # | File | Type | Apply to prod? | Plain-form needed? |
|---|------|------|----------------|--------------------|
| 1  | `20260520_120000_create_agent_run_table.sql` | CREATE | YES | — |
| 2  | `20260520_180000_add_b2b_billing_indexes.sql` | CREATE INDEX | YES | **§2.A** — `IF NOT EXISTS` |
| 3  | `20260521_010000_alter_agent_run_add_compact_columns.sql` | ALTER | YES | — |
| 4  | `20260521_120000_agent_mode_compliance_3layer.sql` | CREATE | YES | — |
| 5  | `20260521_120000_agent_permission_pipeline.sql` | CREATE | YES | — |
| 6  | `20260521_120000_create_agent_session_memory.sql` | CREATE | YES | — |
| 7  | `20260521_120000_create_tool_definition_and_factory_registry.sql` | CREATE | YES | — |
| 8  | `20260521_120100_create_user_global_memory.sql` | CREATE | YES | — |
| 9  | `20260521_140000_agent_billing_source_type_admin_test.sql` | ALTER ENUM | YES | — |
| 10 | `20260521_140100_agent_run_terminal_metadata.sql` | ALTER | YES | — |
| 11 | `20260521_140200_create_credit_admin_test_grant.sql` | CREATE | YES | — |
| 12 | `20260521_180000_agent_task_profiles_seed.sql` | SEED | YES | — |
| 13 | `20260521_190000_seed_e2e_test_agent.sql` | SEED | **NO — DEV-ONLY** | — |
| 14 | `20260521_190100_seed_e2e_compliance_rule.sql` | SEED | **NO — DEV-ONLY** | — |
| 15 | `20260521_200000_agent_run_admin_cancel.sql` | ALTER | YES | — (file already uses plain ALTER) |
| 16 | `20260522_120000_create_agent_sandbox_session.sql` | CREATE | YES | — |
| 17 | `20260522_153000_add_agent_run_pending_question.sql` | ALTER | YES | **§2.B** — `IF NOT EXISTS` |
| 18 | `20260522_220000_create_agent_definition.sql` | CREATE | YES | — |
| 19 | `20260522_220100_create_agent_definition_history.sql` | CREATE | YES | — |
| 20 | `20260522_220200_create_skill_template.sql` | CREATE | YES | — |
| 21 | `20260522_220300_seed_skill_template.sql` | SEED | YES | — |

Net to prod: **19 forward migrations** (skipping #13 and #14).

> Same-timestamp tie-break (rows 4–7 all stamped `20260521_120000`): apply in **alphabetical filename order** — there are no inter-row FK dependencies, so order is moot for correctness, but pick one and stick to it for reproducibility.

---

## §2 Plain-form rewrites for MySQL 8.x

MySQL does NOT support `ADD COLUMN IF NOT EXISTS` (any version) and does NOT support `CREATE INDEX IF NOT EXISTS` (the on-disk syntax is parsed but errors at exec time on stock MySQL 8.0/8.4). Dev hit this on 2026-05-22 — wave1 report §2 has the trace. Use the rewrites below for migrations #2 and #17.

For each, run the pre-flight probe FIRST. If the probe shows the column/index already exists, **skip the ALTER/CREATE entirely** (don't run the plain form on top of an existing column — it errors out with `Duplicate column name`).

### §2.A Migration #2 — `20260520_180000_add_b2b_billing_indexes.sql`

**Pre-flight probe:**

```sql
SELECT index_name FROM information_schema.statistics
WHERE table_schema=DATABASE()
  AND table_name IN ('subscription', 'trial_grant')
  AND index_name IN ('idx_sub_source_first_started_at', 'idx_sub_source_updated_at', 'idx_tg_source_granted_at');
```

For each row this returns, **delete the corresponding CREATE INDEX from the plain form below**.

**Plain form (apply only the indexes the probe says are missing):**

```sql
CREATE INDEX `idx_sub_source_first_started_at` ON `subscription` (`source`, `first_started_at`);
CREATE INDEX `idx_sub_source_updated_at`       ON `subscription` (`source`, `updated_at`);
CREATE INDEX `idx_tg_source_granted_at`        ON `trial_grant`   (`source`, `granted_at`);
```

**Verify:**

```sql
SHOW INDEX FROM subscription WHERE Key_name LIKE 'idx_sub_source_%';   -- expect 2 rows
SHOW INDEX FROM trial_grant  WHERE Key_name = 'idx_tg_source_granted_at';  -- expect 1 row
```

### §2.B Migration #17 — `20260522_153000_add_agent_run_pending_question.sql`

**Pre-flight probe:**

```sql
SHOW COLUMNS FROM agent_run LIKE 'pending_question_json';  -- expect 0 rows on fresh prod
SHOW COLUMNS FROM agent_run LIKE 'pending_question_at';    -- expect 0 rows on fresh prod
SHOW INDEX FROM agent_run WHERE Key_name = 'idx_ar_state_pending';  -- expect 0 rows
```

**Plain form (apply only what's missing):**

```sql
ALTER TABLE `agent_run`
  ADD COLUMN `pending_question_json` JSON         NULL COMMENT 'ask_user_question YieldPayload JSON; non-null when state_reason=waiting_for_user_choice',
  ADD COLUMN `pending_question_at`   TIMESTAMP(3) NULL COMMENT 'timestamp when the question was enqueued; used for SLA/timeout tracking';

CREATE INDEX `idx_ar_state_pending` ON `agent_run` (`state_reason`, `pending_question_at`);
```

**Verify:**

```sql
SHOW COLUMNS FROM agent_run LIKE 'pending_question_%';  -- expect 2 rows
SHOW INDEX FROM agent_run WHERE Key_name = 'idx_ar_state_pending';  -- expect 1 row
```

---

## §3 Execution template (per migration)

For every migration in §1, follow this loop:

```bash
# 0. Pick the migration file
MIG="20260520_120000_create_agent_run_table.sql"

# 1. Pre-flight probe (use the validation column from §4 inline)
# (see §4 row for the migration in question)

# 2. SCP to deploy machine (or wherever the mysql client lives)
sshpass -p "$PROD_SSH_PASS" scp migrations/$MIG \
  "$PROD_SSH_USER@$PROD_SSH_HOST:/tmp/$MIG"

# 3. Apply
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
  "mysql -u <db_user> -p<db_pass> <db_name> < /tmp/$MIG"

# 4. Post-flight verify (see §4 row)

# 5. Tick the checkbox in §1, move on.
```

For migrations #2 (b2b indexes) and #17 (pending_question) use the plain-form blocks from §2 instead of the file.

---

## §4 Per-migration validation queries

Run each after the corresponding migration. Stop if any returns the unexpected value.

| # | File / patch | Validation SQL | Expected |
|---|--------------|----------------|----------|
| 1 | `create_agent_run_table` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_run';` | 1 |
| 2 | b2b_billing_indexes (plain form §2.A) | `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND index_name IN ('idx_sub_source_first_started_at','idx_sub_source_updated_at','idx_tg_source_granted_at');` | 3 |
| 3 | `alter_agent_run_add_compact_columns` | `SHOW COLUMNS FROM agent_run LIKE 'compact_state';` | 1 row |
| 4 | `agent_mode_compliance_3layer` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='compliance_rule';` | 1 |
| 5 | `agent_permission_pipeline` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_permission_config';` | 1 |
| 6 | `create_agent_session_memory` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_session_memory';` | 1 |
| 7 | `create_tool_definition_and_factory_registry` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('tool_definition','tool_factory_registry');` | 2 |
| 8 | `create_user_global_memory` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='user_global_memory';` | 1 |
| 9 | `agent_billing_source_type_admin_test` | `SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE table_schema=DATABASE() AND table_name='credit_transaction' AND column_name='source_type';` | enum value includes `'admin_test'` |
| 10 | `agent_run_terminal_metadata` | `SHOW COLUMNS FROM agent_run LIKE 'terminal_metadata';` | 1 row |
| 11 | `create_credit_admin_test_grant` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='credit_admin_test_grant';` | 1 |
| 12 | `agent_task_profiles_seed` | `SELECT COUNT(*) FROM task_profile WHERE task_id LIKE 'agent.%';` | 7 |
| 13 | seed_e2e_test_agent | **SKIPPED on prod** | — |
| 14 | seed_e2e_compliance_rule | **SKIPPED on prod** | — |
| 15 | `agent_run_admin_cancel` | `SHOW COLUMNS FROM agent_run LIKE 'cancellation_requested_at'; SHOW COLUMNS FROM agent_run LIKE 'agent_definition_id';` | 1 row each |
| 16 | `create_agent_sandbox_session` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_sandbox_session';` | 1 |
| 17 | pending_question (plain form §2.B) | `SHOW COLUMNS FROM agent_run LIKE 'pending_question_%';` | 2 rows |
| 18 | `create_agent_definition` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_definition';` | 1 |
| 19 | `create_agent_definition_history` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='agent_definition_history';` | 1 |
| 20 | `create_skill_template` | `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='skill_template';` | 1 |
| 21 | `seed_skill_template` | `SELECT COUNT(*) FROM skill_template;` | 10 |

---

## §5 Final cross-check (after all migrations applied)

```sql
-- 13 new tables exist
SELECT GROUP_CONCAT(table_name ORDER BY table_name) AS new_tables
FROM information_schema.tables
WHERE table_schema=DATABASE()
  AND table_name IN (
    'agent_run','agent_definition','agent_definition_history','agent_sandbox_session',
    'agent_session_memory','agent_permission_config','compliance_rule','compliance_audit_log',
    'user_global_memory','tool_definition','tool_factory_registry','skill_template',
    'credit_admin_test_grant'
  );
-- Expect a 13-name comma-separated list. Any missing → that CREATE failed silently.

-- agent_run has all expected agent-mode columns
SHOW COLUMNS FROM agent_run;
-- Spot-check: compact_state, terminal_metadata, cancellation_requested_at, agent_definition_id,
-- pending_question_json, pending_question_at — all present.

-- credit_transaction.source_type accepts admin_test
SELECT COLUMN_TYPE FROM information_schema.COLUMNS
WHERE table_schema=DATABASE() AND table_name='credit_transaction' AND column_name='source_type';
-- Expect enum string contains 'admin_test'.

-- task_profile seeded
SELECT COUNT(*), GROUP_CONCAT(task_id) FROM task_profile WHERE task_id LIKE 'agent.%';
-- Expect 7, comma-separated task_id list.

-- skill_template seeded
SELECT COUNT(*) FROM skill_template;
-- Expect 10.
```

If any cross-check fails → STOP, fix or rollback, do NOT proceed to deploy the binary.

---

## §6 Rollback (only if you must)

Rollback is **destructive** for the seeded data tables — `skill_template` and `task_profile` seed rows are gone after rollback. Existing prod data (users, subscriptions, sop_runs, etc.) is untouched.

Apply rollbacks **in reverse order** of the forwards (21 → 20 → ... → 1, skipping #13/#14 which were never applied):

```bash
for f in $(ls migrations/2026052*_rollback.sql | sort -r); do
  case "$f" in
    *seed_e2e_test_agent*|*seed_e2e_compliance_rule*) continue ;;
  esac
  sshpass -p "$PROD_SSH_PASS" scp "$f" "$PROD_SSH_USER@$PROD_SSH_HOST:/tmp/"
  sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" \
    "mysql -u <db_user> -p<db_pass> <db_name> < /tmp/$(basename $f)"
done
```

After rollback:

- [ ] All agent_* / tool_* / skill_template / compliance_* tables gone (cross-check §5 returns zero matching tables).
- [ ] `agent_run` table dropped (no remaining columns to clean up).
- [ ] credit_transaction.source_type enum no longer contains `admin_test` (rollback #9 removes that value).
- [ ] Deploy machine reverts: `git checkout v2.1.32` → `/deploy-prod server` + `/deploy-prod admin`; front-end reverts to v1.4.7 / v1.0.28 (or whatever the prior tag was).
- [ ] Restore the mysqldump from §0.3 if rollback SQL itself failed at any step.

---

## §7 What this runbook intentionally does NOT cover

- **Code deploy** (4 × `/deploy-prod`) — happens AFTER all 19 migrations are green; see [go-live-checklist.md](go-live-checklist.md) §3.
- **Tag creation** (`git tag v2.2.0` etc.) — see [go-live-checklist.md](go-live-checklist.md) §2.
- **Post-deploy verification** (Langfuse trace, healthz, sample agent run) — see [deploy-checklist-feature-14.md](deploy-checklist-feature-14.md) §4.
- **Incident response** if a migration deadlocks or corrupts something — see [runbook.md](runbook.md).

---

## §8 Operator sign-off

After every step in §1–§5 is green:

- [ ] Operator name + timestamp: `_______________________ @ _____________`
- [ ] Confirmation: "I have run all 19 migrations and §5 cross-check returns the expected counts."
- [ ] mysqldump backup retained at: `_______________________` (path on backup host)
- [ ] Ready to hand over to deploy step (`/deploy-prod`).

---

*Authored 2026-05-22 by Wave 2 session. Sources: deploy-checklist-feature-14.md, wave1-dev-deploy-report.md (MySQL 8.4 ALTER quirk), live `ls migrations/` inventory.*
