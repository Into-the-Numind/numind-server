-- Rollback: revert to original ENUMs. Will fail if 'refund_lost' (event_type)
-- or 'cycle' (product_type) values are already in the column; DELETE those
-- rows first or convert them to allowed values before running this rollback.
ALTER TABLE `membership_event`
  MODIFY COLUMN `event_type` ENUM(
    'trial_granted','sub_granted','sub_renewed','booster_granted','admin_calibration'
  ) NOT NULL,
  MODIFY COLUMN `product_type` ENUM('trial','monthly','booster') NOT NULL;
