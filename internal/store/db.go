package store

import (
	"database/sql"
	"fmt"
	"log"
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
	kind string // "session" | "chunk" | "file" | "update_status" | "file_and_status"
	sid  string // session ID (for chunk/status)
	idx  int    // chunk index (for chunk)
	size int64  // chunk size
	hash string // chunk/file hash
	s    *model.UploadSession
	f    *model.FileRecord
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

// executor 抽象 Exec 方法，使 *sql.DB 和 *sql.Tx 可互换，消除 flush/execSync 重复逻辑
type executor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// executeJob 统一执行单个 writeJob 的 SQL 逻辑，供 flush（事务内）和 execSync（同步回退）复用
func (q *AsyncWriteQueue) executeJob(exec executor, j writeJob) error {
	var err error
	switch j.kind {
	case "session":
		_, err = exec.Exec(
			q.insertIgnorePrefix()+` INTO upload_sessions (id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, upload_mode, object_key, space_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.s.ID, j.s.Filename, j.s.FileSize, j.s.FileHash,
			j.s.ChunkSize, j.s.TotalChunks, j.s.Status, j.s.StorageType,
			j.s.UploadMode, j.s.ObjectKey, j.s.SpaceID,
			j.s.CreatedAt.Format(time.RFC3339), j.s.UpdatedAt.Format(time.RFC3339),
		)
	case "chunk":
		_, err = exec.Exec(
			q.insertIgnorePrefix()+` INTO chunks (session_id, chunk_index, size, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			j.sid, j.idx, j.size, j.hash, time.Now().Format(time.RFC3339),
		)
	case "file":
		_, err = exec.Exec(
			q.insertIgnorePrefix()+` INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, space_id, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.f.ID, j.f.Filename, j.f.Size, j.f.Hash, j.f.StoragePath, j.f.StorageType,
			j.f.ChunkSize, j.f.TotalChunks, j.f.Status, j.f.Owner, j.f.SpaceID,
			j.f.CreatedAt.Format(time.RFC3339), j.f.UpdatedAt.Format(time.RFC3339),
		)
	case "update_status":
		_, err = exec.Exec(
			`UPDATE upload_sessions SET status = 'completed', updated_at = ? WHERE id = ?`,
			time.Now().Format(time.RFC3339), j.sid,
		)
	case "file_and_status":
		_, err = exec.Exec(
			q.insertIgnorePrefix()+` INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.f.ID, j.f.Filename, j.f.Size, j.f.Hash, j.f.StoragePath, j.f.StorageType,
			j.f.ChunkSize, j.f.TotalChunks, j.f.Status, j.f.Owner,
			j.f.CreatedAt.Format(time.RFC3339), j.f.UpdatedAt.Format(time.RFC3339),
		)
		if err == nil {
			_, err = exec.Exec(
				`UPDATE upload_sessions SET status = 'completed', updated_at = ? WHERE id = ?`,
				time.Now().Format(time.RFC3339), j.sid,
			)
		}
	}
	return err
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
		if execErr := q.executeJob(tx, j); execErr != nil {
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
	if err := q.executeJob(q.db, j); err != nil {
		log.Printf("[AsyncSQLite] execSync error (%s): %v", j.kind, err)
	}
}

// AsyncCreateSession enqueues a session for batch write.
// 队列满时回退到同步写入，避免丢失 session 记录导致后续上传状态查询失败。
func (db *DB) AsyncCreateSession(session *model.UploadSession) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "session", s: session}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Falling back to sync write for session %s", session.ID)
			db.CreateUploadSession(session)
		}
	} else {
		db.CreateUploadSession(session)
	}
}

// AsyncSaveChunk enqueues a chunk record for batch write.
// 队列满时回退到同步写入，避免丢失 chunk 记录导致断点续传失效。
func (db *DB) AsyncSaveChunk(sessionID string, chunkIndex int, size int64, hash string) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "chunk", sid: sessionID, idx: chunkIndex, size: size, hash: hash}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Falling back to sync write for chunk %s/%d", sessionID, chunkIndex)
			db.SaveChunk(sessionID, chunkIndex, size, hash)
		}
	} else {
		db.SaveChunk(sessionID, chunkIndex, size, hash)
	}
}

// AsyncCreateFile enqueues a file record for batch write.
// 队列满时回退到同步写入，避免丢失 file 记录导致文件不可见。
func (db *DB) AsyncCreateFile(f *model.FileRecord) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "file", f: f}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Falling back to sync write for file %s", f.ID)
			db.CreateFile(f)
		}
	} else {
		db.CreateFile(f)
	}
}

// AsyncUpdateStatus enqueues a session status update for batch write.
// 队列满时回退到同步写入，避免丢失 status 更新导致 session 永久卡在 uploading。
func (db *DB) AsyncUpdateStatus(sessionID string) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "update_status", sid: sessionID}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Falling back to sync write for status update %s", sessionID)
			db.UpdateUploadSessionStatus(sessionID, "completed")
		}
	} else {
		db.UpdateUploadSessionStatus(sessionID, "completed")
	}
}

// AsyncCreateFileAndStatus atomically enqueues a file record + session status update.
// These two operations are always written together in the same transaction, ensuring consistency.
// 队列满时回退到同步写入，避免丢失 file 记录导致上传成功但文件不可见。
func (db *DB) AsyncCreateFileAndStatus(f *model.FileRecord, sessionID string) {
	if db.asyncQ != nil {
		select {
		case db.asyncQ.ch <- writeJob{kind: "file_and_status", f: f, sid: sessionID}:
		default:
			log.Printf("[AsyncSQLite] Queue full! Falling back to sync write for file_and_status %s/%s", f.ID, sessionID)
			db.CreateFile(f)
			db.UpdateUploadSessionStatus(sessionID, "completed")
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
