package store

import (
	"fmt"
	"log"
	"strings"
	"time"
)

// 本文件包含 schema migrate 与数据迁移，见 db.go 的构造入口。
// 全部方法均为 *DB 方法，随 db.go 同包编译。
// migrate 根据 dialect 执行对应的 schema
// MySQL 不支持单 Exec 多语句，需拆分；SQLite 支持
func (db *DB) migrate() error {
	var statements []string
	switch db.dialect {
	case "mysql":
		statements = []string{
			`CREATE TABLE IF NOT EXISTS upload_sessions (
				id VARCHAR(64) PRIMARY KEY,
				filename VARCHAR(1024) NOT NULL,
				file_size BIGINT NOT NULL,
				file_hash VARCHAR(128) NOT NULL DEFAULT '',
				chunk_size BIGINT NOT NULL,
				total_chunks INT NOT NULL,
				status VARCHAR(32) NOT NULL DEFAULT 'active',
				storage_type VARCHAR(32) NOT NULL DEFAULT 'local',
				upload_mode VARCHAR(16) NOT NULL DEFAULT '',
				object_key VARCHAR(1024) NOT NULL DEFAULT '',
				space_id VARCHAR(64) NOT NULL DEFAULT '',
				created_at VARCHAR(32) NOT NULL,
				updated_at VARCHAR(32) NOT NULL,
				INDEX idx_sessions_hash (file_hash),
				INDEX idx_sessions_status (status)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS chunks (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				session_id VARCHAR(64) NOT NULL,
				chunk_index INT NOT NULL,
				size BIGINT NOT NULL DEFAULT 0,
				hash VARCHAR(128) NOT NULL DEFAULT '',
				created_at VARCHAR(32) NOT NULL,
				UNIQUE KEY uk_session_chunk (session_id, chunk_index),
				INDEX idx_chunks_session (session_id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS files (
				id VARCHAR(64) PRIMARY KEY,
				filename VARCHAR(1024) NOT NULL,
				size BIGINT NOT NULL,
				hash VARCHAR(128) NOT NULL DEFAULT '',
				storage_path VARCHAR(1024) NOT NULL,
				storage_type VARCHAR(32) NOT NULL DEFAULT 'local',
				chunk_size BIGINT NOT NULL,
				total_chunks INT NOT NULL,
				status VARCHAR(32) NOT NULL DEFAULT 'completed',
				owner VARCHAR(64) NOT NULL DEFAULT '',
				created_at VARCHAR(32) NOT NULL,
			updated_at VARCHAR(32) NOT NULL,
			deleted_at VARCHAR(32) DEFAULT NULL,
			INDEX idx_files_filename (filename(255)),
			INDEX idx_files_status (status),
			INDEX idx_files_owner (owner),
			INDEX idx_files_deleted_at (deleted_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS users (
				id VARCHAR(64) PRIMARY KEY,
				username VARCHAR(64) NOT NULL,
				email VARCHAR(255) DEFAULT NULL,
				password_hash VARCHAR(255) NOT NULL,
				role VARCHAR(32) NOT NULL DEFAULT 'admin',
				status VARCHAR(32) NOT NULL DEFAULT 'active',
				created_at VARCHAR(32) NOT NULL,
				updated_at VARCHAR(32) NOT NULL,
				UNIQUE KEY uk_username (username),
				UNIQUE KEY uk_email (email)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS user_activation_tokens (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				user_id VARCHAR(64) NOT NULL,
				token VARCHAR(64) NOT NULL,
				expires_at VARCHAR(32) NOT NULL,
				created_at VARCHAR(32) NOT NULL,
				UNIQUE KEY uk_token (token),
				INDEX idx_token_user (user_id),
				INDEX idx_token_expires (expires_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS password_reset_codes (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				user_id VARCHAR(64) NOT NULL,
				email VARCHAR(255) NOT NULL,
				code VARCHAR(8) NOT NULL,
				expires_at VARCHAR(32) NOT NULL,
				used INT NOT NULL DEFAULT 0,
				created_at VARCHAR(32) NOT NULL,
				INDEX idx_reset_email (email),
				INDEX idx_reset_expires (expires_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS shares (
				id VARCHAR(64) PRIMARY KEY,
				file_id VARCHAR(64) DEFAULT NULL,
				dir_prefix VARCHAR(1024) DEFAULT NULL,
				share_type VARCHAR(16) NOT NULL,
				created_by VARCHAR(64) NOT NULL,
				space_id VARCHAR(64) DEFAULT NULL,
				created_at VARCHAR(32) NOT NULL,
				expires_at VARCHAR(32) DEFAULT NULL,
				download_count INT NOT NULL DEFAULT 0,
				max_downloads INT DEFAULT NULL,
				is_active INT NOT NULL DEFAULT 1,
				password_hash VARCHAR(128) DEFAULT NULL,
				INDEX idx_shares_file (file_id),
				INDEX idx_shares_creator (created_by)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS spaces (
				id VARCHAR(64) PRIMARY KEY,
				name VARCHAR(128) NOT NULL,
				owner VARCHAR(64) NOT NULL DEFAULT '',
				storage_type VARCHAR(32) NOT NULL DEFAULT 'local',
				created_at VARCHAR(32) NOT NULL,
				updated_at VARCHAR(32) NOT NULL,
				INDEX idx_spaces_owner (owner)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS share_downloads (
				id BIGINT AUTO_INCREMENT PRIMARY KEY,
				share_id VARCHAR(64) NOT NULL,
				visitor_id VARCHAR(64) NOT NULL,
				ip VARCHAR(64) DEFAULT NULL,
				user_agent VARCHAR(512) DEFAULT NULL,
				downloaded_at VARCHAR(32) NOT NULL,
				UNIQUE KEY uk_share_visitor (share_id, visitor_id),
				INDEX idx_dl_share (share_id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS user_settings (
				username VARCHAR(64) PRIMARY KEY,
				chunk_size BIGINT NOT NULL DEFAULT 8388608,
				concurrency INT NOT NULL DEFAULT 3,
				updated_at VARCHAR(32) NOT NULL
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS api_tokens (
				id VARCHAR(64) PRIMARY KEY,
				user_id VARCHAR(64) NOT NULL,
				username VARCHAR(64) NOT NULL DEFAULT '',
				name VARCHAR(128) NOT NULL,
				token_hash VARCHAR(64) NOT NULL,
				scopes VARCHAR(255) NOT NULL DEFAULT '',
				space_id VARCHAR(64) NOT NULL DEFAULT '',
				path_prefix VARCHAR(1024) NOT NULL DEFAULT '',
				quota_bytes BIGINT NOT NULL DEFAULT 0,
				quota_used BIGINT NOT NULL DEFAULT 0,
				created_at VARCHAR(32) NOT NULL,
				expires_at VARCHAR(32) DEFAULT NULL,
				last_used_at VARCHAR(32) DEFAULT NULL,
				revoked_at VARCHAR(32) DEFAULT NULL,
				UNIQUE KEY uk_api_token_hash (token_hash),
				 INDEX idx_api_tokens_user (user_id),
				 INDEX idx_api_tokens_revoked (revoked_at)
				) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
				`CREATE TABLE IF NOT EXISTS upload_events (
				 id BIGINT AUTO_INCREMENT PRIMARY KEY,
				 username VARCHAR(64) NOT NULL DEFAULT '',
				 filename VARCHAR(1024) NOT NULL DEFAULT '',
				 size BIGINT NOT NULL DEFAULT 0,
				 space_id VARCHAR(64) NOT NULL DEFAULT '',
				 tool VARCHAR(32) NOT NULL DEFAULT '',
				 created_at VARCHAR(32) NOT NULL,
				 INDEX idx_upload_events_user (username),
				 INDEX idx_upload_events_time (created_at)
				) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			}
	default: // sqlite 支持多语句
		statements = []string{
			`CREATE TABLE IF NOT EXISTS upload_sessions (
				id TEXT PRIMARY KEY,
				filename TEXT NOT NULL,
				file_size INTEGER NOT NULL,
				file_hash TEXT DEFAULT '',
				chunk_size INTEGER NOT NULL,
				total_chunks INTEGER NOT NULL,
				status TEXT NOT NULL DEFAULT 'active',
				storage_type TEXT NOT NULL DEFAULT 'local',
				upload_mode TEXT NOT NULL DEFAULT '',
				object_key TEXT NOT NULL DEFAULT '',
				space_id TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);

			CREATE TABLE IF NOT EXISTS chunks (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id TEXT NOT NULL,
				chunk_index INTEGER NOT NULL,
				size INTEGER NOT NULL DEFAULT 0,
				hash TEXT DEFAULT '',
				created_at TEXT NOT NULL,
				FOREIGN KEY (session_id) REFERENCES upload_sessions(id),
				UNIQUE(session_id, chunk_index)
			);

			CREATE TABLE IF NOT EXISTS files (
				id TEXT PRIMARY KEY,
				filename TEXT NOT NULL,
				size INTEGER NOT NULL,
				hash TEXT DEFAULT '',
				storage_path TEXT NOT NULL,
				storage_type TEXT NOT NULL DEFAULT 'local',
				chunk_size INTEGER NOT NULL,
				total_chunks INTEGER NOT NULL,
				status TEXT NOT NULL DEFAULT 'completed',
				owner TEXT NOT NULL DEFAULT '',
				space_id TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT DEFAULT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_files_owner ON files(owner);
		CREATE INDEX IF NOT EXISTS idx_files_space ON files(space_id);
		CREATE INDEX IF NOT EXISTS idx_files_deleted_at ON files(deleted_at);

			CREATE TABLE IF NOT EXISTS users (
				id TEXT PRIMARY KEY,
				username TEXT NOT NULL UNIQUE,
				email TEXT DEFAULT NULL,
				password_hash TEXT NOT NULL,
				role TEXT NOT NULL DEFAULT 'admin',
				status TEXT NOT NULL DEFAULT 'active',
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);

			CREATE TABLE IF NOT EXISTS user_activation_tokens (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id TEXT NOT NULL,
				token TEXT NOT NULL UNIQUE,
				expires_at TEXT NOT NULL,
				created_at TEXT NOT NULL
			);

			CREATE TABLE IF NOT EXISTS password_reset_codes (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				user_id TEXT NOT NULL,
				email TEXT NOT NULL,
				code TEXT NOT NULL,
				expires_at TEXT NOT NULL,
				used INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL
			);

			CREATE INDEX IF NOT EXISTS idx_token_user ON user_activation_tokens(user_id);
			CREATE INDEX IF NOT EXISTS idx_token_expires ON user_activation_tokens(expires_at);
			CREATE INDEX IF NOT EXISTS idx_reset_email ON password_reset_codes(email);
			CREATE INDEX IF NOT EXISTS idx_reset_expires ON password_reset_codes(expires_at);

			CREATE TABLE IF NOT EXISTS shares (
				id TEXT PRIMARY KEY,
				file_id TEXT,
				dir_prefix TEXT,
				share_type TEXT NOT NULL,
				created_by TEXT NOT NULL,
				space_id TEXT,
				created_at TEXT NOT NULL,
				expires_at TEXT,
				download_count INTEGER NOT NULL DEFAULT 0,
				max_downloads INTEGER,
				is_active INTEGER NOT NULL DEFAULT 1,
				password_hash TEXT
			);

			CREATE TABLE IF NOT EXISTS spaces (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				owner TEXT NOT NULL DEFAULT '',
				storage_type TEXT NOT NULL DEFAULT 'local',
				created_at TEXT NOT NULL,
				updated_at TEXT NOT NULL
			);

			CREATE INDEX IF NOT EXISTS idx_spaces_owner ON spaces(owner);

			CREATE TABLE IF NOT EXISTS share_downloads (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				share_id TEXT NOT NULL,
				visitor_id TEXT NOT NULL,
				ip TEXT,
				user_agent TEXT,
				downloaded_at TEXT NOT NULL,
				UNIQUE(share_id, visitor_id)
			);

			CREATE TABLE IF NOT EXISTS user_settings (
				username TEXT PRIMARY KEY,
				chunk_size INTEGER NOT NULL DEFAULT 8388608,
				concurrency INTEGER NOT NULL DEFAULT 3,
				updated_at TEXT NOT NULL
			);

			CREATE INDEX IF NOT EXISTS idx_shares_file ON shares(file_id);
			CREATE INDEX IF NOT EXISTS idx_shares_creator ON shares(created_by);
			CREATE INDEX IF NOT EXISTS idx_dl_share ON share_downloads(share_id);

			CREATE TABLE IF NOT EXISTS api_tokens (
				id TEXT PRIMARY KEY,
				user_id TEXT NOT NULL,
				username TEXT NOT NULL DEFAULT '',
				name TEXT NOT NULL,
				token_hash TEXT NOT NULL UNIQUE,
				scopes TEXT NOT NULL DEFAULT '',
				space_id TEXT NOT NULL DEFAULT '',
				path_prefix TEXT NOT NULL DEFAULT '',
				quota_bytes INTEGER NOT NULL DEFAULT 0,
				quota_used INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL,
				expires_at TEXT,
				last_used_at TEXT,
				revoked_at TEXT
			);

			CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);
			CREATE INDEX IF NOT EXISTS idx_api_tokens_revoked ON api_tokens(revoked_at);

			CREATE TABLE IF NOT EXISTS upload_events (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				username TEXT NOT NULL DEFAULT '',
				filename TEXT NOT NULL DEFAULT '',
				size INTEGER NOT NULL DEFAULT 0,
				space_id TEXT NOT NULL DEFAULT '',
				tool TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_upload_events_user ON upload_events(username);
			CREATE INDEX IF NOT EXISTS idx_upload_events_time ON upload_events(created_at);`,
		}
	}
	for _, stmt := range statements {
		if _, err := db.conn.Exec(stmt); err != nil {
			return err
		}
	}

	// 增量迁移：为已存在的 users 表追加 email/status 列（旧库升级路径）
	if err := db.migrateUsersAddColumns(); err != nil {
		return fmt.Errorf("migrate users columns: %w", err)
	}

	// 增量迁移：为已存在的 files 表追加 owner 列（用户隔离基础）
	if err := db.migrateFilesAddOwner(); err != nil {
		return fmt.Errorf("migrate files owner: %w", err)
	}

	// 增量迁移：为 files.hash 列创建索引（秒传检查加速）
	if err := db.migrateFilesAddHashIndex(); err != nil {
		return fmt.Errorf("migrate files hash index: %w", err)
	}

	// 增量迁移：为 files 表追加 deleted_at 列（回收站软删除）
	if err := db.migrateFilesAddDeletedAt(); err != nil {
		return fmt.Errorf("migrate files deleted_at: %w", err)
	}

	// 增量迁移：为 shares 表追加 password_hash 列（分享密码保护）
	if err := db.migrateSharesAddPassword(); err != nil {
		return fmt.Errorf("migrate shares password_hash: %w", err)
	}

	// 增量迁移：为 upload_sessions 表追加 upload_mode/object_key 列（presigned 直连）
	if err := db.migrateUploadSessionsAddPresignedCols(); err != nil {
		return fmt.Errorf("migrate upload_sessions presigned cols: %w", err)
	}

	// 增量迁移：为 files 表追加 space_id 列（多空间隔离）
	if err := db.migrateFilesAddSpaceID(); err != nil {
		return fmt.Errorf("migrate files space_id: %w", err)
	}
	// 增量迁移：为 upload_sessions 表追加 space_id 列（上传归属空间）
	if err := db.migrateUploadSessionsAddSpaceID(); err != nil {
		return fmt.Errorf("migrate upload_sessions space_id: %w", err)
	}
	// 增量迁移：为 shares 表追加 space_id 列（分享所属空间）
	if err := db.migrateSharesAddSpaceID(); err != nil {
		return fmt.Errorf("migrate shares space_id: %w", err)
	}
	// 为已有用户确保默认空间记录存在（升级路径）
	if err := db.ensureDefaultSpaces(); err != nil {
		return fmt.Errorf("ensure default spaces: %w", err)
	}
	// 数据迁移：现有文件归入所属用户的「我的空间」（space_id='default-<owner>'）
	// 升级兼容：引入多空间前创建的文件 space_id=''，为保证升级后仍能在默认空间看到，
	// 统一迁移到用户的默认空间；回收站文件一并迁移（恢复后仍在原空间）。
	if err := db.migrateAssignDefaultSpace(); err != nil {
		return fmt.Errorf("migrate assign default space: %w", err)
	}
	// 增量迁移：oauth_clients 追加 owner_username 列（client_credentials grant 用）
	if err := db.migrateOAuthClientsAddOwner(); err != nil {
		return fmt.Errorf("migrate oauth_clients owner_username: %w", err)
	}

	return nil
}

