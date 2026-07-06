package store

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"filesync/internal/model"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

// DB wraps SQL database (SQLite or MySQL) for metadata persistence
type DB struct {
	conn      *sql.DB
	asyncQ    *AsyncWriteQueue // async batch write channel (nil if not enabled)
	asyncOnce sync.Once
	dialect   string // "sqlite" | "mysql"
}

// writeJob is a single job in the async write queue.
type writeJob struct {
	kind  string // "session" | "chunk" | "file" | "update_status" | "file_and_status"
	sid   string // session ID (for chunk/status)
	idx   int    // chunk index (for chunk)
	size  int64  // chunk size
	hash  string // chunk/file hash
	s     *model.UploadSession
	f     *model.FileRecord
}

// AsyncWriteQueue batches SQLite writes from a buffered channel.
// A single background goroutine consumes jobs and writes them in transactions,
// bypassing the SetMaxOpenConns(1) bottleneck at the HTTP handler level.
type AsyncWriteQueue struct {
	ch      chan writeJob
	db      *sql.DB
	dialect string // "sqlite" | "mysql"，用于切换 INSERT OR IGNORE / INSERT IGNORE
	ticker  *time.Ticker
	closed  chan struct{}
}

const asyncQSize = 2000

// EnableAsyncWrite starts the async write queue.
// Must be called once before using Async* methods.
func (db *DB) EnableAsyncWrite() {
	db.asyncOnce.Do(func() {
		db.asyncQ = &AsyncWriteQueue{
			ch:      make(chan writeJob, asyncQSize),
			db:      db.conn,
			dialect: db.dialect,
			ticker:  time.NewTicker(10 * time.Millisecond),
			closed:  make(chan struct{}),
		}
		go db.asyncQ.run()
		log.Printf("[AsyncWrite] Batch write queue enabled (dialect=%s, capacity=%d, interval=10ms, batch=500)", db.dialect, asyncQSize)
	})
}

// insertIgnorePrefix 返回 dialect 对应的 INSERT IGNORE 前缀
// SQLite: "INSERT OR IGNORE"  MySQL: "INSERT IGNORE"
func (q *AsyncWriteQueue) insertIgnorePrefix() string {
	if q.dialect == "mysql" {
		return "INSERT IGNORE"
	}
	return "INSERT OR IGNORE"
}

// replaceIntoPrefix 返回 dialect 对应的 REPLACE 前缀
// SQLite: "INSERT OR REPLACE"  MySQL: "REPLACE"
func (db *DB) replaceIntoPrefix() string {
	if db.dialect == "mysql" {
		return "REPLACE"
	}
	return "INSERT OR REPLACE"
}

// insertIgnorePrefix 返回 dialect 对应的 INSERT IGNORE 前缀
// SQLite: "INSERT OR IGNORE"  MySQL: "INSERT IGNORE"
// 用于利用 UNIQUE 约束实现幂等插入（如分享下载去重）
func (db *DB) insertIgnorePrefix() string {
	if db.dialect == "mysql" {
		return "INSERT IGNORE"
	}
	return "INSERT OR IGNORE"
}

// Close shuts down the async queue and the DB connection.
func (db *DB) Close() error {
	if db.asyncQ != nil {
		db.asyncQ.ticker.Stop()
		close(db.asyncQ.closed)
	}
	return db.conn.Close()
}

