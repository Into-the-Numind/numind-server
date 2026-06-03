-- agent-mode-billing T10 (M1): pricing_rule for image_gen.
-- FORWARD-LOOKING: the current T9 billing charges a flat credit cost
-- (credit.GetEstimatedCredits("image_gen")=10) via explicit Reserve/Reconcile;
-- image_gen still uses bare HTTP (aiservice.ImageGen 收编 = T8 follow-up). This
-- row is consumed by the Billing middleware usage_record path ONLY after T8
-- routes image_gen through aiservice. price_per_call is a PLACEHOLDER.
-- TODO(运营): confirm the real ¥/image price before T8 lands.
INSERT INTO pricing_rule (service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at)
SELECT 'image_gen', 'dmxapi', 'gemini-2.5-flash-image', 'flat', 'call',
  0, 0, 0.30, 0,
  0, 0, 0.30, 0,
  1, NOW(), NOW()
WHERE NOT EXISTS (
  SELECT 1 FROM pricing_rule
  WHERE service_type='image_gen' AND provider='dmxapi' AND model='gemini-2.5-flash-image'
);
