package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

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