func (q *AsyncWriteQueue) run() {
	batch := make([]writeJob, 0, 500)
	for {
		select {
		case <-q.closed:
			// Flush remaining jobs
			if len(batch) > 0 {
				q.flush(batch)
			}
			return
		case job := <-q.ch:
			batch = append(batch, job)
			// Drain channel up to 500 more without blocking
			for len(batch) < 500 {
				select {
				case j := <-q.ch:
					batch = append(batch, j)
				default:
					goto flush
				}
			}
		flush:
			q.flush(batch)
			batch = batch[:0]
		case <-q.ticker.C:
			if len(batch) > 0 {
				q.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (q *AsyncWriteQueue) flush(jobs []writeJob) {
	if len(jobs) == 0 {
		return
	}
	tx, err := q.db.Begin()
	if err != nil {
		log.Printf("[AsyncSQLite] Begin tx error: %v (retrying %d jobs sync)", err, len(jobs))
		// Fallback: retry each job synchronously outside a transaction
		for _, j := range jobs {
			q.execSync(j)
		}
		return
	}
	success := true
	for _, j := range jobs {
		var execErr error
		switch j.kind {
		case "session":
			_, execErr = tx.Exec(
				q.insertIgnorePrefix()+` INTO upload_sessions (id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				j.s.ID, j.s.Filename, j.s.FileSize, j.s.FileHash,
				j.s.ChunkSize, j.s.TotalChunks, j.s.Status, j.s.StorageType,
				j.s.CreatedAt.Format(time.RFC3339), j.s.UpdatedAt.Format(time.RFC3339),
			)
		case "chunk":
			_, execErr = tx.Exec(
				q.insertIgnorePrefix()+` INTO chunks (session_id, chunk_index, size, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
				j.sid, j.idx, j.size, j.hash, time.Now().Format(time.RFC3339),
			)
		case "file":
			_, execErr = tx.Exec(
				q.insertIgnorePrefix()+` INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				j.f.ID, j.f.Filename, j.f.Size, j.f.Hash, j.f.StoragePath, j.f.StorageType,
				j.f.ChunkSize, j.f.TotalChunks, j.f.Status,
				j.f.CreatedAt.Format(time.RFC3339), j.f.UpdatedAt.Format(time.RFC3339),
			)
		case "update_status":
			_, execErr = tx.Exec(
				`UPDATE upload_sessions SET status = 'completed', updated_at = ? WHERE id = ?`,
				time.Now().Format(time.RFC3339), j.sid,
			)
		case "file_and_status":
			_, execErr = tx.Exec(
				q.insertIgnorePrefix()+` INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				j.f.ID, j.f.Filename, j.f.Size, j.f.Hash, j.f.StoragePath, j.f.StorageType,
				j.f.ChunkSize, j.f.TotalChunks, j.f.Status,
				j.f.CreatedAt.Format(time.RFC3339), j.f.UpdatedAt.Format(time.RFC3339),
			)
			if execErr == nil {
				_, execErr = tx.Exec(
					`UPDATE upload_sessions SET status = 'completed', updated_at = ? WHERE id = ?`,
					time.Now().Format(time.RFC3339), j.sid,
				)
			}
		}
		if execErr != nil {
			log.Printf("[AsyncSQLite] job %s error: %v (will retry sync)", j.kind, execErr)
			tx.Rollback()
			// Retry this failed job synchronously
			q.execSync(j)
			success = false
			break
		}
	}
	if success {
		if err := tx.Commit(); err != nil {
			log.Printf("[AsyncSQLite] Commit error: %v (retrying %d jobs sync)", err, len(jobs))
			tx.Rollback()
			for _, j := range jobs {
				q.execSync(j)
			}
		} else if len(jobs) > 10 {
			log.Printf("[AsyncSQLite] Flushed %d jobs", len(jobs))
		}
	}
}

// execSync executes a single writeJob synchronously (fallback after tx failure).
func (q *AsyncWriteQueue) execSync(j writeJob) {
	var err error
	switch j.kind {
	case "session":
		_, err = q.db.Exec(
			q.insertIgnorePrefix()+` INTO upload_sessions (id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.s.ID, j.s.Filename, j.s.FileSize, j.s.FileHash,
			j.s.ChunkSize, j.s.TotalChunks, j.s.Status, j.s.StorageType,
			j.s.CreatedAt.Format(time.RFC3339), j.s.UpdatedAt.Format(time.RFC3339),
		)
	case "chunk":
		_, err = q.db.Exec(
			q.insertIgnorePrefix()+` INTO chunks (session_id, chunk_index, size, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			j.sid, j.idx, j.size, j.hash, time.Now().Format(time.RFC3339),
		)
	case "file":
		_, err = q.db.Exec(
			q.insertIgnorePrefix()+` INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.f.ID, j.f.Filename, j.f.Size, j.f.Hash, j.f.StoragePath, j.f.StorageType,
			j.f.ChunkSize, j.f.TotalChunks, j.f.Status,
			j.f.CreatedAt.Format(time.RFC3339), j.f.UpdatedAt.Format(time.RFC3339),
		)
	case "update_status":
		_, err = q.db.Exec(
			`UPDATE upload_sessions SET status = 'completed', updated_at = ? WHERE id = ?`,
			time.Now().Format(time.RFC3339), j.sid,
		)
	case "file_and_status":
		_, err = q.db.Exec(
			q.insertIgnorePrefix()+` INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.f.ID, j.f.Filename, j.f.Size, j.f.Hash, j.f.StoragePath, j.f.StorageType,
			j.f.ChunkSize, j.f.TotalChunks, j.f.Status,
			j.f.CreatedAt.Format(time.RFC3339), j.f.UpdatedAt.Format(time.RFC3339),
		)
		if err == nil {
			_, err = q.db.Exec(
				`UPDATE upload_sessions SET status = 'completed', updated_at = ? WHERE id = ?`,
				time.Now().Format(time.RFC3339), j.sid,
			)
		}
	}
	if err != nil {
		log.Printf("[AsyncSQLite] execSync error (%s): %v", j.kind, err)
	}
}

// AsyncCreateSession enqueues a session for batch write.
func (db *DB) AsyncCreateSession(session *model.UploadSession) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "session", s: session}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Dropping session %s", session.ID)
		}
	} else {
		db.CreateUploadSession(session)
	}
}

// AsyncSaveChunk enqueues a chunk record for batch write.
func (db *DB) AsyncSaveChunk(sessionID string, chunkIndex int, size int64, hash string) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "chunk", sid: sessionID, idx: chunkIndex, size: size, hash: hash}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Dropping chunk %s/%d", sessionID, chunkIndex)
		}
	} else {
		db.SaveChunk(sessionID, chunkIndex, size, hash)
	}
}

