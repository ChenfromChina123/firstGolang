package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

// === 空间（Space）CRUD ===

// CreateSpace 创建空间。
func (db *DB) CreateSpace(s *model.Space) error {
	_, err := db.conn.Exec(
		`INSERT INTO spaces (id, name, owner, storage_type, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Owner, s.StorageType,
		s.CreatedAt.Format(time.RFC3339), s.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// ListSpaces 列出指定用户的空间（owner 为空时列出所有空间，admin 用）。
// 附带每个空间的动态文件数（含 .keep 占位，不含回收站）。
// 按 created_at 升序（默认空间在前）+ id 排序保证确定性。
func (db *DB) ListSpaces(owner string) ([]model.Space, error) {
	var rows *sql.Rows
	var err error
	const cols = `SELECT s.id, s.name, s.owner, s.storage_type, s.created_at, s.updated_at,
			(SELECT COUNT(*) FROM files f WHERE f.space_id = s.id AND f.deleted_at IS NULL) AS file_count
		 FROM spaces s`
	if owner == "" {
		rows, err = db.conn.Query(cols + ` ORDER BY s.created_at ASC, s.id ASC`)
	} else {
		rows, err = db.conn.Query(cols+` WHERE s.owner = ? ORDER BY s.created_at ASC, s.id ASC`, owner)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var spaces []model.Space
	for rows.Next() {
		var s model.Space
		var createdAt, updatedAt string
		if err := rows.Scan(&s.ID, &s.Name, &s.Owner, &s.StorageType, &createdAt, &updatedAt, &s.FileCount); err != nil {
			return nil, err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		spaces = append(spaces, s)
	}
	return spaces, rows.Err()
}

// EnsureUserDefaultSpace 确保指定用户存在「我的空间」记录（不存在则插入）。
// 用于新注册用户首次访问空间列表时自动补建。
func (db *DB) EnsureUserDefaultSpace(username string) error {
	if username == "" {
		return nil
	}
	id := "default-" + username
	now := time.Now().Format(time.RFC3339)
	_, err := db.conn.Exec(
		db.insertIgnorePrefix()+` INTO spaces (id, name, owner, storage_type, created_at, updated_at)
		 VALUES (?, ?, ?, 'local', ?, ?)`,
		id, "我的空间", username, now, now,
	)
	return err
}

// GetSpace 按 ID 查询空间。不存在返回 sql.ErrNoRows。
func (db *DB) GetSpace(id string) (*model.Space, error) {
	var s model.Space
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		`SELECT id, name, owner, storage_type, created_at, updated_at FROM spaces WHERE id = ?`,
		id,
	).Scan(&s.ID, &s.Name, &s.Owner, &s.StorageType, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &s, nil
}

// DeleteSpace 删除空间记录。调用方需先确认空间为空（CountFilesBySpace==0）。
// 默认空间记录（id 以 "default-" 开头）不允许删除，调用方负责拦截。
func (db *DB) DeleteSpace(id string) error {
	_, err := db.conn.Exec(`DELETE FROM spaces WHERE id = ?`, id)
	return err
}

// CountFilesBySpace 统计空间内文件数（含 .keep 占位文件，不含回收站）。
// 用于删除空间前的非空检查。
func (db *DB) CountFilesBySpace(spaceID string) (int64, error) {
	var count int64
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM files WHERE space_id = ? AND deleted_at IS NULL`, spaceID).Scan(&count)
	return count, err
}
