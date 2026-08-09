package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

// === API Token (PAT) 仓储 ===
// 独立文件（不并入 db.go），遵守 store 层按领域拆分的方向。
// 表结构见 migrate.go 的 api_tokens 建表语句。

// CreateAPIToken 创建 PAT 记录（token_hash 唯一）。
func (db *DB) CreateAPIToken(t *model.APIToken) error {
	_, err := db.conn.Exec(
		`INSERT INTO api_tokens
		 (id, user_id, username, name, token_hash, scopes, space_id, path_prefix,
		  quota_bytes, quota_used, created_at, expires_at, last_used_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Username, t.Name, t.TokenHash, t.Scopes, t.SpaceID, t.PathPrefix,
		t.QuotaBytes, t.QuotaUsed, t.CreatedAt.Format(time.RFC3339),
		nullableTime(t.ExpiresAt), nullableTime(t.LastUsedAt), nullableTime(t.RevokedAt),
	)
	return err
}

// GetAPITokenByHash 按 SHA-256 哈希查询 PAT（认证时使用）。
// 不存在返回 sql.ErrNoRows。
func (db *DB) GetAPITokenByHash(hash string) (*model.APIToken, error) {
	var t model.APIToken
	var createdAt, expiresAt, lastUsedAt, revokedAt sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, user_id, username, name, token_hash, scopes, space_id, path_prefix,
		        quota_bytes, quota_used, created_at, expires_at, last_used_at, revoked_at
		 FROM api_tokens WHERE token_hash = ?`,
		hash,
	).Scan(&t.ID, &t.UserID, &t.Username, &t.Name, &t.TokenHash, &t.Scopes, &t.SpaceID, &t.PathPrefix,
		&t.QuotaBytes, &t.QuotaUsed, &createdAt, &expiresAt, &lastUsedAt, &revokedAt)
	if err != nil {
		return nil, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
	t.ExpiresAt = parseNullableTime(expiresAt)
	t.LastUsedAt = parseNullableTime(lastUsedAt)
	t.RevokedAt = parseNullableTime(revokedAt)
	return &t, nil
}

// GetAPITokenByID 按 ID 查询 PAT（管理页用）。
func (db *DB) GetAPITokenByID(id string) (*model.APIToken, error) {
	var t model.APIToken
	var createdAt, expiresAt, lastUsedAt, revokedAt sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, user_id, username, name, token_hash, scopes, space_id, path_prefix,
		        quota_bytes, quota_used, created_at, expires_at, last_used_at, revoked_at
		 FROM api_tokens WHERE id = ?`,
		id,
	).Scan(&t.ID, &t.UserID, &t.Username, &t.Name, &t.TokenHash, &t.Scopes, &t.SpaceID, &t.PathPrefix,
		&t.QuotaBytes, &t.QuotaUsed, &createdAt, &expiresAt, &lastUsedAt, &revokedAt)
	if err != nil {
		return nil, err
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
	t.ExpiresAt = parseNullableTime(expiresAt)
	t.LastUsedAt = parseNullableTime(lastUsedAt)
	t.RevokedAt = parseNullableTime(revokedAt)
	return &t, nil
}

// ListAPITokens 列出指定用户的未吊销 PAT（admin 传 userID="" 列全部）。
// 按 created_at 倒序（最新在前）。
func (db *DB) ListAPITokens(userID string) ([]model.APIToken, error) {
	var rows *sql.Rows
	var err error
	const cols = `SELECT id, user_id, username, name, token_hash, scopes, space_id, path_prefix,
		        quota_bytes, quota_used, created_at, expires_at, last_used_at, revoked_at
		 FROM api_tokens`
	if userID == "" {
		rows, err = db.conn.Query(cols + ` WHERE revoked_at IS NULL ORDER BY created_at DESC`)
	} else {
		rows, err = db.conn.Query(cols+` WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC`, userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []model.APIToken
	for rows.Next() {
		var t model.APIToken
		var createdAt, expiresAt, lastUsedAt, revokedAt sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Username, &t.Name, &t.TokenHash, &t.Scopes, &t.SpaceID,
			&t.PathPrefix, &t.QuotaBytes, &t.QuotaUsed, &createdAt, &expiresAt, &lastUsedAt, &revokedAt); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
		t.ExpiresAt = parseNullableTime(expiresAt)
		t.LastUsedAt = parseNullableTime(lastUsedAt)
		t.RevokedAt = parseNullableTime(revokedAt)
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// RevokeAPIToken 吊销 PAT（软删除：置 revoked_at，不影响其它字段）。
// 幂等：已吊销再次调用返回 nil。
func (db *DB) RevokeAPIToken(id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := db.conn.Exec(
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now, id,
	)
	return err
}

// TouchAPIToken 更新 last_used_at（认证成功时调用，非致命）。
func (db *DB) TouchAPIToken(id string) {
	now := time.Now().Format(time.RFC3339)
	_, _ = db.conn.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now, id)
}

// AddAPITokenQuotaUsed 累加 PAT 已用配额（成功写入文件后记账）。
func (db *DB) AddAPITokenQuotaUsed(id string, bytes int64) error {
	_, err := db.conn.Exec(`UPDATE api_tokens SET quota_used = quota_used + ? WHERE id = ?`, bytes, id)
	return err
}

// SetAPITokenQuotaUsed 直接设置 PAT 已用配额（幂等恢复用）。
func (db *DB) SetAPITokenQuotaUsed(id string, bytes int64) error {
	_, err := db.conn.Exec(`UPDATE api_tokens SET quota_used = ? WHERE id = ?`, bytes, id)
	return err
}

// SumAPITokenQuotaUsed 返回指定用户所有未吊销 PAT 的配额使用合计（admin 传 ""）。
func (db *DB) SumAPITokenQuotaUsed(userID string) (int64, error) {
	var total int64
	var err error
	if userID == "" {
		err = db.conn.QueryRow(`SELECT COALESCE(SUM(quota_used),0) FROM api_tokens WHERE revoked_at IS NULL`).Scan(&total)
	} else {
		err = db.conn.QueryRow(
			`SELECT COALESCE(SUM(quota_used),0) FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL`, userID).Scan(&total)
	}
	return total, err
}

// === 时间辅助（与其它 repo 保持一致的空值处理） ===

func nullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

func parseNullableTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, ns.String)
	if err != nil {
		return nil
	}
	return &t
}
