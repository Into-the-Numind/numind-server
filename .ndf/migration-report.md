# NDF v1 → v2 Migration Report

**Date:** 2026-05-19 19:10:13 CST
**Backup:** `build-manifest.yaml.legacy.20260519` (keep for 30 days)

## Summary

- Total features: 25
- Active (留 manifest): 23
- Archived completed: 1
- Archived cancelled: 1
- Decisions extracted to ADR: 136/227

## Size change

- Old `build-manifest.yaml`: 1031 lines
- New `.ndf/manifest.yaml`: 892 lines
- Reduction: 13.5%

## Active features (kept in manifest)

- `drop-billing-account-dead-table` (stage=H1, track=hotfix)
- `settings-credits-display-broken` (stage=H3-dev-validated, track=hotfix)
- `sop-salesrag-parent-scope` (stage=S6, track=standard)
- `credits-gateway-completion-estimate` (stage=completed, track=hotfix)
- `credits-deduct-cycle-wiring` (stage=completed, track=standard)
- `ui-error-friendly-mapping` (stage=completed, track=hotfix)
- `membership-balance-read-path` (stage=completed, track=standard)
- `chatbot-session-rename-pin` (stage=completed, track=standard)
- `sop-chatbot-visibility-scope` (stage=completed, track=standard)
- `sop-run-mobile-layout` (stage=H1, track=hotfix)
- `login-icp-footer` (stage=completed, track=hotfix)
- `sop-edit-save-button-bottom` (stage=completed, track=hotfix)
- `remove-tier-fields` (stage=backlog, track=standard)
- `linkapi-provider` (stage=H1-research, track=hotfix)
- `content-monitor` (stage=S5-paused, track=standard)
- `membership-credits-redesign` (stage=S5-prod-deferred, track=standard)
- `ai-service-deprecated-field-cleanup` (stage=backlog, track=standard)
- `pricing-charge-cost-split` (stage=S1, track=standard)
- `credits-trial-grant-bypass-fix` (stage=H3, track=hotfix)
- `legacy-system-deprecation` (stage=completed, track=standard)
- `credits-system-audit-hotfix` (stage=completed, track=hotfix)
- `salesrag-embed-dim-2048` (stage=completed, track=hotfix)
- `cicd-pipeline-migration-tcr` (stage=completed, track=hotfix)

## Archived

### 2026-04-completed.yaml (1 features)
- `admin-credit-user-type-config`

