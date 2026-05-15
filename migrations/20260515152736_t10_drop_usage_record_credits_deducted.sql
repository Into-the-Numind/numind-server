-- T10: drop dead column credits_deducted from usage_record.
-- Pre-T6 this was written by legacy biz/credit/credit.go:255-259 deductCreditsTxFull.
-- After T6 (merge 5695066), zero writers remain. 3274 prod rows all 0, zero readers.
ALTER TABLE usage_record DROP COLUMN credits_deducted;
