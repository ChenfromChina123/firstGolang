package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

// CreateUploadSession inserts a new upload session
func (db *DB) CreateUploadSession(session *model.UploadSession) error {
	_, err := db.conn.Exec(
		`INSERT INTO upload_sessions (id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, upload_mode, object_key, space_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.Filename, session.FileSize, session.FileHash,
		session.ChunkSize, session.TotalChunks, session.Status, session.StorageType,
		session.UploadMode, session.ObjectKey, session.SpaceID,
		session.CreatedAt.Format(time.RFC3339), session.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetUploadSession retrieves a session by ID
func (db *DB) GetUploadSession(id string) (*model.UploadSession, error) {
	row := db.conn.QueryRow(
		`SELECT id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, upload_mode, object_key, space_id, created_at, updated_at
		 FROM upload_sessions WHERE id = ?`, id,
	)
	s := &model.UploadSession{}
	var createdAt, updatedAt string
	err := row.Scan(&s.ID, &s.Filename, &s.FileSize, &s.FileHash,
		&s.ChunkSize, &s.TotalChunks, &s.Status, &s.StorageType,
		&s.UploadMode, &s.ObjectKey, &s.SpaceID, &createdAt, &updatedAt)
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

// FindActiveSessionByHash 按 space_id + file_hash + filename 查找活跃的断点续传 session。
// 用于 InitUpload 时恢复已有 session，避免每次都创建新 session 导致之前的 chunks 作废。
// 条件：space_id 匹配（空间隔离，避免跨空间复用 session 导致文件落入错误空间）+ file_hash 匹配 + filename 匹配 + status='active'，取最近一条。
func (db *DB) FindActiveSessionByHash(spaceID, fileHash, filename string) (*model.UploadSession, error) {
	if fileHash == "" {
		return nil, nil
	}
	row := db.conn.QueryRow(
		`SELECT id, filename, file_size, file_hash, chunk_size, total_chunks, status, storage_type, upload_mode, object_key, space_id, created_at, updated_at
		 FROM upload_sessions
		 WHERE file_hash = ? AND filename = ? AND status = 'active' AND space_id = ?
		 ORDER BY created_at DESC LIMIT 1`, fileHash, filename, spaceID,
	)
	s := &model.UploadSession{}
	var createdAt, updatedAt string
	err := row.Scan(&s.ID, &s.Filename, &s.FileSize, &s.FileHash,
		&s.ChunkSize, &s.TotalChunks, &s.Status, &s.StorageType,
		&s.UploadMode, &s.ObjectKey, &s.SpaceID, &createdAt, &updatedAt)
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
