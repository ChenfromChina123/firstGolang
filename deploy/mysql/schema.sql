-- FileSync MySQL schema 初始化
-- 用于数据迁移前创建表结构（与 db.go migrate() MySQL 分支保持一致）

CREATE TABLE IF NOT EXISTS upload_sessions (
    id VARCHAR(64) PRIMARY KEY,
    filename VARCHAR(1024) NOT NULL,
    file_size BIGINT NOT NULL,
    file_hash VARCHAR(128) NOT NULL DEFAULT '',
    chunk_size BIGINT NOT NULL,
    total_chunks INT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    storage_type VARCHAR(32) NOT NULL DEFAULT 'local',
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    INDEX idx_sessions_hash (file_hash),
    INDEX idx_sessions_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS chunks (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    chunk_index INT NOT NULL,
    size BIGINT NOT NULL DEFAULT 0,
    hash VARCHAR(128) NOT NULL DEFAULT '',
    created_at VARCHAR(32) NOT NULL,
    UNIQUE KEY uk_session_chunk (session_id, chunk_index),
    INDEX idx_chunks_session (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS files (
    id VARCHAR(64) PRIMARY KEY,
    filename VARCHAR(1024) NOT NULL,
    size BIGINT NOT NULL,
    hash VARCHAR(128) NOT NULL DEFAULT '',
    storage_path VARCHAR(1024) NOT NULL,
    storage_type VARCHAR(32) NOT NULL DEFAULT 'local',
    chunk_size BIGINT NOT NULL,
    total_chunks INT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'completed',
    created_at VARCHAR(32) NOT NULL,
    updated_at VARCHAR(32) NOT NULL,
    INDEX idx_files_filename (filename(255)),
    INDEX idx_files_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
