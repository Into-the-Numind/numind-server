-- Rollback: add_user_type_credit_multiplier
-- Reverts: 20260429_120000_add_user_type_credit_multiplier.sql
-- Date: 2026-04-29
-- Description: Drop credit_user_type_config table and remove user_type_multiplier
--              column from credit_reservation.

-- Drop table first (no FK dependencies)
DROP TABLE IF EXISTS credit_user_type_config;

-- Remove column from credit_reservation
ALTER TABLE credit_reservation
    DROP COLUMN user_type_multiplier;
