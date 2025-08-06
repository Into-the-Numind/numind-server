-- 为cards表添加rendered_image字段
-- 用于存储渲染后的卡片图片URL

ALTER TABLE cards ADD COLUMN rendered_image VARCHAR(255) COMMENT '渲染后的卡片图片URL';

-- 添加索引以提高查询性能（可选）
-- CREATE INDEX idx_cards_rendered_image ON cards(rendered_image); 