-- Feature: vision-capability-unify
-- 把"模型能不能看图"统一为单一真相源 = capability_json.input_modalities 含 "image"。
-- 配套代码 (T1) 改 capability 投影：AcceptsImageInline 从 input_modalities 派生，
-- 不再读 accepts_image_inline。配套 (T2) 删 admin schema capabilities enum 的 vision。
--
-- ⚠⚠ 部署时序硬约束（S3 review P0）：本 migration 的 (a)(b) 必须【先于 T1 代码上线】执行，
--    然后重启服务清 5min capability 缓存，再部署 T1 新镜像。否则只有 accepts_image_inline
--    信号、input_modalities 无 image 的视觉模型（seed 的 qwen-vl / dev 手设的 claude-gpt）
--    会在 T1 容器启动瞬间被判"不能看图"。
--    安全顺序：① 旧容器 DB 跑 (a)(b)（旧代码读 accepts_image_inline，加 input_modalities 无影响，向后兼容）
--             → ② 重启服务清缓存 → ③ 部署 T1 镜像（fresh cache 读已回填的 input_modalities）
--             → ④ dev 验收 → ⑤ 跑 (c) 清理。
-- 手工 SSH 执行（dev/prod 不自动跑 migration）。幂等：所有 UPDATE 带 WHERE 守卫，可重跑。

-- ════════════════════════════════════════════════════════════════════════════
-- (a) 回填 input_modalities —— 把真相搬进唯一真相源（保 text 等既有模态不丢）
--     视觉信号 = accepts_image_inline=true OR features.vision=true OR capabilities 含 vision
--     （三信号 OR，缺一不可：不同模型用了不同信号；accepts_image_inline 尤其不能丢）
-- ════════════════════════════════════════════════════════════════════════════

-- (a1) 有视觉信号但 input_modalities 缺失/为空 → 置 ["text","image"]
UPDATE ai_service
SET capability_json = JSON_SET(IFNULL(capability_json, '{}'), '$.input_modalities', JSON_ARRAY('text', 'image'))
WHERE deprecated_at IS NULL
  AND capability_json IS NOT NULL AND JSON_VALID(capability_json)
  AND ( JSON_CONTAINS(capability_json, 'true', '$.accepts_image_inline') = 1
     OR JSON_CONTAINS(capability_json, 'true', '$.features.vision') = 1
     OR JSON_CONTAINS(capability_json, '"vision"', '$.capabilities') = 1 )
  AND ( JSON_EXTRACT(capability_json, '$.input_modalities') IS NULL
     OR JSON_LENGTH(JSON_EXTRACT(capability_json, '$.input_modalities')) = 0 );

-- (a2) 有视觉信号且 input_modalities 非空但不含 image → 追加 image（保留 text/audio 等既有项）
UPDATE ai_service
SET capability_json = JSON_ARRAY_APPEND(capability_json, '$.input_modalities', 'image')
WHERE deprecated_at IS NULL
  AND capability_json IS NOT NULL AND JSON_VALID(capability_json)
  AND ( JSON_CONTAINS(capability_json, 'true', '$.accepts_image_inline') = 1
     OR JSON_CONTAINS(capability_json, 'true', '$.features.vision') = 1
     OR JSON_CONTAINS(capability_json, '"vision"', '$.capabilities') = 1 )
  AND JSON_EXTRACT(capability_json, '$.input_modalities') IS NOT NULL
  AND JSON_LENGTH(JSON_EXTRACT(capability_json, '$.input_modalities')) > 0
  AND JSON_CONTAINS(capability_json, '"image"', '$.input_modalities') = 0;

-- ════════════════════════════════════════════════════════════════════════════
-- (b) task_profile.requirements 去掉 features 数组里的 "vision"
--     识图任务靠 input_modalities=[text,image] 路由即可；features.vision 是冗余/死条件
--     （ai_service 侧多数视觉模型本就无 features.vision，该校验当前已被 direct binding 绕过）。
--     仅改确实含 vision 的行（防御性 WHERE，避免 no-op 歧义；attachment.vision_describe
--     requirements 为空对象，自动跳过）。
-- ════════════════════════════════════════════════════════════════════════════
UPDATE task_profile
SET requirements = JSON_REMOVE(requirements,
      JSON_UNQUOTE(JSON_SEARCH(requirements, 'one', 'vision', NULL, '$.features')))
WHERE requirements IS NOT NULL AND JSON_VALID(requirements)
  AND JSON_CONTAINS(requirements, '"vision"', '$.features') = 1;

-- ════════════════════════════════════════════════════════════════════════════
-- (c) 数据清理（回填+验收后再跑；与 schema enum 删 vision 配套，防 admin 表单出现
--     不可取消的 unknown chip）。代码已不读 capabilities.vision / accepts_image_inline，
--     删除纯属数据卫生。
-- ════════════════════════════════════════════════════════════════════════════

-- (c1) 删模型 capability_json 里 capabilities 数组的 "vision"
UPDATE ai_service
SET capability_json = JSON_REMOVE(capability_json,
      JSON_UNQUOTE(JSON_SEARCH(capability_json, 'one', 'vision', NULL, '$.capabilities')))
WHERE deprecated_at IS NULL
  AND capability_json IS NOT NULL AND JSON_VALID(capability_json)
  AND JSON_CONTAINS(capability_json, '"vision"', '$.capabilities') = 1;

-- (c2) 可选数据卫生（代码已不读，留着也无害；如需彻底清理可取消注释）：
--   删 features.vision：
--     UPDATE ai_service SET capability_json = JSON_REMOVE(capability_json, '$.features.vision')
--       WHERE deprecated_at IS NULL AND JSON_CONTAINS_PATH(capability_json,'one','$.features.vision');
--   删退役的 accepts_image_inline / 死字段：
--     UPDATE ai_service SET capability_json = JSON_REMOVE(capability_json,
--       '$.accepts_image_inline','$.max_inline_size_bytes','$.supports_vision_tool_calling','$.preferred_image_format')
--       WHERE deprecated_at IS NULL AND JSON_CONTAINS_PATH(capability_json,'one','$.accepts_image_inline');
