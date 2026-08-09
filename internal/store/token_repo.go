package store

import (
	"time"
)

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