// SpaceAll 表示不过滤空间（admin 全局视图）。db 层空间过滤语义：
//   - spaceID == SpaceAll ("*")：不过滤，返回所有空间的记录（admin/CLI 兼容）
//   - spaceID == ""：仅默认空间（space_id = ”）
//   - spaceID == "<id>"：仅指定空间
const SpaceAll = "*"

// spaceFilter 返回 space_id 过滤 SQL 片段与参数。
func spaceFilter(spaceID string) (string, []interface{}) {
	if spaceID == SpaceAll {
		return "", nil
	}
	if spaceID == "" {
		return " AND space_id = ''", nil
	}
	return " AND space_id = ?", []interface{}{spaceID}
}

// migrateUsersAddColumns 检测 users 表是否缺少 email/status 列，缺少则 ALTER TABLE ADD COLUMN。
// 兼容 SQLite 和 MySQL：CREATE TABLE IF NOT EXISTS 不会修改已存在表，需要单独追加。
func (db *DB) migrateUsersAddColumns() error {
	// 查询 users 表当前所有列名
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'users' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(users)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasEmail := false
	hasStatus := false
	for _, c := range columns {
		if c == "email" {
			hasEmail = true
		}
		if c == "status" {
			hasStatus = true
		}
	}

	if !hasEmail {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE users ADD COLUMN email VARCHAR(255) DEFAULT NULL`
		} else {
			stmt = `ALTER TABLE users ADD COLUMN email TEXT DEFAULT NULL`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add email column: %w", err)
		}
		log.Printf("[DB] users table: added 'email' column")
	}
	if !hasStatus {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE users ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT 'active'`
		} else {
			stmt = `ALTER TABLE users ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add status column: %w", err)
		}
		log.Printf("[DB] users table: added 'status' column")
	}

	// MySQL 需显式创建 email 唯一索引（SQLite 在 CREATE TABLE 中已声明）
	if db.dialect == "mysql" && !hasEmail {
		_, err := db.conn.Exec(`CREATE UNIQUE INDEX idx_users_email ON users(email)`)
		if err != nil {
			log.Printf("[DB] WARNING: create idx_users_email failed (may already exist): %v", err)
		}
	}

	return nil
}

// migrateFilesAddOwner 检测 files 表是否缺少 owner 列，缺少则 ALTER TABLE ADD COLUMN。
// 兼容 SQLite 和 MySQL 旧库升级，用于实现用户级文件隔离。
func (db *DB) migrateFilesAddOwner() error {
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'files' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(files)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasOwner := false
	for _, c := range columns {
		if c == "owner" {
			hasOwner = true
			break
		}
	}

	if !hasOwner {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE files ADD COLUMN owner VARCHAR(64) NOT NULL DEFAULT ''`
		} else {
			stmt = `ALTER TABLE files ADD COLUMN owner TEXT NOT NULL DEFAULT ''`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add owner column: %w", err)
		}
		log.Printf("[DB] files table: added 'owner' column")
	}

	// MySQL 需显式创建索引（SQLite 在 CREATE TABLE 后已用 CREATE INDEX IF NOT EXISTS 创建）
	if db.dialect == "mysql" && !hasOwner {
		_, err := db.conn.Exec(`CREATE INDEX idx_files_owner ON files(owner)`)
		if err != nil {
			log.Printf("[DB] WARNING: create idx_files_owner failed (may already exist): %v", err)
		}
	}

	return nil
}

