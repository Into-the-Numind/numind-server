CREATE TABLE IF NOT EXISTS agent_session_memory (
  id                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id                    INT UNSIGNED NOT NULL                   COMMENT 'FK 到 user.id；与 #5 parent_user_id 类型对齐',
  agent_definition_id        BIGINT UNSIGNED NOT NULL                COMMENT 'FK 到 agent_definition.id',
  kind                       VARCHAR(20) NOT NULL                    COMMENT 'summary/learning/decision/issue/fact/preference',
  content                    TEXT NOT NULL                           COMMENT 'memory 内容，写入前 html.EscapeString 转义',
  embedding                  LONGBLOB NULL                           COMMENT 'v1 NULL；v2 swap 真实向量',
  score                      FLOAT NOT NULL DEFAULT 1.0              COMMENT 'BM25/向量融合分缓存',
  source_type                VARCHAR(20) NOT NULL DEFAULT 'agent'    COMMENT 'agent/user_explicit/agent_tool',
  source_agent_definition_id BIGINT UNSIGNED NULL                   COMMENT '写入者 agent；与 agent_definition_id 可能不同',
  recency_at                 DATETIME NOT NULL                       COMMENT '最近被引用时刻；recency boost 用',
  expires_at                 DATETIME NULL                           COMMENT 'TTL；NULL=永久；v1 默认 created_at+90d',
  created_at                 DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at                 DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_asm_recency (user_id, agent_definition_id, recency_at)
);