// AsyncCreateFile enqueues a file record for batch write.
func (db *DB) AsyncCreateFile(f *model.FileRecord) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "file", f: f}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Dropping file %s", f.ID)
		}
	} else {
		db.CreateFile(f)
	}
}

// AsyncUpdateStatus enqueues a session status update for batch write.
func (db *DB) AsyncUpdateStatus(sessionID string) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "update_status", sid: sessionID}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Dropping status update %s", sessionID)
		}
	}
}

// AsyncCreateFileAndStatus atomically enqueues a file record + session status update.
// These two operations are always written together in the same transaction, ensuring consistency.
func (db *DB) AsyncCreateFileAndStatus(f *model.FileRecord, sessionID string) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "file_and_status", f: f, sid: sessionID}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Dropping file_and_status %s/%s", f.ID, sessionID)
		}
	} else {
		db.CreateFile(f)
		db.UpdateUploadSessionStatus(sessionID, "completed")
	}
}

// New opens or creates the SQLite database.
//
// 关键 PRAGMA 配置：
//   - synchronous=NORMAL：事务提交不再 fsync 等待，崩溃时只丢最近事务
//   - cache_size=-65536：64MB 内存缓存，提升查询命中率
//   - busy_timeout=5000：5s 锁等待，避免 SQLITE_BUSY 错误
//   - 注意：modernc.org/sqlite（纯 Go）不启用 WAL/mmap，单连接下 WAL 无收益
func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite doesn't support concurrent writes well

	// PRAGMA 必须在 migrate() 之前执行
	pragmas := []string{
		`PRAGMA synchronous=NORMAL;`,
		`PRAGMA cache_size=-65536;`,
		`PRAGMA busy_timeout=5000;`,
	}
	for _, p := range pragmas {
		if _, err := conn.Exec(p); err != nil {
			conn.Close()
			return nil, fmt.Errorf("exec %s: %w", p, err)
		}
	}

	db := &DB{conn: conn, dialect: "sqlite"}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// NewMySQL opens or creates a MySQL database connection.
// dsn 格式：filesync:password@tcp(127.0.0.1:13306)/filesync?parseTime=true&loc=Local&charset=utf8mb4
// MySQL 支持并发写入，无需 SetMaxOpenConns(1) 限制。
func NewMySQL(dsn string) (*DB, error) {
	conn, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	// MySQL 支持并发，配置合理的连接池
	conn.SetMaxOpenConns(20)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)

	// 验证连接
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	db := &DB{conn: conn, dialect: "mysql"}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	log.Printf("[MySQL] connected, dialect=mysql, dsn=%s", maskDSN(dsn))
	return db, nil
}

// maskDSN 脱敏 DSN 中的密码，仅用于日志输出
func maskDSN(dsn string) string {
	// 简单脱敏：把 :password@ 替换为 :***@
	for i := 0; i < len(dsn)-1; i++ {
		if dsn[i] == ':' && i+1 < len(dsn) {
			if j := indexOf(dsn, '@', i+1); j > 0 {
				return dsn[:i+1] + "***" + dsn[j:]
			}
		}
	}
	return dsn
}