// migrateFilesAddHashIndex 为 files.hash 列创建索引（秒传检查加速）。
// 兼容 SQLite 和 MySQL：SQLite 用 CREATE INDEX IF NOT EXISTS，MySQL 先检测再创建。
// 索引可将秒传检查从全表扫描降为索引查找。
func (db *DB) migrateFilesAddHashIndex() error {
	switch db.dialect {
	case "mysql":
		// 检测索引是否已存在
		var cnt int
		err := db.conn.QueryRow(
			`SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
			 WHERE TABLE_NAME = 'files' AND TABLE_SCHEMA = DATABASE() AND INDEX_NAME = 'idx_files_hash'`).Scan(&cnt)
		if err != nil {
			return fmt.Errorf("check idx_files_hash: %w", err)
		}
		if cnt == 0 {
			if _, err := db.conn.Exec(`CREATE INDEX idx_files_hash ON files(hash)`); err != nil {
				log.Printf("[DB] WARNING: create idx_files_hash failed: %v", err)
			} else {
				log.Printf("[DB] files table: added 'idx_files_hash' index")
			}
		}
	default:
		// SQLite 支持 IF NOT EXISTS
		if _, err := db.conn.Exec(`CREATE INDEX IF NOT EXISTS idx_files_hash ON files(hash)`); err != nil {
			log.Printf("[DB] WARNING: create idx_files_hash failed: %v", err)
		}
	}
	return nil
}

