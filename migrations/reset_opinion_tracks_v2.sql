-- 重置所有观点赛道的 doc_id，触发下次启动时重新 Seed + 向量化
UPDATE opinion_track SET doc_id = 0 WHERE deleted_at IS NULL;
