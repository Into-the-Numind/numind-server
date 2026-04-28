-- Add credit_multiplier to pricing_rule table.
-- credit_multiplier scales the cost_cents before deducting user credits:
--   credits_charged = round(cost_yuan * credit_multiplier * 100)
-- Default 1.00 = no change from current behaviour.
-- Set < 1.00 for expensive models (e.g. 0.50 for Claude/GPT) to slow credit burn;
-- set > 1.00 to accelerate burn for models with thin margins.
ALTER TABLE pricing_rule
    ADD COLUMN credit_multiplier DECIMAL(5,2) NOT NULL DEFAULT 1.00
    COMMENT 'Credits burn-rate multiplier. 1.00 = 1:1 with cost_cents. <1 slows burn for expensive models.';