// indexOf 返回字符 c 在 s 中从 start 位置开始查找的索引，找不到返回 -1
func indexOf(s string, c byte, start int) int {
	for i := start; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

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
				created_at VARCHAR(32) NOT NULL,
				expires_at VARCHAR(32) DEFAULT NULL,
				download_count INT NOT NULL DEFAULT 0,
				max_downloads INT DEFAULT NULL,
				is_active INT NOT NULL DEFAULT 1,
				password_hash VARCHAR(128) DEFAULT NULL,
				INDEX idx_shares_file (file_id),
				INDEX idx_shares_creator (created_by)
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
				created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT DEFAULT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_files_owner ON files(owner);
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
				created_at TEXT NOT NULL,
				expires_at TEXT,
				download_count INTEGER NOT NULL DEFAULT 0,
				max_downloads INTEGER,
				is_active INTEGER NOT NULL DEFAULT 1,
				password_hash TEXT
			);

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
			CREATE INDEX IF NOT EXISTS idx_dl_share ON share_downloads(share_id);`,
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

	return nil
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

// CreateUploadSession inserts a new upload session
func (db *DB) CreateUploadSession(session *model.UploadSession) error {
	_, err := db.conn.Exec(
		`INSERT INTO upload_sessions (id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.Filename, session.FileSize, session.FileHash,
		session.ChunkSize, session.TotalChunks, session.Status, session.StorageType,
		session.CreatedAt.Format(time.RFC3339), session.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetUploadSession retrieves a session by ID
func (db *DB) GetUploadSession(id string) (*model.UploadSession, error) {
	row := db.conn.QueryRow(
		`SELECT id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, created_at, updated_at
		 FROM upload_sessions WHERE id = ?`, id,
	)
	s := &model.UploadSession{}
	var createdAt, updatedAt string
	err := row.Scan(&s.ID, &s.Filename, &s.FileSize, &s.FileHash,
		&s.ChunkSize, &s.TotalChunks, &s.Status, &s.StorageType, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	// load received chunks
	chunks, err := db.getSessionChunks(id)
	if err != nil {
		return nil, err
	}
	s.ReceivedChunks = chunks
	return s, nil
}

// FindActiveSessionByHash 按 file_hash + filename 查找活跃的断点续传 session。
// 用于 InitUpload 时恢复已有 session，避免每次都创建新 session 导致之前的 chunks 作废。
// 条件：file_hash 匹配 + filename 匹配 + status='active'，取最近一条。
// file_hash 与 filename 双重匹配，避免不同文件 hash 碰撞（虽然概率极低）。
func (db *DB) FindActiveSessionByHash(fileHash, filename string) (*model.UploadSession, error) {
	if fileHash == "" {
		return nil, nil
	}
	row := db.conn.QueryRow(
		`SELECT id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, created_at, updated_at
		 FROM upload_sessions
		 WHERE file_hash = ? AND filename = ? AND status = 'active'
		 ORDER BY created_at DESC LIMIT 1`, fileHash, filename,
	)
	s := &model.UploadSession{}
	var createdAt, updatedAt string
	err := row.Scan(&s.ID, &s.Filename, &s.FileSize, &s.FileHash,
		&s.ChunkSize, &s.TotalChunks, &s.Status, &s.StorageType, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	// 加载已上传的 chunks
	chunks, err := db.getSessionChunks(s.ID)
	if err != nil {
		return nil, err
	}
	s.ReceivedChunks = chunks
	return s, nil
}

func (db *DB) getSessionChunks(sessionID string) ([]int, error) {
	rows, err := db.conn.Query(
		`SELECT chunk_index FROM chunks WHERE session_id = ? ORDER BY chunk_index`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []int
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			return nil, err
		}
		chunks = append(chunks, idx)
	}
	return chunks, rows.Err()
}

// UpdateUploadSession updates session status
func (db *DB) UpdateUploadSessionStatus(id, status string) error {
	_, err := db.conn.Exec(
		`UPDATE upload_sessions SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().Format(time.RFC3339), id,
	)
	return err
}

// SaveChunk records a received chunk
func (db *DB) SaveChunk(sessionID string, chunkIndex int, size int64, hash string) error {
	_, err := db.conn.Exec(
		db.replaceIntoPrefix()+` INTO chunks (session_id, chunk_index, size, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		sessionID, chunkIndex, size, hash, time.Now().Format(time.RFC3339),
	)
	return err
}

// GetReceivedChunks returns the list of received chunk indices
func (db *DB) GetReceivedChunks(sessionID string) ([]int, error) {
	return db.getSessionChunks(sessionID)
}

// CreateFile records a completed file
func (db *DB) CreateFile(f *model.FileRecord) error {
	_, err := db.conn.Exec(
		`INSERT INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Filename, f.Size, f.Hash, f.StoragePath, f.StorageType,
		f.ChunkSize, f.TotalChunks, f.Status, f.Owner,
		f.CreatedAt.Format(time.RFC3339), f.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetFile retrieves a file record by ID（仅返回未删除的文件，回收站文件不可访问）
func (db *DB) GetFile(id string) (*model.FileRecord, error) {
	row := db.conn.QueryRow(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
		 FROM files WHERE id = ? AND deleted_at IS NULL`, id,
	)
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// FindFileByName checks if a file with the given name exists (filtered by owner)
// owner 为空时不过滤（兼容旧调用方），非空时加 WHERE owner = ? 条件
// 仅返回未删除的文件（deleted_at IS NULL），回收站文件不参与冲突检测
func (db *DB) FindFileByName(filename, owner string) (*model.FileRecord, error) {
	var row *sql.Row
	if owner == "" {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
			 FROM files WHERE filename = ? AND status = 'completed' AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1`, filename)
	} else {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
			 FROM files WHERE filename = ? AND status = 'completed' AND owner = ? AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1`, filename, owner)
	}
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// GetFileByHash 按 hash + owner + size 查询已完成文件（秒传检查用）。
// hash 必须非空（完整 SHA256 hex，64 字符）；owner 非空时按用户过滤（防跨用户哈希侧信道探测）。
// 返回 sql.ErrNoRows 时表示未命中（调用方据此判断是否秒传）。
// 校验条件：hash 匹配 + size 匹配 + status='completed'，三重校验避免误判。
func (db *DB) GetFileByHash(hash, owner string, fileSize int64) (*model.FileRecord, error) {
	row := db.conn.QueryRow(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
		 FROM files WHERE hash = ? AND size = ? AND status = 'completed' AND owner = ? AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, hash, fileSize, owner)
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// ListFiles returns all completed files
// ListFiles 返回所有已完成文件。prefix 非空时按路径前缀过滤（递归匹配子目录）。
// owner 非空时按归属用户过滤（用户隔离）；owner 为空时返回所有用户的文件（兼容旧调用方）。
// 路径枚举方案：filename 中的 "/" 作为虚拟目录分隔符，无需单独 directories 表。
func (db *DB) ListFiles(prefix, owner string) ([]model.FileRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	const cols = `SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
		     FROM files WHERE status = 'completed' AND deleted_at IS NULL`
	switch {
	case prefix == "" && owner == "":
		rows, err = db.conn.Query(cols + ` ORDER BY created_at DESC`)
	case prefix == "" && owner != "":
		rows, err = db.conn.Query(cols+` AND owner = ? ORDER BY created_at DESC`, owner)
	case prefix != "" && owner == "":
		// ESCAPE '|': 用 | 作为 LIKE 转义符，避免 MySQL 对 '\' 的歧义解析
		rows, err = db.conn.Query(cols+` AND filename LIKE ? ESCAPE '|' ORDER BY created_at DESC`,
			escapeLikePrefix(prefix)+"%")
	default:
		rows, err = db.conn.Query(cols+` AND filename LIKE ? ESCAPE '|' AND owner = ? ORDER BY created_at DESC`,
			escapeLikePrefix(prefix)+"%", owner)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.FileRecord
	for rows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		files = append(files, f)
	}
	return files, rows.Err()
}

// escapeLikePrefix 转义 LIKE 模式中的特殊字符（% _ |），防止前缀注入。
// 转义符使用 |（与 SQL 中的 ESCAPE '|' 对应），避免 MySQL 对 '\' 的歧义解析。
func escapeLikePrefix(s string) string {
	out := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' || c == '_' || c == '|' {
			out = append(out, '|')
		}
		out = append(out, c)
	}
	return string(out)
}

// CreateUser 创建新用户。username/email 唯一，重复时返回错误。
// 兼容旧调用方：Email/Status 为空时数据库会自动填充默认值（email=NULL, status='active'）。
func (db *DB) CreateUser(u *model.User) error {
	status := u.Status
	if status == "" {
		status = "active"
	}
	_, err := db.conn.Exec(
		`INSERT INTO users (id, username, email, password_hash, role, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, nullableString(u.Email), u.PasswordHash, u.Role, status,
		u.CreatedAt.Format(time.RFC3339), u.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// nullableString 空字符串转 nil（让数据库存储 NULL 而非空串）
func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// GetUserByUsername 按用户名查询用户。不存在返回 sql.ErrNoRows。
func (db *DB) GetUserByUsername(username string) (*model.User, error) {
	var u model.User
	var email sql.NullString
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		`SELECT id, username, email, password_hash, role, status, created_at, updated_at FROM users WHERE username = ?`,
		username,
	).Scan(&u.ID, &u.Username, &email, &u.PasswordHash, &u.Role, &u.Status, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	u.Email = email.String
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &u, nil
}

// GetUserByEmail 按邮箱查询用户。不存在返回 sql.ErrNoRows。
func (db *DB) GetUserByEmail(email string) (*model.User, error) {
	var u model.User
	var emailDB sql.NullString
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		`SELECT id, username, email, password_hash, role, status, created_at, updated_at FROM users WHERE email = ?`,
		email,
	).Scan(&u.ID, &u.Username, &emailDB, &u.PasswordHash, &u.Role, &u.Status, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	u.Email = emailDB.String
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &u, nil
}

// GetUserByID 按 ID 查询用户。不存在返回 sql.ErrNoRows。
func (db *DB) GetUserByID(id string) (*model.User, error) {
	var u model.User
	var email sql.NullString
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		`SELECT id, username, email, password_hash, role, status, created_at, updated_at FROM users WHERE id = ?`,
		id,
	).Scan(&u.ID, &u.Username, &email, &u.PasswordHash, &u.Role, &u.Status, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	u.Email = email.String
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &u, nil
}

// UpdateUserStatus 更新用户激活状态（pending → active）。
func (db *DB) UpdateUserStatus(id, status string) error {
	_, err := db.conn.Exec(
		`UPDATE users SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().Format(time.RFC3339), id,
	)
	return err
}

// UpdateUserPassword 更新用户密码哈希（忘记密码重置时调用）。
func (db *DB) UpdateUserPassword(id, passwordHash string) error {
	_, err := db.conn.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, time.Now().Format(time.RFC3339), id,
	)
	return err
}

// CountUsers 返回用户总数。用于首次启动判断是否需要创建初始管理员。
func (db *DB) CountUsers() (int64, error) {
	var count int64
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// CreateActivationToken 创建账号激活令牌（注册后通过邮件发送链接）。
// token 为 32 字节 hex 字符串，expiresAt 为过期时间。
func (db *DB) CreateActivationToken(userID, token string, expiresAt time.Time) error {
	now := time.Now()
	_, err := db.conn.Exec(
		`INSERT INTO user_activation_tokens (user_id, token, expires_at, created_at)
		 VALUES (?, ?, ?, ?)`,
		userID, token, expiresAt.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	return err
}

// GetActivationToken 按 token 查询激活令牌。返回 userID 与过期时间。
// 不存在返回 sql.ErrNoRows。
func (db *DB) GetActivationToken(token string) (userID string, expiresAt time.Time, err error) {
	var expStr string
	err = db.conn.QueryRow(
		`SELECT user_id, expires_at FROM user_activation_tokens WHERE token = ?`,
		token,
	).Scan(&userID, &expStr)
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt, _ = time.Parse(time.RFC3339, expStr)
	return userID, expiresAt, nil
}

// DeleteActivationToken 删除指定 token（激活成功后调用，确保一次性）。
func (db *DB) DeleteActivationToken(token string) error {
	_, err := db.conn.Exec(`DELETE FROM user_activation_tokens WHERE token = ?`, token)
	return err
}

// DeleteExpiredActivationTokens 清理所有过期 token（启动时调用，避免表膨胀）。
func (db *DB) DeleteExpiredActivationTokens() (int64, error) {
	now := time.Now().Format(time.RFC3339)
	res, err := db.conn.Exec(`DELETE FROM user_activation_tokens WHERE expires_at < ?`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteActivationTokensByUserID 删除指定用户的所有激活 token（重新发送激活邮件前清理旧 token）。
func (db *DB) DeleteActivationTokensByUserID(userID string) error {
	_, err := db.conn.Exec(`DELETE FROM user_activation_tokens WHERE user_id = ?`, userID)
	return err
}

// CreateResetCode 创建密码重置验证码（忘记密码时通过邮件发送）。
// code 为 6 位数字字符串，expiresAt 为过期时间。
func (db *DB) CreateResetCode(userID, email, code string, expiresAt time.Time) error {
	now := time.Now()
	_, err := db.conn.Exec(
		`INSERT INTO password_reset_codes (user_id, email, code, expires_at, used, created_at)
		 VALUES (?, ?, ?, ?, 0, ?)`,
		userID, email, code, expiresAt.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	return err
}

// GetResetCode 按 email + code 查询验证码记录。返回 userID、是否已使用、过期时间。
// 不存在返回 sql.ErrNoRows。
func (db *DB) GetResetCode(email, code string) (userID string, used bool, expiresAt time.Time, err error) {
	var usedInt int
	var expStr string
	err = db.conn.QueryRow(
		`SELECT user_id, used, expires_at FROM password_reset_codes
		 WHERE email = ? AND code = ? ORDER BY created_at DESC LIMIT 1`,
		email, code,
	).Scan(&userID, &usedInt, &expStr)
	if err != nil {
		return "", false, time.Time{}, err
	}
	expiresAt, _ = time.Parse(time.RFC3339, expStr)
	return userID, usedInt != 0, expiresAt, nil
}

// MarkResetCodeUsed 标记验证码为已使用（重置密码成功后调用，防止重放）。
func (db *DB) MarkResetCodeUsed(email, code string) error {
	_, err := db.conn.Exec(
		`UPDATE password_reset_codes SET used = 1 WHERE email = ? AND code = ?`,
		email, code,
	)
	return err
}

// DeleteExpiredResetCodes 清理所有过期验证码（启动时调用，避免表膨胀）。
func (db *DB) DeleteExpiredResetCodes() (int64, error) {
	now := time.Now().Format(time.RFC3339)
	res, err := db.conn.Exec(`DELETE FROM password_reset_codes WHERE expires_at < ?`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteFile 软删除单个文件记录（按 ID，移入回收站）。返回受影响行数。
// 仅标记 deleted_at = 当前时间，不物理删除，30 天后由 CleanupExpiredTrash 自动清理。
func (db *DB) DeleteFile(id string) (int64, error) {
	res, err := db.conn.Exec(
		`UPDATE files SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().Format(time.RFC3339), id,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteFilesByPrefix 软删除指定前缀下所有文件（递归匹配子目录，移入回收站）。返回删除文件列表。
// 路径枚举方案：用 LIKE 'prefix%' ESCAPE '|' 匹配。调用方不再需要删除存储文件（软删除保留存储）。
func (db *DB) DeleteFilesByPrefix(prefix, owner string) ([]model.FileRecord, error) {
	// 先查询出待删除文件（返回给调用方用于 UI 反馈）
	files, err := db.ListFiles(prefix, owner)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return files, nil
	}
	// 批量软删除（事务）
	now := time.Now().Format(time.RFC3339)
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, err := tx.Exec(
			`UPDATE files SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
			now, f.ID,
		); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return files, nil
}

// ListTrashedFiles 列出回收站中的文件（deleted_at IS NOT NULL）。
// owner 非空时按归属用户过滤（用户隔离）；owner 为空时返回所有用户的回收站文件（admin 用）。
// 按 deleted_at DESC 排序（最近删除的在前）。
func (db *DB) ListTrashedFiles(owner string) ([]model.FileRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	const cols = `SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at, deleted_at
		     FROM files WHERE deleted_at IS NOT NULL`
	if owner == "" {
		rows, err = db.conn.Query(cols + ` ORDER BY deleted_at DESC`)
	} else {
		rows, err = db.conn.Query(cols+` AND owner = ? ORDER BY deleted_at DESC`, owner)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.FileRecord
	for rows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt, deletedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt, &deletedAt); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		if t, err := time.Parse(time.RFC3339, deletedAt); err == nil {
			f.DeletedAt = &t
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// RestoreFile 恢复回收站中的文件（SET deleted_at = NULL）。
// owner 非空时加 WHERE owner = ? 条件防止越权恢复他人文件；为空时不过滤（admin 用）。
// 返回受影响行数（0 表示文件不存在或不属于该用户）。
func (db *DB) RestoreFile(id, owner string) (int64, error) {
	var res sql.Result
	var err error
	if owner == "" {
		res, err = db.conn.Exec(
			`UPDATE files SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
			time.Now().Format(time.RFC3339), id,
		)
	} else {
		res, err = db.conn.Exec(
			`UPDATE files SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL AND owner = ?`,
			time.Now().Format(time.RFC3339), id, owner,
		)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PermanentlyDeleteFile 物理删除回收站中的文件（DELETE FROM files）。
// 仅允许删除已软删除的文件（deleted_at IS NOT NULL），防止误删正常文件。
// owner 非空时加 WHERE owner = ? 条件防止越权删除他人文件；为空时不过滤（admin 用）。
// 返回文件记录（调用方需要 storage_path 删除存储文件）和受影响行数。
func (db *DB) PermanentlyDeleteFile(id, owner string) (*model.FileRecord, int64, error) {
	// 先查询文件记录（获取 storage_path 用于删除存储）
	var row *sql.Row
	if owner == "" {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
			 FROM files WHERE id = ? AND deleted_at IS NOT NULL`, id)
	} else {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
			 FROM files WHERE id = ? AND deleted_at IS NOT NULL AND owner = ?`, id, owner)
	}
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, 0, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	// 物理删除数据库记录
	res, err := db.conn.Exec(`DELETE FROM files WHERE id = ? AND deleted_at IS NOT NULL`, id)
	if err != nil {
		return nil, 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	return f, affected, nil
}

// CleanupExpiredTrash 清理回收站中超过保留期的文件（物理删除数据库记录 + 删除存储文件）。
// retentionDays 为保留天数（默认 30 天）。返回清理的文件数量和错误。
// 此方法在服务启动时自动调用，避免回收站无限膨胀。
// 注意：调用方需要传入 storage.Storage 实例用于删除存储文件，此处仅返回待清理的文件列表。
func (db *DB) CleanupExpiredTrash(retentionDays int) ([]model.FileRecord, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format(time.RFC3339)

	// 查询过期文件（deleted_at < cutoff）
	rows, err := db.conn.Query(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
		 FROM files WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	var expired []model.FileRecord
	for rows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		expired = append(expired, f)
	}
	rows.Close()

	if len(expired) == 0 {
		return expired, nil
	}

	// 批量物理删除数据库记录
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	for _, f := range expired {
		if _, err := tx.Exec(`DELETE FROM files WHERE id = ? AND deleted_at IS NOT NULL`, f.ID); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return expired, nil
}

// UpdateFilename 更新文件名（含路径前缀），用于重命名/移动。
// newFilename 必须通过 validateFilePath 校验（由 handler 层保证）。
// owner 非空时加 WHERE owner = ? 条件防止越权改他人文件名；为空时不过滤（兼容旧调用方）。
func (db *DB) UpdateFilename(id, newFilename, owner string) error {
	if owner == "" {
		_, err := db.conn.Exec(
			`UPDATE files SET filename = ?, updated_at = ? WHERE id = ?`,
			newFilename, time.Now().Format(time.RFC3339), id,
		)
		return err
	}
	_, err := db.conn.Exec(
		`UPDATE files SET filename = ?, updated_at = ? WHERE id = ? AND owner = ?`,
		newFilename, time.Now().Format(time.RFC3339), id, owner,
	)
	return err
}

// === 分享链接 CRUD ===

// CreateShare 创建分享记录。s.ID 由调用方生成（8 字符短 ID）。
// password_hash 为空字符串表示无密码；非空为 bcrypt 哈希值。
func (db *DB) CreateShare(s *model.Share) error {
	var expiresAt interface{}
	if s.ExpiresAt != nil {
		expiresAt = s.ExpiresAt.Format(time.RFC3339)
	}
	var maxDownloads interface{}
	if s.MaxDownloads != nil {
		maxDownloads = *s.MaxDownloads
	}
	isActive := 0
	if s.IsActive {
		isActive = 1
	}
	var passwordHash interface{}
	if s.PasswordHash != "" {
		passwordHash = s.PasswordHash
	}
	_, err := db.conn.Exec(
		`INSERT INTO shares (id, file_id, dir_prefix, share_type, created_by, created_at, expires_at, download_count, max_downloads, is_active, password_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, nullableString(s.FileID), nullableString(s.DirPrefix), s.ShareType,
		s.CreatedBy, s.CreatedAt.Format(time.RFC3339), expiresAt, s.DownloadCount, maxDownloads, isActive, passwordHash,
	)
	return err
}

// GetShare 按 ID 查询分享。不存在返回 sql.ErrNoRows。
func (db *DB) GetShare(id string) (*model.Share, error) {
	var s model.Share
	var fileID, dirPrefix, expiresAtStr, passwordHash sql.NullString
	var maxDownloads sql.NullInt64
	var isActive int
	var createdAtStr string
	err := db.conn.QueryRow(
		`SELECT id, file_id, dir_prefix, share_type, created_by, created_at, expires_at, download_count, max_downloads, is_active, password_hash
		 FROM shares WHERE id = ?`,
		id,
	).Scan(&s.ID, &fileID, &dirPrefix, &s.ShareType, &s.CreatedBy, &createdAtStr, &expiresAtStr, &s.DownloadCount, &maxDownloads, &isActive, &passwordHash)
	if err != nil {
		return nil, err
	}
	s.FileID = fileID.String
	s.DirPrefix = dirPrefix.String
	if expiresAtStr.Valid {
		t, _ := time.Parse(time.RFC3339, expiresAtStr.String)
		s.ExpiresAt = &t
	}
	if maxDownloads.Valid {
		md := int(maxDownloads.Int64)
		s.MaxDownloads = &md
	}
	s.IsActive = isActive != 0
	s.PasswordHash = passwordHash.String
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	return &s, nil
}

// ListSharesByCreator 查询指定用户创建的所有分享（按创建时间倒序）。
func (db *DB) ListSharesByCreator(username string) ([]*model.Share, error) {
	rows, err := db.conn.Query(
		`SELECT id, file_id, dir_prefix, share_type, created_by, created_at, expires_at, download_count, max_downloads, is_active, password_hash
		 FROM shares WHERE created_by = ? ORDER BY created_at DESC`,
		username,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shares []*model.Share
	for rows.Next() {
		var s model.Share
		var fileID, dirPrefix, expiresAtStr, passwordHash sql.NullString
		var maxDownloads sql.NullInt64
		var isActive int
		var createdAtStr string
		if err := rows.Scan(&s.ID, &fileID, &dirPrefix, &s.ShareType, &s.CreatedBy, &createdAtStr, &expiresAtStr, &s.DownloadCount, &maxDownloads, &isActive, &passwordHash); err != nil {
			return nil, err
		}
		s.FileID = fileID.String
		s.DirPrefix = dirPrefix.String
		if expiresAtStr.Valid {
			t, _ := time.Parse(time.RFC3339, expiresAtStr.String)
			s.ExpiresAt = &t
		}
		if maxDownloads.Valid {
			md := int(maxDownloads.Int64)
			s.MaxDownloads = &md
		}
		s.IsActive = isActive != 0
		s.PasswordHash = passwordHash.String
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		shares = append(shares, &s)
	}
	return shares, rows.Err()
}

// DeleteShare 删除分享记录（调用方应先验证所有权）。
func (db *DB) DeleteShare(id string) error {
	_, err := db.conn.Exec(`DELETE FROM shares WHERE id = ?`, id)
	return err
}

// IncrementShareDownload 记录一次下载并增加计数。
// 利用 UNIQUE(share_id, visitor_id) 约束实现幂等：
//   - 新访客：INSERT 成功，RowsAffected=1，更新 shares.download_count + 1
//   - 老访客：INSERT 被忽略，RowsAffected=0，不增加计数
func (db *DB) IncrementShareDownload(shareID, visitorID, ip, userAgent string) error {
	now := time.Now().Format(time.RFC3339)
	res, err := db.conn.Exec(
		db.insertIgnorePrefix()+` INTO share_downloads (share_id, visitor_id, ip, user_agent, downloaded_at)
		 VALUES (?, ?, ?, ?, ?)`,
		shareID, visitorID, ip, userAgent, now,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected > 0 {
		// 新访客下载，增加计数
		_, err = db.conn.Exec(
			`UPDATE shares SET download_count = download_count + 1 WHERE id = ?`,
			shareID,
		)
	}
	return err
}

// === 用户配置 CRUD ===

// GetUserSettings 按用户名查询配置。不存在返回 nil, nil（调用方使用默认值）。
func (db *DB) GetUserSettings(username string) (*model.UserSettings, error) {
	var s model.UserSettings
	var updatedAtStr string
	err := db.conn.QueryRow(
		`SELECT username, chunk_size, concurrency, updated_at FROM user_settings WHERE username = ?`,
		username,
	).Scan(&s.Username, &s.ChunkSize, &s.Concurrency, &updatedAtStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAtStr)
	return &s, nil
}

// SaveUserSettings 保存用户配置（不存在则插入，存在则替换）。
func (db *DB) SaveUserSettings(s *model.UserSettings) error {
	_, err := db.conn.Exec(
		db.replaceIntoPrefix()+` INTO user_settings (username, chunk_size, concurrency, updated_at)
		 VALUES (?, ?, ?, ?)`,
		s.Username, s.ChunkSize, s.Concurrency, s.UpdatedAt.Format(time.RFC3339),
	)
	return err
}