// migrateFilesAddDeletedAt 检测 files 表是否缺少 deleted_at 列，缺少则 ALTER TABLE ADD COLUMN。
// 兼容 SQLite 和 MySQL 旧库升级，用于实现回收站软删除功能。
// deleted_at 为 NULL 表示文件正常；非 NULL 表示文件已移入回收站（值为删除时间 ISO 8601）。
func (db *DB) migrateFilesAddDeletedAt() error {
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'files' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(files)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasDeletedAt := false
	for _, c := range columns {
		if c == "deleted_at" {
			hasDeletedAt = true
			break
		}
	}

	if !hasDeletedAt {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE files ADD COLUMN deleted_at VARCHAR(32) DEFAULT NULL`
		} else {
			stmt = `ALTER TABLE files ADD COLUMN deleted_at TEXT DEFAULT NULL`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add deleted_at column: %w", err)
		}
		log.Printf("[DB] files table: added 'deleted_at' column (trash feature)")
	}

	// 为 deleted_at 创建索引（回收站列表查询加速）
	switch db.dialect {
	case "mysql":
		var cnt int
		err := db.conn.QueryRow(
			`SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
			 WHERE TABLE_NAME = 'files' AND TABLE_SCHEMA = DATABASE() AND INDEX_NAME = 'idx_files_deleted_at'`).Scan(&cnt)
		if err != nil {
			return fmt.Errorf("check idx_files_deleted_at: %w", err)
		}
		if cnt == 0 {
			if _, err := db.conn.Exec(`CREATE INDEX idx_files_deleted_at ON files(deleted_at)`); err != nil {
				log.Printf("[DB] WARNING: create idx_files_deleted_at failed: %v", err)
			} else {
				log.Printf("[DB] files table: added 'idx_files_deleted_at' index")
			}
		}
	default:
		if _, err := db.conn.Exec(`CREATE INDEX IF NOT EXISTS idx_files_deleted_at ON files(deleted_at)`); err != nil {
			log.Printf("[DB] WARNING: create idx_files_deleted_at failed: %v", err)
		}
	}

	return nil
}

// migrateSharesAddPassword 检测 shares 表是否缺少 password_hash 列，缺少则 ALTER TABLE ADD COLUMN。
// 兼容 SQLite 和 MySQL 旧库升级，用于实现分享密码保护功能。
// password_hash 为 NULL 表示无密码；非 NULL 为 bcrypt 哈希值。
func (db *DB) migrateSharesAddPassword() error {
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'shares' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(shares)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasPasswordHash := false
	for _, c := range columns {
		if c == "password_hash" {
			hasPasswordHash = true
			break
		}
	}

	if !hasPasswordHash {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE shares ADD COLUMN password_hash VARCHAR(128) DEFAULT NULL`
		} else {
			stmt = `ALTER TABLE shares ADD COLUMN password_hash TEXT`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add password_hash column: %w", err)
		}
		log.Printf("[DB] shares table: added 'password_hash' column (share password feature)")
	}

	return nil
}

