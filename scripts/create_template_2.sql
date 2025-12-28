-- 创建Template 2的SOP
-- 此脚本用于创建template id为2的SOP模板及其4个节点
-- 前提：template 1已经存在且有4个节点

-- 步骤1: 创建template 2
INSERT INTO sop_template (name, description, status, prompt, created_at, updated_at)
SELECT 
    '感悟型朋友圈创作' as name,
    description,
    'active' as status,
    '' as prompt,  -- 没有系统级提示词
    NOW() as created_at,
    NOW() as updated_at
FROM sop_template
WHERE id = 1;

-- 获取新创建的template 2的ID（假设是2，如果不是需要手动调整）
SET @template2_id = LAST_INSERT_ID();

-- 如果template 2的ID不是2，需要手动设置
-- SET @template2_id = 2;

-- 步骤2: 创建4个节点，配置与template 1相同，但名称不同
-- 节点1 (sort=0): 拆解产品
INSERT INTO sop_node (
    template_id, parent_id, name, status, base_url, model_name, api_key,
    timeout_seconds, sort, is_root, prompt, created_at, updated_at
)
SELECT 
    @template2_id as template_id,
    parent_id,
    '拆解产品' as name,
    status,
    base_url,
    model_name,
    api_key,
    timeout_seconds,
    sort,
    is_root,
    prompt,
    NOW() as created_at,
    NOW() as updated_at
FROM sop_node
WHERE template_id = 1 AND sort = 0;

-- 节点2 (sort=1): 拆解爆款朋友圈
INSERT INTO sop_node (
    template_id, parent_id, name, status, base_url, model_name, api_key,
    timeout_seconds, sort, is_root, prompt, created_at, updated_at
)
SELECT 
    @template2_id as template_id,
    parent_id,
    '拆解爆款朋友圈' as name,
    status,
    base_url,
    model_name,
    api_key,
    timeout_seconds,
    sort,
    is_root,
    prompt,
    NOW() as created_at,
    NOW() as updated_at
FROM sop_node
WHERE template_id = 1 AND sort = 1;

-- 节点3 (sort=2): 拆解语言风格
INSERT INTO sop_node (
    template_id, parent_id, name, status, base_url, model_name, api_key,
    timeout_seconds, sort, is_root, prompt, created_at, updated_at
)
SELECT 
    @template2_id as template_id,
    parent_id,
    '拆解语言风格' as name,
    status,
    base_url,
    model_name,
    api_key,
    timeout_seconds,
    sort,
    is_root,
    prompt,
    NOW() as created_at,
    NOW() as updated_at
FROM sop_node
WHERE template_id = 1 AND sort = 2;

-- 节点4 (sort=3): 仿写朋友圈
INSERT INTO sop_node (
    template_id, parent_id, name, status, base_url, model_name, api_key,
    timeout_seconds, sort, is_root, prompt, created_at, updated_at
)
SELECT 
    @template2_id as template_id,
    parent_id,
    '仿写朋友圈' as name,
    status,
    base_url,
    model_name,
    api_key,
    timeout_seconds,
    sort,
    is_root,
    prompt,
    NOW() as created_at,
    NOW() as updated_at
FROM sop_node
WHERE template_id = 1 AND sort = 3;

-- 验证：查询template 2及其节点
SELECT 'Template 2 created:' as info;
SELECT id, name, description, status, prompt FROM sop_template WHERE id = @template2_id;

SELECT 'Template 2 nodes:' as info;
SELECT id, template_id, name, sort, base_url, model_name, timeout_seconds 
FROM sop_node 
WHERE template_id = @template2_id 
ORDER BY sort;






