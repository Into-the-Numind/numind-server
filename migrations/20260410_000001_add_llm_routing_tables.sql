-- LLM 模型切换与多供应商路由表

-- 1. LLM 供应商表
CREATE TABLE IF NOT EXISTS llm_provider (
    id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name         VARCHAR(50) NOT NULL UNIQUE,
    display_name VARCHAR(100) NOT NULL,
    base_url     VARCHAR(255) NOT NULL,
    api_key      VARCHAR(255) NOT NULL,
    is_active    TINYINT(1) DEFAULT 1,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2. LLM 逻辑模型表
CREATE TABLE IF NOT EXISTS llm_model (
    id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    model_key         VARCHAR(100) NOT NULL UNIQUE,
    display_name      VARCHAR(100) NOT NULL,
    is_thinking       TINYINT(1) DEFAULT 0,
    base_model_id     BIGINT UNSIGNED,
    supports_thinking TINYINT(1) DEFAULT 0,
    icon              VARCHAR(50),
    sort_order        INT DEFAULT 0,
    is_active         TINYINT(1) DEFAULT 1,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_base_model (base_model_id),
    CONSTRAINT fk_base_model FOREIGN KEY (base_model_id) REFERENCES llm_model(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 3. 模型×供应商路由映射
CREATE TABLE IF NOT EXISTS llm_model_provider (
    id                    BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    model_id              BIGINT UNSIGNED NOT NULL,
    provider_id           BIGINT UNSIGNED NOT NULL,
    provider_model_id     VARCHAR(100) NOT NULL,
    priority              INT DEFAULT 0,
    input_price_per_mtok  DECIMAL(10,4) DEFAULT 0,
    output_price_per_mtok DECIMAL(10,4) DEFAULT 0,
    is_active             TINYINT(1) DEFAULT 1,
    created_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_model_provider (model_id, provider_id),
    INDEX idx_mp_model_active (model_id, is_active, priority),
    CONSTRAINT fk_mp_model FOREIGN KEY (model_id) REFERENCES llm_model(id),
    CONSTRAINT fk_mp_provider FOREIGN KEY (provider_id) REFERENCES llm_provider(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 4. 用户模型偏好表
CREATE TABLE IF NOT EXISTS user_model_preference (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id    BIGINT UNSIGNED NOT NULL,
    feature    VARCHAR(20) NOT NULL,
    model_key  VARCHAR(100) NOT NULL,
    thinking   TINYINT(1) DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user_feature (user_id, feature)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 5. Seed: 初始模型数据（4 基础模型）
INSERT INTO llm_model (model_key, display_name, is_thinking, base_model_id, supports_thinking, icon, sort_order) VALUES
('claude-sonnet-4-6', 'Claude Sonnet 4.6', 0, NULL, 1, 'claude', 1),
('gemini-3.1-pro-preview', 'Gemini 3.1 Pro', 0, NULL, 1, 'gemini', 2),
('deepseek-v3.2', 'DeepSeek V3.2', 0, NULL, 1, 'deepseek', 3),
('gpt-5.4', 'GPT 5.4', 0, NULL, 1, 'openai', 4);

-- Thinking 变体（使用独立 UPDATE 避免 MySQL 同表子查询限制）
INSERT INTO llm_model (model_key, display_name, is_thinking, base_model_id, supports_thinking, icon, sort_order) VALUES
('claude-sonnet-4-6-thinking', 'Claude Sonnet 4.6 Thinking', 1, NULL, 0, 'claude', 11),
('gemini-3.1-pro-preview-thinking', 'Gemini 3.1 Pro Thinking', 1, NULL, 0, 'gemini', 12),
('deepseek-v3.2-thinking', 'DeepSeek V3.2 Thinking', 1, NULL, 0, 'deepseek', 13),
('gpt-5.4-thinking', 'GPT 5.4 Thinking', 1, NULL, 0, 'openai', 14);

-- 填充 thinking 变体的 base_model_id
UPDATE llm_model t
JOIN llm_model base ON base.model_key = 'claude-sonnet-4-6'
SET t.base_model_id = base.id
WHERE t.model_key = 'claude-sonnet-4-6-thinking';

UPDATE llm_model t
JOIN llm_model base ON base.model_key = 'gemini-3.1-pro-preview'
SET t.base_model_id = base.id
WHERE t.model_key = 'gemini-3.1-pro-preview-thinking';

UPDATE llm_model t
JOIN llm_model base ON base.model_key = 'deepseek-v3.2'
SET t.base_model_id = base.id
WHERE t.model_key = 'deepseek-v3.2-thinking';

UPDATE llm_model t
JOIN llm_model base ON base.model_key = 'gpt-5.4'
SET t.base_model_id = base.id
WHERE t.model_key = 'gpt-5.4-thinking';
