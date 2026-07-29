# Weekly Membership Design

## Current State

Membership billing has three credit pools:

- Trial grant
- Subscription credit cycle
- Booster balance

The current subscription path assumes monthly periods and hard-codes 2000 cycle credits. Paid B2B grants are represented as `membership_event` rows and the finance report reconstructs subscription charges from those events.

## Target State

Weekly membership is represented as a subscription plan:

| Field | Value |
| --- | --- |
| Product type | `weekly` |
| Price | RMB 25 (`2500` cents) |
| Duration | 7 days |
| Credits | 500 |
| Grant source | `b2b_grant` |
| Finance report | included |

## Schema

Add two columns to `subscription`:

- `plan_type VARCHAR(20) NOT NULL DEFAULT 'monthly'`
- `cycle_credits INT NOT NULL DEFAULT 2000`

This avoids overloading `total_months_purchased` for weekly plans and preserves monthly semantics for all existing rows.

## Service Semantics

`GrantWeeklySubscription` follows existing B2B validation:

- parent cannot grant to self through this path
- active same-plan weekly subscription renews
- expired subscription reopens
- active different paid plan is rejected

Weekly renewals extend `expires_at` by 7 days from the current expiry.

## Credit Cycle Semantics

`ensureCurrentCycle` and balance aggregation derive cycle windows and grants from subscription plan metadata:

- monthly: anchored month periods, 2000 credits
- weekly: anchored 7-day periods, 500 credits

Cycle rows remain in `credit_cycle`; no new pool is introduced.

## Billing Semantics

`membership_event` remains the source for paid subscription grants.

Weekly events:

- `product_type=weekly`
- `event_type=sub_granted` or `sub_renewed`
- `amount_cents=2500`
- `quantity=1`
- `months=NULL`

B2B billing detail keeps `months=0`; readers infer the period from `product_type=weekly`.

## API Contract

`POST /v1/users/children/:child_id/grant-membership` accepts:

```json
{
  "product_type": "weekly"
}
```

Response includes the existing fields plus `days=7` for weekly responses.

## Compatibility

- Monthly grants remain unchanged.
- Trial grants remain unchanged.
- Booster purchase/deduction remains unchanged.
- Existing subscription rows default to monthly.
- Existing frontends continue to work against the expanded enum.

## Risk Controls

- Reject mixed active weekly/monthly stacking for this release.
- Keep B2B amount calculation server-side, not frontend-derived.
- Cover weekly cycle credit amount with backend tests.
- Deploy backend before frontends in Dev.
