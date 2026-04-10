-- Self-Service Config: Knowledge Base + Chatbot + SOP Template Extension

-- 知识库
CREATE TABLE IF NOT EXISTS knowledge_base (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT UNSIGNED NOT NULL,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(1024) DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL,
    INDEX idx_kb_user_id (user_id),
    INDEX idx_kb_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS knowledge_base_document (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    knowledge_base_id INT UNSIGNED NOT NULL,
    document_id INT UNSIGNED NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_kbd_kb_doc (knowledge_base_id, document_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 智能体
CREATE TABLE IF NOT EXISTS chatbot_config (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT UNSIGNED NOT NULL,
    name VARCHAR(100) NOT NULL,
    description VARCHAR(1024) DEFAULT '',
    avatar VARCHAR(500) DEFAULT '',
    system_prompt LONGTEXT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'draft',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL,
    INDEX idx_cc_user_status (user_id, status),
    INDEX idx_cc_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS chatbot_knowledge_base (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    chatbot_id INT UNSIGNED NOT NULL,
    knowledge_base_id INT UNSIGNED NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE INDEX idx_ckb_chatbot_kb (chatbot_id, knowledge_base_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 对话
CREATE TABLE IF NOT EXISTS chatbot_session (
    id INT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id INT UNSIGNED NOT NULL,
    chatbot_id INT UNSIGNED NOT NULL,
    title VARCHAR(200) DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    message_count INT NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL,
    INDEX idx_cs_user_chatbot (user_id, chatbot_id),
    INDEX idx_cs_deleted_at (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS chatbot_message (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    session_id INT UNSIGNED NOT NULL,
    user_id INT UNSIGNED NOT NULL,
    role VARCHAR(20) NOT NULL,
    content LONGTEXT,
    thinking LONGTEXT,
    trace_id VARCHAR(100) DEFAULT '',
    seq INT NOT NULL DEFAULT 0,
    prompt_tokens INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_cm_session_seq (session_id, seq),
    INDEX idx_cm_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- SOP 模板扩展（幂等）
ALTER TABLE sop_template ADD COLUMN IF NOT EXISTS creator_user_id INT UNSIGNED DEFAULT NULL;
-- publish_status: 2 态 draft/published（offline 已废弃，与 draft 等价合并）
-- 老 admin 创建的模板（无 publish_status 概念）默认视为 published，保留历史可见性
ALTER TABLE sop_template ADD COLUMN IF NOT EXISTS publish_status VARCHAR(20) NOT NULL DEFAULT 'published';
-- 回填历史空字符串/NULL（曾用 DEFAULT '' 的环境）→ published
UPDATE sop_template SET publish_status = 'published' WHERE publish_status IS NULL OR publish_status = '';
-- 兼容 offline 历史值 → draft（当前代码已不再产生 offline，但防止任何环境残留）
UPDATE sop_template SET publish_status = 'draft' WHERE publish_status = 'offline';
-- 强制 schema 一致性（对已存在旧 schema 的 dev 环境生效；fresh 环境是 no-op）
ALTER TABLE sop_template MODIFY COLUMN publish_status VARCHAR(20) NOT NULL DEFAULT 'published';
CREATE INDEX IF NOT EXISTS idx_st_creator ON sop_template (creator_user_id);

-- chatbot_config status: offline 历史值 → draft（与 SOP 对称简化为 2 态）
UPDATE chatbot_config SET status = 'draft' WHERE status = 'offline';

-- Feature permission seed
INSERT INTO user_feature_permission (parent_user_id, sub_user_id, feature_key, created_at, updated_at)
SELECT id, 0, 'self_service_config', NOW(), NOW()
FROM user WHERE parent_user_id IS NULL
ON DUPLICATE KEY UPDATE updated_at = NOW();
-- 注意：上面是示例 seed，实际需要通过 admin 手动授权给指定 B 端客户
