-- Migration: seed 10 builtin skill templates
-- Feature: agent-mode-skill-system (#5/14)
-- Rollback: 20260522_220300_seed_skill_template_rollback.sql
-- NOTE: 使用 MySQL JSON_OBJECT/JSON_ARRAY 函数；SQLite 不支持，单测不跑此 SQL。

INSERT INTO skill_template (id, name, description, icon_url, category_tags, questionnaire_answers, default_tool_flags, display_order, is_active, created_at, updated_at) VALUES
(1,
 '学员爆款分析师',
 '帮你分析小红书笔记或短视频数据，快速识别爆款规律，提炼高转化选题方向，让复盘更有效率。',
 '/icons/template-01.png',
 JSON_ARRAY('小红书运营', '数据分析'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('analyze_data'),
   'q7', JSON_ARRAY('text', 'image'),
   'q8', 800,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'encouraging'
 ),
 JSON_OBJECT('code_sandbox', TRUE, 'media_processing', TRUE, 'web_search', FALSE),
 10, 1, NOW(), NOW()),

(2,
 '周度复盘报告助手',
 '帮你整理本周账号数据和运营动作，自动生成结构化复盘报告，让每周回顾更清晰、更有执行力。',
 '/icons/template-02.png',
 JSON_ARRAY('IP 孵化', '运营复盘'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('analyze_data'),
   'q7', JSON_ARRAY('text'),
   'q8', 1200,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'professional'
 ),
 JSON_OBJECT('code_sandbox', TRUE, 'media_processing', FALSE, 'web_search', FALSE),
 20, 1, NOW(), NOW()),

(3,
 '选题创意助手',
 '根据你的账号方向和受众画像，快速发散选题灵感，帮你从平台趋势和用户需求中找到高潜力内容方向。',
 '/icons/template-03.png',
 JSON_ARRAY('内容创作', '选题策划'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('generate_content'),
   'q7', JSON_ARRAY('text'),
   'q8', 600,
   'q9', 'allow_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'friendly'
 ),
 JSON_OBJECT('code_sandbox', FALSE, 'media_processing', FALSE, 'web_search', TRUE),
 30, 1, NOW(), NOW()),

(4,
 '学员问答助手（基础版）',
 '为你的学员提供 7×24 小时即时答疑服务，基于课程文档回答常见问题，减少重复解答，提升学员满意度。',
 '/icons/template-04.png',
 JSON_ARRAY('学员服务', '知识问答'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('answer_questions'),
   'q7', JSON_ARRAY('text'),
   'q8', 600,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'friendly'
 ),
 JSON_OBJECT('code_sandbox', FALSE, 'media_processing', FALSE, 'web_search', FALSE),
 40, 1, NOW(), NOW()),

(5,
 '学员问答助手（带分析）',
 '结合学员上传的作业截图和笔记，给出针对性答疑和数据点评，帮助学员更快理解和改进。',
 '/icons/template-05.png',
 JSON_ARRAY('学员服务', '数据分析'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('analyze_data', 'answer_questions'),
   'q7', JSON_ARRAY('text', 'image'),
   'q8', 1000,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'encouraging'
 ),
 JSON_OBJECT('code_sandbox', FALSE, 'media_processing', TRUE, 'web_search', FALSE),
 50, 1, NOW(), NOW()),

(6,
 '内容改写助手',
 '帮你把原始文案改写为更符合平台调性和目标受众口味的版本，支持多风格输出，提高内容质量与传播效率。',
 '/icons/template-06.png',
 JSON_ARRAY('内容创作', '文案优化'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('generate_content'),
   'q7', JSON_ARRAY('text'),
   'q8', 800,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'friendly'
 ),
 JSON_OBJECT('code_sandbox', FALSE, 'media_processing', FALSE, 'web_search', FALSE),
 60, 1, NOW(), NOW()),

(7,
 '直播切片助手',
 '帮你从直播文字记录中提炼精华片段，生成切片标题和脚本，让每场直播的内容价值最大化。',
 '/icons/template-07.png',
 JSON_ARRAY('直播运营', '内容创作'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('analyze_data', 'generate_content'),
   'q7', JSON_ARRAY('text'),
   'q8', 1200,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'professional'
 ),
 JSON_OBJECT('code_sandbox', FALSE, 'media_processing', FALSE, 'web_search', FALSE),
 70, 1, NOW(), NOW()),

(8,
 '限流诊断师',
 '上传近期发布数据，帮你分析账号是否触发限流机制，找出可能的问题维度并给出改进建议。',
 '/icons/template-08.png',
 JSON_ARRAY('数据分析', '账号诊断'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('analyze_data'),
   'q7', JSON_ARRAY('text', 'csv'),
   'q8', 1000,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'professional'
 ),
 JSON_OBJECT('code_sandbox', TRUE, 'media_processing', FALSE, 'web_search', FALSE),
 80, 1, NOW(), NOW()),

(9,
 '私域跟进助手',
 '根据学员的购买阶段和互动记录，生成个性化私信跟进话术，帮你提升私域转化率和学员留存。',
 '/icons/template-09.png',
 JSON_ARRAY('私域运营', '学员转化'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('generate_content', 'answer_questions'),
   'q7', JSON_ARRAY('text'),
   'q8', 800,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'encouraging'
 ),
 JSON_OBJECT('code_sandbox', FALSE, 'media_processing', FALSE, 'web_search', FALSE),
 90, 1, NOW(), NOW()),

(10,
 '数据汇总助手',
 '上传多份 Excel/CSV 数据，自动汇总关键指标、生成趋势对比表格，让数据整理工作提速 10 倍。',
 '/icons/template-10.png',
 JSON_ARRAY('数据分析', '运营效率'),
 JSON_OBJECT(
   'q6', JSON_ARRAY('analyze_data'),
   'q7', JSON_ARRAY('csv'),
   'q8', 1500,
   'q9', 'no_web_search',
   'q10', '',
   'q11', '这个问题超出我的能力范围，建议你去问老师',
   'q12', 'professional'
 ),
 JSON_OBJECT('code_sandbox', TRUE, 'media_processing', FALSE, 'web_search', FALSE),
 100, 1, NOW(), NOW())
;
