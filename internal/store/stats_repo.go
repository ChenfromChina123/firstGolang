package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

// StorageUsage 存储用量统计结果
type StorageUsage struct {
	UsedSize   int64 `json:"used_size"`   // 已用空间（字节，不含回收站）
	FileCount  int64 `json:"file_count"`  // 文件数
	TrashSize  int64 `json:"trash_size"`  // 回收站大小（字节）
	TrashCount int64 `json:"trash_count"` // 回收站文件数
}

// GetUserStorageUsage 统计指定用户的存储用量。
// owner 非空时按用户过滤；owner 为空时统计全局（admin 用）。
// 统计范围：status='completed' 的文件；回收站文件单独统计。
func (db *DB) GetUserStorageUsage(owner string) (*StorageUsage, error) {
	u := &StorageUsage{}
	var err error
	if owner != "" {
		err = db.conn.QueryRow(
			`SELECT COALESCE(SUM(size),0), COUNT(*) FROM files
			 WHERE owner = ? AND deleted_at IS NULL AND status = 'completed'`, owner,
		).Scan(&u.UsedSize, &u.FileCount)
	} else {
		err = db.conn.QueryRow(
			`SELECT COALESCE(SUM(size),0), COUNT(*) FROM files
			 WHERE deleted_at IS NULL AND status = 'completed'`,
		).Scan(&u.UsedSize, &u.FileCount)
	}
	if err != nil {
		return nil, err
	}
	if owner != "" {
		err = db.conn.QueryRow(
			`SELECT COALESCE(SUM(size),0), COUNT(*) FROM files
			 WHERE owner = ? AND deleted_at IS NOT NULL`, owner,
		).Scan(&u.TrashSize, &u.TrashCount)
	} else {
		err = db.conn.QueryRow(
			`SELECT COALESCE(SUM(size),0), COUNT(*) FROM files
			 WHERE deleted_at IS NOT NULL`,
		).Scan(&u.TrashSize, &u.TrashCount)
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ListUsers 列出所有用户（admin 用，不含密码哈希）。
// 按 created_at 降序排列。
func (db *DB) ListUsers() ([]*model.User, error) {
	rows, err := db.conn.Query(
		`SELECT id, username, email, role, status, created_at, updated_at FROM users ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*model.User
	for rows.Next() {
		var u model.User
		var email sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&u.ID, &u.Username, &email, &u.Role, &u.Status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		u.Email = email.String
		u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		u.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		users = append(users, &u)
	}
	return users, rows.Err()
}

// SystemStats 系统总览统计
type SystemStats struct {
	TotalUsers    int64 `json:"total_users"`
	ActiveUsers   int64 `json:"active_users"`
	DisabledUsers int64 `json:"disabled_users"`
	TotalFiles    int64 `json:"total_files"`
	TotalSize     int64 `json:"total_size"`
	TrashFiles    int64 `json:"trash_files"`
	TrashSize     int64 `json:"trash_size"`
	TotalShares   int64 `json:"total_shares"`
}

// GetSystemStats 获取系统总览统计（admin 用）。
// 一次性查询所有统计信息，避免多次 RTT。
func (db *DB) GetSystemStats() (*SystemStats, error) {
	s := &SystemStats{}
	var err error

	// 用户统计
	err = db.conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&s.TotalUsers)
	if err != nil {
		return nil, err
	}
	err = db.conn.QueryRow(`SELECT COUNT(*) FROM users WHERE status = 'active'`).Scan(&s.ActiveUsers)
	if err != nil {
		return nil, err
	}
	err = db.conn.QueryRow(`SELECT COUNT(*) FROM users WHERE status != 'active'`).Scan(&s.DisabledUsers)
	if err != nil {
		return nil, err
	}

	// 文件统计（正常文件）
	err = db.conn.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM files WHERE deleted_at IS NULL AND status = 'completed'`,
	).Scan(&s.TotalFiles, &s.TotalSize)
	if err != nil {
		return nil, err
	}

	// 回收站统计
	err = db.conn.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM files WHERE deleted_at IS NOT NULL`,
	).Scan(&s.TrashFiles, &s.TrashSize)
	if err != nil {
		return nil, err
	}

	// 分享统计
	err = db.conn.QueryRow(`SELECT COUNT(*) FROM shares`).Scan(&s.TotalShares)
	if err != nil {
		return nil, err
	}

	return s, nil
}
