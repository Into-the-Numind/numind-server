-- Add description field to sop_node table for B-end custom step descriptions
-- Rollback: ALTER TABLE sop_node DROP COLUMN description;

ALTER TABLE sop_node ADD COLUMN description TEXT AFTER name;
