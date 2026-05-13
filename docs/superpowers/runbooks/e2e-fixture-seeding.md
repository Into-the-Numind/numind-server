# E2E Fixture Seeding — membership-credits-redesign

> Runbook for Wave 3 Agent H (2026-04-30). Describes the 3 child fixture accounts
> required to run `numind-web-v3/e2e/membership-credits-redesign.spec.ts`.

---

## Quick Status

As of 2026-04-30, all fixtures are already seeded on the **dev DB** (numind-mysql-dev).
The 11-test E2E suite passes 11/11.

---

## Fixture Accounts

| Env var | Username | User ID | State | Parent |
|---------|----------|---------|-------|--------|
| `CHILD_USERNAME_FREE` | `e2e_child_free` | 60 | Free — no trial, no sub, no booster | admin (id=25) |
| `CHILD_USERNAME_BOOSTER` | `e2e_child_booster` | 61 | Active trial (expires ~2026-05-03) + 1200 booster credits | admin (id=25) |
| `CHILD_USERNAME_FROZEN` | `e2e_child_frozen` | 62 | Expired trial (2000-01-01) + 800 booster credits (frozen) | admin (id=25) |

**Password**: `e2e_test_pass_2026` (plaintext in dev DB, env var `CHILD_PASSWORD`)

**Parent**: `admin` (id=25), env var `E2E_USERNAME=admin`, `E2E_PASSWORD=admin`

---

## DB State Per Table

### `user` table (all 3 accounts)

```sql
-- Common: parent_user_id = 25, billing_mode = 'credits', status = 0
-- e2e_child_free: user_tier = 'free'
-- e2e_child_booster: user_tier = 'trial'
-- e2e_child_frozen: user_tier = 'free'
```

### `trial_grant` table

| user_id | granted_at | expires_at | credits_remaining | source |
|---------|------------|------------|-------------------|--------|
| 61 (booster) | 2026-04-30T19:29:29 | 2026-05-03T19:29:29 | 200 | b2b_grant |
| 62 (frozen) | (any) | 2000-01-01T00:00:00 | 0 | b2b_grant |

`e2e_child_free` (id=60): **NO row** in trial_grant.

### `user_booster_balance` table

| user_id | credits_remaining |
|---------|-------------------|
| 61 (booster) | 1200 |
| 62 (frozen) | 800 |

`e2e_child_free` (id=60): **NO row** in user_booster_balance.

### `subscription` table

All 3 child accounts: **NO rows**. The booster account relies on active `trial_grant`,
not a subscription, to satisfy the "active member" requirement.

---

## How to Rebuild (if dev DB is reset)

SSH into dev server and run via Docker:

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
  "docker exec -i numind-mysql-dev mysql -u root -pNumind2025 numind-dev" << 'SQL'

-- Step 1: Get parent user ID (should be 25 for admin)
SELECT id FROM user WHERE username = 'admin';

-- Step 2: Create child accounts (adjust parent_user_id if admin ID differs)
INSERT INTO user (username, password, nickname, billing_mode, user_tier, parent_user_id, status)
VALUES
  ('e2e_child_free',    'e2e_test_pass_2026', 'E2E Free Child',    'credits', 'free',  25, 0),
  ('e2e_child_booster', 'e2e_test_pass_2026', 'E2E Booster Child', 'credits', 'trial', 25, 0),
  ('e2e_child_frozen',  'e2e_test_pass_2026', 'E2E Frozen Child',  'credits', 'free',  25, 0)
ON DUPLICATE KEY UPDATE
  billing_mode   = VALUES(billing_mode),
  user_tier      = VALUES(user_tier),
  parent_user_id = VALUES(parent_user_id),
  status         = VALUES(status);

-- Step 3: Seed trial_grant for booster (active) and frozen (expired)
SET @booster_id = (SELECT id FROM user WHERE username = 'e2e_child_booster');
SET @frozen_id  = (SELECT id FROM user WHERE username = 'e2e_child_frozen');

INSERT INTO trial_grant (user_id, granted_at, expires_at, credits_remaining, source, granter_user_id, created_at)
VALUES
  (@booster_id, NOW(), DATE_ADD(NOW(), INTERVAL 3 DAY), 200, 'b2b_grant', 25, NOW()),
  (@frozen_id,  '2000-01-01 00:00:00', '2000-01-01 00:00:00', 0, 'b2b_grant', 25, '2000-01-01 00:00:00')
ON DUPLICATE KEY UPDATE
  expires_at         = VALUES(expires_at),
  credits_remaining  = VALUES(credits_remaining);

-- Step 4: Seed user_booster_balance
INSERT INTO user_booster_balance (user_id, credits_remaining, updated_at)
VALUES
  (@booster_id, 1200, NOW()),
  (@frozen_id,  800,  NOW())
ON DUPLICATE KEY UPDATE
  credits_remaining = VALUES(credits_remaining),
  updated_at        = NOW();

SQL
```

---

## Environment Variables (in `.claude/settings.local.json`)

These are already set and should NOT be committed to any repository:

```json
"E2E_USERNAME":            "admin",
"E2E_PASSWORD":            "admin",
"CHILD_USERNAME_FREE":     "e2e_child_free",
"CHILD_USERNAME_BOOSTER":  "e2e_child_booster",
"CHILD_USERNAME_FROZEN":   "e2e_child_frozen",
"CHILD_PASSWORD":          "e2e_test_pass_2026",
"E2E_CHILD_FREE":          "e2e_child_free",
"E2E_CHILD_BOOSTER":       "e2e_child_booster",
"E2E_CHILD_FROZEN":        "e2e_child_frozen",
"E2E_CHILD_PASSWORD":      "e2e_test_pass_2026"
```

---

## Running the E2E Suite

```bash
cd numind-web-v3

# Ensure vite dev server proxies to dev backend
VITE_PROXY_TARGET="http://49.233.219.254:9091" npm run dev &

# Run the spec
E2E_USERNAME=admin E2E_PASSWORD=admin \
CHILD_USERNAME_FREE=e2e_child_free \
CHILD_USERNAME_BOOSTER=e2e_child_booster \
CHILD_USERNAME_FROZEN=e2e_child_frozen \
CHILD_PASSWORD=e2e_test_pass_2026 \
npx playwright test e2e/membership-credits-redesign.spec.ts --reporter=line
```

---

## Notes & Cautions

- **Dev only**: These fixture accounts must NEVER be created on prod. The usernames
  start with `e2e_` as a naming convention.
- **Password stored plaintext**: The dev DB stores passwords in plaintext (not bcrypt).
  This is an existing dev-environment characteristic, not introduced by these fixtures.
- **Booster trial expiry**: The `e2e_child_booster` trial expires ~3 days from seeding.
  If it expires, re-run the seed SQL (Step 3) to refresh `expires_at = DATE_ADD(NOW(), INTERVAL 3 DAY)`.
- **Parent visibility**: The parent (admin) sees all 3 children in `/customers` via
  `parent_user_id = 25` — no extra configuration needed.
- **spec.ts selectors**: The spec uses CSS class selectors aligned with the actual
  component implementation (`.grant-dialog`, `.membership-badge.badge--trial`, etc.).
  See the selector constants at the top of the spec file.

---

*Last updated: 2026-04-30 by Wave 3 Agent H*
