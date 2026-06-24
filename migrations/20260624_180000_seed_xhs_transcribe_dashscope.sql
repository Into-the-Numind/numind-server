-- Migration: 注册小红书视频转写云 ASR(DashScope Paraformer)。
-- Feature: xhs-collector 视频逐字稿。Rollback: 20260624_180000_seed_xhs_transcribe_dashscope_rollback.sql
--
-- 背景:FunASR(自部署 localhost:10095)在 dev/prod 均未部署 → xhs 视频转写跑不通。
-- 改走云 DashScope「录音文件识别」(paraformer-v2),复用 ali 的 DashScope api_key。
-- 适配器: internal/pkg/aiservice/adapter/dashscope_asr.go (Name()="dashscope-asr")。
-- 网关按 llm_provider.name 找适配器,故 provider.name 必须 = "dashscope-asr"。
-- transcribe.go 走 profile.XhsTranscribe='xhs.transcribe'(与 monitor.transcribe 隔离,不影响会议/监控)。
-- 计费在 biz 层按音频秒数 Reserve/Reconcile,不依赖 gateway pricing_rule(故无需 pricing 行)。
--
-- ⚠️ CI 不跑 migrations:dev/prod 部署后须手动 SSH 执行此 SQL。
-- 启动时 SyncProviderCredentials 会把 ai_providers.ali.api_key 同步进 dashscope-asr provider 的 api_key。

-- 1. provider: dashscope-asr (base_url 固定 DashScope 根域;api_key 留空,启动同步填充)
INSERT IGNORE INTO llm_provider (name, display_name, base_url, api_key, provider_type, supports_streaming, is_active)
VALUES ('dashscope-asr', 'DashScope 录音文件识别', 'https://dashscope.aliyuncs.com', '', 'asr', 0, 1);

-- 2. ai_service: dashscope-paraformer (asr)
INSERT IGNORE INTO ai_service (model_key, display_name, service_type, capability_json, is_active)
VALUES ('dashscope-paraformer', 'DashScope Paraformer 录音文件识别', 'asr',
  '{"audio_formats":["wav","mp3","m4a"],"max_duration_sec":3600,"languages":["zh","en"],"realtime":false,"capabilities":["asr"]}', 1);

-- 3. ai_service_route: dashscope-paraformer → dashscope-asr provider, model paraformer-v2
INSERT IGNORE INTO ai_service_route (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'paraformer-v2', 10, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'dashscope-asr'
WHERE s.model_key = 'dashscope-paraformer';

-- 4. task_profile: xhs.transcribe → 默认绑定 dashscope-paraformer
INSERT IGNORE INTO task_profile
  (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT 'xhs.transcribe', '小红书视频转写', '小红书视频笔记语音转文字(DashScope Paraformer)', 'asr',
  '{"audio_formats":["wav"],"max_duration_sec":3600}', s.id, 0
FROM ai_service s WHERE s.model_key = 'dashscope-paraformer';

-- 验证:
--   SELECT id,name,base_url,IF(api_key='','NO_KEY','has_key') FROM llm_provider WHERE name='dashscope-asr';
--   SELECT t.task_id, s.model_key, p.name prov, r.provider_model_id
--     FROM task_profile t JOIN ai_service s ON s.id=t.default_service_id
--     JOIN ai_service_route r ON r.model_id=s.id JOIN llm_provider p ON p.id=r.provider_id
--    WHERE t.task_id='xhs.transcribe';
