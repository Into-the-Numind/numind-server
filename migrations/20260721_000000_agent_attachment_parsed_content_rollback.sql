UPDATE `agent_attachment`
SET `modality` = 'unknown'
WHERE `modality` = 'text';

ALTER TABLE `agent_attachment`
  DROP COLUMN `parsed_at`,
  DROP COLUMN `parsed_page_count`,
  DROP COLUMN `parsed_content_byte_size`,
  DROP COLUMN `parsed_content_sha256`,
  DROP COLUMN `parsed_content`;
