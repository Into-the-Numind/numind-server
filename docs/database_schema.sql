-- 微信小程序图片处理数据库结构
-- 用户上传图片 -> AI处理成卡片 -> 多个卡片组成卡册

-- 用户表
CREATE TABLE users (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    openid VARCHAR(50) UNIQUE NOT NULL,
    phone VARCHAR(20) INDEX,
    nickname VARCHAR(100),
    avatar_url VARCHAR(255),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    is_active BOOLEAN DEFAULT TRUE,
    is_pro BOOLEAN DEFAULT FALSE,
    book_num INT DEFAULT 0,
    
    -- 管理员字段
    username VARCHAR(50) UNIQUE,
    password VARCHAR(255),
    is_admin BOOLEAN DEFAULT FALSE,
    status INT DEFAULT 0,
    last_login TIMESTAMP NULL,
    
    INDEX idx_openid (openid),
    INDEX idx_phone (phone),
    INDEX idx_username (username)
);

-- 图片表 (用户上传的原始图片)
CREATE TABLE images (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    original_url VARCHAR(512) NOT NULL,
    thumb_url VARCHAR(512),
    file_name VARCHAR(255) NOT NULL,
    file_size BIGINT NOT NULL,
    mime_type VARCHAR(100),
    width INT,
    height INT,
    status VARCHAR(20) DEFAULT 'uploaded',
    process_msg VARCHAR(255),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    
    INDEX idx_user_id (user_id),
    INDEX idx_status (status),
    INDEX idx_created_at (created_at),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- 卡册表
CREATE TABLE books (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    title VARCHAR(255) NOT NULL,
    description TEXT,
    cover_url VARCHAR(512),
    category VARCHAR(100),
    tags VARCHAR(255),
    status VARCHAR(20) DEFAULT 'draft',
    is_public BOOLEAN DEFAULT FALSE,
    card_count INT DEFAULT 0,
    view_count INT DEFAULT 0,
    like_count INT DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    
    INDEX idx_user_id (user_id),
    INDEX idx_status (status),
    INDEX idx_category (category),
    INDEX idx_created_at (created_at),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- 卡片表 (AI处理后的卡片)
CREATE TABLE cards (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    book_id BIGINT UNSIGNED,
    image_id BIGINT UNSIGNED NOT NULL,
    title VARCHAR(255),
    content TEXT,
    ocr_text TEXT,
    processed_text TEXT,
    card_type VARCHAR(50) DEFAULT 'text',
    status VARCHAR(20) DEFAULT 'processing',
    sort_order INT DEFAULT 0,
    tags VARCHAR(255),
    source VARCHAR(255),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    
    INDEX idx_user_id (user_id),
    INDEX idx_book_id (book_id),
    INDEX idx_image_id (image_id),
    INDEX idx_status (status),
    INDEX idx_card_type (card_type),
    INDEX idx_sort_order (sort_order),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE SET NULL,
    FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE CASCADE
);

-- AI处理任务表
CREATE TABLE processing_tasks (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT UNSIGNED NOT NULL,
    image_id BIGINT UNSIGNED NOT NULL,
    book_id BIGINT UNSIGNED,
    task_type VARCHAR(50) NOT NULL,
    status VARCHAR(20) DEFAULT 'pending',
    progress INT DEFAULT 0,
    result TEXT,
    error_msg VARCHAR(500),
    started_at TIMESTAMP NULL,
    completed_at TIMESTAMP NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    
    INDEX idx_user_id (user_id),
    INDEX idx_image_id (image_id),
    INDEX idx_book_id (book_id),
    INDEX idx_task_type (task_type),
    INDEX idx_status (status),
    INDEX idx_created_at (created_at),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE CASCADE,
    FOREIGN KEY (book_id) REFERENCES books(id) ON DELETE SET NULL
);

-- 卡片图片关联表 (如果需要一张卡片对应多张图片)
CREATE TABLE card_rel_image (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    card_id BIGINT UNSIGNED NOT NULL,
    url VARCHAR(512) NOT NULL,
    ocr_text TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    
    INDEX idx_card_id (card_id),
    FOREIGN KEY (card_id) REFERENCES cards(id) ON DELETE CASCADE
);

-- 触发器：更新卡册的卡片数量
DELIMITER //
CREATE TRIGGER update_book_card_count_insert
AFTER INSERT ON cards
FOR EACH ROW
BEGIN
    IF NEW.book_id IS NOT NULL THEN
        UPDATE books SET card_count = card_count + 1 WHERE id = NEW.book_id;
    END IF;
END//

CREATE TRIGGER update_book_card_count_delete
AFTER DELETE ON cards
FOR EACH ROW
BEGIN
    IF OLD.book_id IS NOT NULL THEN
        UPDATE books SET card_count = card_count - 1 WHERE id = OLD.book_id;
    END IF;
END//

CREATE TRIGGER update_book_card_count_update
AFTER UPDATE ON cards
FOR EACH ROW
BEGIN
    IF OLD.book_id IS NOT NULL AND NEW.book_id IS NULL THEN
        UPDATE books SET card_count = card_count - 1 WHERE id = OLD.book_id;
    ELSEIF OLD.book_id IS NULL AND NEW.book_id IS NOT NULL THEN
        UPDATE books SET card_count = card_count + 1 WHERE id = NEW.book_id;
    ELSEIF OLD.book_id IS NOT NULL AND NEW.book_id IS NOT NULL AND OLD.book_id != NEW.book_id THEN
        UPDATE books SET card_count = card_count - 1 WHERE id = OLD.book_id;
        UPDATE books SET card_count = card_count + 1 WHERE id = NEW.book_id;
    END IF;
END//
DELIMITER ; 