// migrateUploadSessionsAddPresignedCols 检测 upload_sessions 表是否缺少
// upload_mode/object_key 列，缺少则 ALTER TABLE ADD COLUMN。
// 用于 presigned URL 直连 OSS 功能（记录上传模式和最终对象键）。
// 兼容 SQLite 和 MySQL 旧库升级。
func (db *DB) migrateUploadSessionsAddPresignedCols() error {
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'upload_sessions' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(upload_sessions)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasUploadMode := false
	hasObjectKey := false
	for _, c := range columns {
		if c == "upload_mode" {
			hasUploadMode = true
		}
		if c == "object_key" {
			hasObjectKey = true
		}
	}

	if !hasUploadMode {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE upload_sessions ADD COLUMN upload_mode VARCHAR(16) NOT NULL DEFAULT ''`
		} else {
			stmt = `ALTER TABLE upload_sessions ADD COLUMN upload_mode TEXT NOT NULL DEFAULT ''`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add upload_mode column: %w", err)
		}
		log.Printf("[DB] upload_sessions table: added 'upload_mode' column (presigned upload feature)")
	}
	if !hasObjectKey {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE upload_sessions ADD COLUMN object_key VARCHAR(1024) NOT NULL DEFAULT ''`
		} else {
			stmt = `ALTER TABLE upload_sessions ADD COLUMN object_key TEXT NOT NULL DEFAULT ''`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add object_key column: %w", err)
		}
		log.Printf("[DB] upload_sessions table: added 'object_key' column (presigned upload feature)")
	}

	return nil
}

