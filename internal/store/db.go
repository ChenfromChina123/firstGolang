package store

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"filesync/internal/model"

	_ "modernc.org/sqlite"
)

// DB wraps SQLite for metadata persistence
type DB struct {
	conn      *sql.DB
	asyncQ    *AsyncWriteQueue // async batch write channel (nil if not enabled)
	asyncOnce sync.Once
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
	ch     chan writeJob
	db     *sql.DB
	ticker *time.Ticker
	closed chan struct{}
}

const asyncQSize = 2000

// EnableAsyncWrite starts the async write queue.
// Must be called once before using Async* methods.
func (db *DB) EnableAsyncWrite() {
	db.asyncOnce.Do(func() {
		db.asyncQ = &AsyncWriteQueue{
			ch:     make(chan writeJob, asyncQSize),
			db:     db.conn,
			ticker: time.NewTicker(10 * time.Millisecond),
			closed: make(chan struct{}),
		}
		go db.asyncQ.run()
		log.Printf("[AsyncSQLite] Batch write queue enabled (capacity=%d, interval=10ms, batch=500)", asyncQSize)
	})
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
				`INSERT OR IGNORE INTO upload_sessions (id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				j.s.ID, j.s.Filename, j.s.FileSize, j.s.FileHash,
				j.s.ChunkSize, j.s.TotalChunks, j.s.Status, j.s.StorageType,
				j.s.CreatedAt.Format(time.RFC3339), j.s.UpdatedAt.Format(time.RFC3339),
			)
		case "chunk":
			_, execErr = tx.Exec(
				`INSERT OR IGNORE INTO chunks (session_id, chunk_index, size, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
				j.sid, j.idx, j.size, j.hash, time.Now().Format(time.RFC3339),
			)
		case "file":
			_, execErr = tx.Exec(
				`INSERT OR IGNORE INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
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
				`INSERT OR IGNORE INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
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
			`INSERT OR IGNORE INTO upload_sessions (id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.s.ID, j.s.Filename, j.s.FileSize, j.s.FileHash,
			j.s.ChunkSize, j.s.TotalChunks, j.s.Status, j.s.StorageType,
			j.s.CreatedAt.Format(time.RFC3339), j.s.UpdatedAt.Format(time.RFC3339),
		)
	case "chunk":
		_, err = q.db.Exec(
			`INSERT OR IGNORE INTO chunks (session_id, chunk_index, size, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			j.sid, j.idx, j.size, j.hash, time.Now().Format(time.RFC3339),
		)
	case "file":
		_, err = q.db.Exec(
			`INSERT OR IGNORE INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
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
			`INSERT OR IGNORE INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
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

// New opens or creates the database.
//
// 关键 PRAGMA 配置：
//   - synchronous=NORMAL：事务提交不再 fsync 等待，崩溃时只丢最近事务
//   - cache_size=-65536：64MB 内存缓存，提升查询命中率
//   - busy_timeout=5000：5s 锁等待，避免 SQLITE_BUSY 错误
//   - 注意：modernc.org/sqlite（纯 Go）不启用 WAL/mmap，单连接下 WAL 无收益
func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
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

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS upload_sessions (
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
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	`
	_, err := db.conn.Exec(schema)
	return err
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
		`INSERT OR REPLACE INTO chunks (session_id, chunk_index, size, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
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
		`INSERT INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Filename, f.Size, f.Hash, f.StoragePath, f.StorageType,
		f.ChunkSize, f.TotalChunks, f.Status,
		f.CreatedAt.Format(time.RFC3339), f.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetFile retrieves a file record by ID
func (db *DB) GetFile(id string) (*model.FileRecord, error) {
	row := db.conn.QueryRow(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at
		 FROM files WHERE id = ?`, id,
	)
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// FindFileByName checks if a file with the given name exists
func (db *DB) FindFileByName(filename string) (*model.FileRecord, error) {
	row := db.conn.QueryRow(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at
		 FROM files WHERE filename = ? AND status = 'completed' ORDER BY created_at DESC LIMIT 1`, filename,
	)
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// ListFiles returns all completed files
// ListFiles 返回所有已完成文件。prefix 非空时按路径前缀过滤（递归匹配子目录）。
// 路径枚举方案：filename 中的 "/" 作为虚拟目录分隔符，无需单独 directories 表。
func (db *DB) ListFiles(prefix string) ([]model.FileRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if prefix == "" {
		rows, err = db.conn.Query(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at
		     FROM files WHERE status = 'completed' ORDER BY created_at DESC`)
	} else {
		rows, err = db.conn.Query(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at
		     FROM files WHERE status = 'completed' AND filename LIKE ? ESCAPE '\' ORDER BY created_at DESC`,
			escapeLikePrefix(prefix)+"%")
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
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		files = append(files, f)
	}
	return files, rows.Err()
}

// escapeLikePrefix 转义 LIKE 模式中的特殊字符（% _ \），防止前缀注入。
func escapeLikePrefix(s string) string {
	out := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' || c == '_' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}

// DeleteFile 删除单个文件记录（按 ID）。返回受影响行数。
func (db *DB) DeleteFile(id string) (int64, error) {
	res, err := db.conn.Exec(`DELETE FROM files WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteFilesByPrefix 删除指定前缀下所有文件（递归匹配子目录）。返回删除文件列表。
// 路径枚举方案：用 LIKE 'prefix%' ESCAPE '\' 匹配。调用方负责删除存储文件。
func (db *DB) DeleteFilesByPrefix(prefix string) ([]model.FileRecord, error) {
	// 先查询出待删除文件（调用方需要 storage_path 删除存储）
	files, err := db.ListFiles(prefix)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return files, nil
	}
	// 批量删除数据库记录（事务）
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, err := tx.Exec(`DELETE FROM files WHERE id = ?`, f.ID); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return files, nil
}

// UpdateFilename 更新文件名（含路径前缀），用于重命名/移动。
// newFilename 必须通过 validateFilePath 校验（由 handler 层保证）。
func (db *DB) UpdateFilename(id, newFilename string) error {
	_, err := db.conn.Exec(
		`UPDATE files SET filename = ?, updated_at = ? WHERE id = ?`,
		newFilename, time.Now().Format(time.RFC3339), id,
	)
	return err
}
