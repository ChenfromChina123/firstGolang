package store

import (
	"database/sql"
	"fmt"
	"time"

	"filesync/internal/model"

	_ "modernc.org/sqlite"
)

// DB wraps SQLite for metadata persistence
type DB struct {
	conn *sql.DB
}

// New opens or creates the database
func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite doesn't support concurrent writes well

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
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
func (db *DB) ListFiles() ([]model.FileRecord, error) {
	rows, err := db.conn.Query(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, created_at, updated_at
		 FROM files WHERE status = 'completed' ORDER BY created_at DESC`)
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
