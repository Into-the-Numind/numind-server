# Weekly Membership Proposal

## Summary

Introduce a paid weekly membership plan alongside the existing trial and monthly plans:

- `product_type=weekly`
- `duration=7 days`
- `cycle_credits=500`
- `amount_cents=2500`
- B2B grant only, included in parent monthly reconciliation

## Product Rules

Weekly membership is a paid subscription, not a trial. It should therefore write to `subscription`, create `membership_event` rows with paid subscription event types, and participate in `credit_cycle` rather than `trial_grant`.

Rules:

- New weekly grant creates a 7-day subscription starting immediately.
- Active weekly renewal extends `expires_at` by 7 days.
- Expired monthly or weekly subscriptions can be reopened as weekly.
- Active monthly subscriptions reject weekly grant for now, avoiding mixed-plan overlap and proration.
- Booster purchase and deduction priority remain unchanged.

## Backend Design

Add subscription metadata:

- `subscription.plan_type`: defaults to `monthly`; weekly rows store `weekly`.
- `subscription.cycle_credits`: defaults to `2000`; weekly rows store `500`.

Existing monthly behavior stays backward compatible through defaults.

Add constants:

- `ProductTypeWeekly`
- `WeeklyPriceCents = 2500`
- `WeeklyDurationDays = 7`
- `WeeklyCycleCredits = 500`

Add a weekly service entrypoint:

- `GrantWeeklySubscription`
- Uses existing B2B granter validation.
- Writes `membership_event.product_type=weekly`.
- Writes `membership_event.amount_cents=2500`.
- Sets event quantity to one weekly period.

Credit cycle logic branches by subscription plan:

- Monthly: existing anchored month cycle logic and 2000 credits.
- Weekly: 7-day anchored cycle logic and 500 credits.

B2B billing logic branches by event product:

- Trial: existing trial-grant report path.
- Monthly: existing month quantity and price calculation.
- Weekly: one event equals RMB 25.

## Frontend Design

User-end customer management:

- Add a weekly tab/card to `GrantMembershipModal`.
- Weekly payload sends `{ product_type: 'weekly' }`.
- UI displays RMB 25, 7 days, 500 credits.

User-end customer billing:

- Add weekly type filter.
- Show weekly duration as 7 days and amount as RMB 25.

Admin B2B billing:

- Include weekly in type labels and CSV output.
- Show event type as opening/renewing weekly membership.

## Rollout

The change is additive and backward compatible:

- Old subscription rows default to monthly plan and 2000 credits.
- Old API clients are unaffected.
- New frontend can call old backend only after backend deploy; therefore deployment order is backend first, then frontends.

Dev deployment includes backend, user frontend, and admin frontend. Production remains out of scope for this request.
