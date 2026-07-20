-- Persist the canonical normalized attachment content so file_read can page it
-- without re-downloading and re-parsing the COS object for every tool call.

ALTER TABLE `agent_attachment`
  ADD COLUMN `parsed_content` LONGTEXT NULL
    COMMENT 'Canonical normalized UTF-8 content paged by file_read' AFTER `fallback_error`,
  ADD COLUMN `parsed_content_sha256` CHAR(71) NOT NULL DEFAULT ''
    COMMENT 'sha256:<hex> continuation token for parsed_content' AFTER `parsed_content`,
  ADD COLUMN `parsed_content_byte_size` BIGINT NOT NULL DEFAULT 0
    COMMENT 'UTF-8 byte length of parsed_content' AFTER `parsed_content_sha256`,
  ADD COLUMN `parsed_page_count` INT NOT NULL DEFAULT 0
    COMMENT 'Parser page count; 0 when unknown' AFTER `parsed_content_byte_size`,
  ADD COLUMN `parsed_at` DATETIME(3) NULL
    COMMENT 'Time canonical parsed content was persisted' AFTER `parsed_page_count`;

-- Historical successful fallbacks are immediately reusable. They may retain
-- their legacy human-readable wrapper; new rows store wrapper-free content.
UPDATE `agent_attachment`
SET `parsed_content` = `text_fallback`,
    `parsed_content_sha256` = CONCAT('sha256:', SHA2(`text_fallback`, 256)),
    `parsed_content_byte_size` = OCTET_LENGTH(`text_fallback`),
    `parsed_at` = COALESCE(`fallback_completed_at`, `created_at`)
WHERE `fallback_ready` = 1
  AND `fallback_error` IS NULL
  AND `text_fallback` IS NOT NULL
  AND `parsed_content` IS NULL;

-- Text uploads were previously marked unknown and skipped by the recovery
-- worker. Promote them so startup RecoverPending parses them once.
UPDATE `agent_attachment`
SET `modality` = 'text'
WHERE `modality` = 'unknown'
  AND (`mime_type` = 'text/plain' OR `mime_type` = 'text/markdown');