// migrateFilesAddSpaceID 检测 files 表是否缺少 space_id 列，缺少则 ALTER TABLE ADD COLUMN。
// space_id 用于多空间隔离：空串表示默认空间，非空为空间 ID。
func (db *DB) migrateFilesAddSpaceID() error {
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'files' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(files)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasSpace := false
	for _, c := range columns {
		if c == "space_id" {
			hasSpace = true
			break
		}
	}
	if !hasSpace {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE files ADD COLUMN space_id VARCHAR(64) NOT NULL DEFAULT ''`
		} else {
			stmt = `ALTER TABLE files ADD COLUMN space_id TEXT NOT NULL DEFAULT ''`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add files.space_id column: %w", err)
		}
		log.Printf("[DB] files table: added 'space_id' column (space feature)")
	}
	if db.dialect == "mysql" && !hasSpace {
		if _, err := db.conn.Exec(`CREATE INDEX idx_files_space ON files(space_id)`); err != nil {
			log.Printf("[DB] WARNING: create idx_files_space failed: %v", err)
		}
	}
	return nil
}

// migrateUploadSessionsAddSpaceID 为 upload_sessions 表追加 space_id 列（上传归属空间）。
func (db *DB) migrateUploadSessionsAddSpaceID() error {
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'upload_sessions' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(upload_sessions)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasSpace := false
	for _, c := range columns {
		if c == "space_id" {
			hasSpace = true
			break
		}
	}
	if !hasSpace {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE upload_sessions ADD COLUMN space_id VARCHAR(64) NOT NULL DEFAULT ''`
		} else {
			stmt = `ALTER TABLE upload_sessions ADD COLUMN space_id TEXT NOT NULL DEFAULT ''`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add upload_sessions.space_id column: %w", err)
		}
		log.Printf("[DB] upload_sessions table: added 'space_id' column (space feature)")
	}
	return nil
}

