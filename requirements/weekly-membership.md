# Weekly Membership Requirement

## Background

Current B2B2C membership grants support trial and monthly subscriptions. Parents can open membership for child accounts from customer management, and finance reconciles those grants from the B2B billing report.

The product now needs a shorter paid plan:

- Product: weekly membership
- Duration: 7 days
- Price: RMB 25
- Credits: 500
- Grant path: parent grants to child from customer management
- Billing path: included in monthly B2B billing reconciliation

## Problem

The existing subscription model assumes monthly cycles:

- Backend constants only expose `trial`, `monthly`, and `booster`.
- `subscription` stores months and monthly credit-cycle assumptions.
- Credit cycle creation grants 2000 credits per monthly cycle.
- B2B billing computes paid subscription amount from purchased months.
- User frontend grant modal and billing view only understand trial/monthly.
- Admin billing report only labels trial/monthly.

Adding a weekly product only in the frontend would create incorrect credit grants and finance totals. Adding it only in backend would leave operators unable to grant and audit it clearly.

## Scope

In scope:

- Add `weekly` as a paid membership product type.
- Create a 7-day subscription grant path with 500 cycle credits and RMB 25 event amount.
- Store enough subscription metadata to derive weekly cycle bounds and credits.
- Include weekly grant events in B2B billing reports.
- Add weekly option to the user-end customer-management grant modal.
- Add weekly labels and filters to user-end and admin billing records.
- Add regression coverage for backend subscription, credit balance/cycle, controller, and billing behavior.

Out of scope:

- C-end self-purchase of weekly membership.
- Payment gateway integration.
- Proration or conversion between monthly and weekly plans.
- Production deployment.

## Acceptance Criteria

- Parent can grant weekly membership to a child through `POST /v1/users/children/:child_id/grant-membership` with `product_type=weekly`.
- Weekly grant creates or reopens a subscription expiring 7 days from grant time.
- Weekly renewal extends an active weekly subscription by 7 days.
- Active monthly subscriptions cannot be stacked with weekly subscriptions in this release.
- Weekly subscription balance grants 500 credits for the active weekly cycle.
- B2B billing report includes weekly grant/renew events with `amount_cents=2500`.
- User customer-management grant modal shows weekly membership with RMB 25, 7 days, and 500 credits.
- User and admin billing record views display weekly membership clearly.
- Backend lint and tests pass; changed Vue repos pass lint and type-check.
- Dev backend, user frontend, and admin frontend are deployed after merge.
