package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

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
