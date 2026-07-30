# Prod SELECT-only preflight — 2026-07-30

## Result

PASS.

The `scripts/2026-07-30-prod-schema-reconcile/00-preflight.sql` script was executed against the real Prod MySQL database using SELECT-only statements. No Prod tables, rows, config values, or customer data were changed.

## Environment

- Time: `2026-07-30T18:09:31+0800`
- Database container: `numind-mysql-prod`
- Database: `numind-prod`
- MySQL version observed by preflight: `8.4.2`

## Product meaning

Prod is structurally ready for the additive Dev-to-Prod schema package:

- notification center schema checks passed;
- document system target tables are absent and upgradeable;
- Feishu personal connection target tables are absent and upgradeable;
- Qwen 3.5 Flash routing/pricing config is absent and upgradeable;
- subscription backfill target columns are absent and upgradeable;
- protected customer, credits, subscription, SOP, chatbot, and sales tables produced evidence projections/checksums.

## Important evidence

- `required_base_tables`: PASS, observed `20`
- `subscription_plan_type_column_count`: PASS, observed `0`
- `subscription_cycle_credits_column_count`: PASS, observed `0`
- `feishu_table_count`: PASS, observed `0`
- `qwen35_service_key_count`: PASS, observed `0`
- `attachment_vision_task_key_count`: PASS, observed `0`
- `qwen35_pricing_key_count`: PASS, observed `0`
- `orphan_*` notification/survey checks: PASS, observed `0`
- `subscription_protected_projection`: row_count `102`, sha256 `a49eab0a594e92838c1c15311b870a3b3b77dc118571811cea629d7f08e413e8`
- Protected core table checksums were emitted for user, trial_grant, credit_account, credit_cycle, booster, membership, reservation, SOP, chatbot, and sales tables.

## Next gate

Before any Prod write:

1. take a recoverable Prod database backup;
2. record backup path, size, and SHA256;
3. restore the backup into an isolated MySQL 8 instance and verify core table row counts;
4. then request final authorization for migration apply + prod deployment.