// migrateSharesAddSpaceID 为 shares 表追加 space_id 列（分享所属空间）。
func (db *DB) migrateSharesAddSpaceID() error {
	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'shares' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(shares)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}

	hasSpace := false
	for _, c := range columns {
		if c == "space_id" {
			hasSpace = true
			break
		}
	}
	if !hasSpace {
		var stmt string
		if db.dialect == "mysql" {
			stmt = `ALTER TABLE shares ADD COLUMN space_id VARCHAR(64) DEFAULT NULL`
		} else {
			stmt = `ALTER TABLE shares ADD COLUMN space_id TEXT`
		}
		if _, err := db.conn.Exec(stmt); err != nil {
			return fmt.Errorf("add shares.space_id column: %w", err)
		}
		log.Printf("[DB] shares table: added 'space_id' column (space feature)")
	}
	return nil
}

// ensureDefaultSpaces 为已有用户确保存在「我的空间」记录（升级路径）。
// 空间功能引入前已有文件归属 space_id=”（默认空间），
// 为用户补充一条 id="default-<username>"、name="我的空间" 的记录，前端列表可见。
func (db *DB) ensureDefaultSpaces() error {
	users, err := db.ListUsers()
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	now := time.Now().Format(time.RFC3339)
	for _, u := range users {
		id := "default-" + u.Username
		prefix := db.insertIgnorePrefix()
		if _, err := db.conn.Exec(
			prefix+` INTO spaces (id, name, owner, storage_type, created_at, updated_at)
			 VALUES (?, ?, ?, 'local', ?, ?)`,
			id, "我的空间", u.Username, now, now,
		); err != nil {
			log.Printf("[DB] WARNING: ensure default space for %s failed: %v", u.Username, err)
		}
	}
	return nil
}

//   - 历史公共文件（owner=”）：保持 ”（仅 admin 全局视图可见，不归属任何用户）
//   - 回收站文件（deleted_at 非空）一并迁移：恢复后仍在原空间
//
// 幂等：仅处理 space_id=” 的记录，迁移后不会再次命中。
func (db *DB) migrateAssignDefaultSpace() error {
	var stmt string
	if db.dialect == "mysql" {
		stmt = `UPDATE files SET space_id = CONCAT('default-', owner) WHERE space_id = '' AND owner != ''`
	} else {
		stmt = `UPDATE files SET space_id = 'default-' || owner WHERE space_id = '' AND owner != ''`
	}
	res, err := db.conn.Exec(stmt)
	if err != nil {
		return fmt.Errorf("assign default space to legacy files: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected > 0 {
		log.Printf("[DB] 数据迁移：%d 个历史文件已归入所属用户的「我的空间」 (space_id='default-<owner>')", affected)
	}
	return nil
}
