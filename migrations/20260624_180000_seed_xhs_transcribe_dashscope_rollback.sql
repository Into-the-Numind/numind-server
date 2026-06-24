-- Rollback: 20260624_180000_seed_xhs_transcribe_dashscope.sql
DELETE FROM task_profile WHERE task_id = 'xhs.transcribe';
DELETE r FROM ai_service_route r JOIN ai_service s ON s.id=r.model_id WHERE s.model_key='dashscope-paraformer';
DELETE FROM ai_service WHERE model_key = 'dashscope-paraformer';
DELETE FROM llm_provider WHERE name = 'dashscope-asr';
