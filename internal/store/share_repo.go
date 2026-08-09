package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

func (db *DB) ListAllShares() ([]*model.Share, error) {
	rows, err := db.conn.Query(
		`SELECT id, file_id, dir_prefix, share_type, created_by, space_id, created_at, expires_at, download_count, max_downloads, is_active, password_hash
		 FROM shares ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shares []*model.Share
	for rows.Next() {
		var s model.Share
		var fileID, dirPrefix, spaceID, expiresAtStr, passwordHash sql.NullString
		var maxDownloads sql.NullInt64
		var isActive int
		var createdAtStr string
		if err := rows.Scan(&s.ID, &fileID, &dirPrefix, &s.ShareType, &s.CreatedBy, &spaceID, &createdAtStr, &expiresAtStr, &s.DownloadCount, &maxDownloads, &isActive, &passwordHash); err != nil {
			return nil, err
		}
		s.FileID = fileID.String
		s.DirPrefix = dirPrefix.String
		s.SpaceID = spaceID.String
		if expiresAtStr.Valid {
			t, _ := time.Parse(time.RFC3339, expiresAtStr.String)
			s.ExpiresAt = &t
		}
		if maxDownloads.Valid {
			md := int(maxDownloads.Int64)
			s.MaxDownloads = &md
		}
		s.IsActive = isActive != 0
		s.PasswordHash = passwordHash.String
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		shares = append(shares, &s)
	}
	return shares, rows.Err()
}

// CreateShare 创建分享记录。s.ID 由调用方生成（8 字符短 ID）。
// password_hash 为空字符串表示无密码；非空为 bcrypt 哈希值。
func (db *DB) CreateShare(s *model.Share) error {
	var expiresAt interface{}
	if s.ExpiresAt != nil {
		expiresAt = s.ExpiresAt.Format(time.RFC3339)
	}
	var maxDownloads interface{}
	if s.MaxDownloads != nil {
		maxDownloads = *s.MaxDownloads
	}
	isActive := 0
	if s.IsActive {
		isActive = 1
	}
	var passwordHash interface{}
	if s.PasswordHash != "" {
		passwordHash = s.PasswordHash
	}
	_, err := db.conn.Exec(
		`INSERT INTO shares (id, file_id, dir_prefix, share_type, created_by, space_id, created_at, expires_at, download_count, max_downloads, is_active, password_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, nullableString(s.FileID), nullableString(s.DirPrefix), s.ShareType,
		s.CreatedBy, nullableString(s.SpaceID), s.CreatedAt.Format(time.RFC3339), expiresAt, s.DownloadCount, maxDownloads, isActive, passwordHash,
	)
	return err
}

// GetShare 按 ID 查询分享。不存在返回 sql.ErrNoRows。
func (db *DB) GetShare(id string) (*model.Share, error) {
	var s model.Share
	var fileID, dirPrefix, expiresAtStr, passwordHash sql.NullString
	var maxDownloads sql.NullInt64
	var isActive int
	var createdAtStr string
	var spaceID sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, file_id, dir_prefix, share_type, created_by, space_id, created_at, expires_at, download_count, max_downloads, is_active, password_hash
		 FROM shares WHERE id = ?`,
		id,
	).Scan(&s.ID, &fileID, &dirPrefix, &s.ShareType, &s.CreatedBy, &spaceID, &createdAtStr, &expiresAtStr, &s.DownloadCount, &maxDownloads, &isActive, &passwordHash)
	if err != nil {
		return nil, err
	}
	s.FileID = fileID.String
	s.DirPrefix = dirPrefix.String
	s.SpaceID = spaceID.String
	if expiresAtStr.Valid {
		t, _ := time.Parse(time.RFC3339, expiresAtStr.String)
		s.ExpiresAt = &t
	}
	if maxDownloads.Valid {
		md := int(maxDownloads.Int64)
		s.MaxDownloads = &md
	}
	s.IsActive = isActive != 0
	s.PasswordHash = passwordHash.String
	s.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
	return &s, nil
}

// ListSharesByCreator 查询指定用户创建的所有分享（按创建时间倒序）。
func (db *DB) ListSharesByCreator(username string) ([]*model.Share, error) {
	rows, err := db.conn.Query(
		`SELECT id, file_id, dir_prefix, share_type, created_by, space_id, created_at, expires_at, download_count, max_downloads, is_active, password_hash
		 FROM shares WHERE created_by = ? ORDER BY created_at DESC`,
		username,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shares []*model.Share
	for rows.Next() {
		var s model.Share
		var fileID, dirPrefix, spaceID, expiresAtStr, passwordHash sql.NullString
		var maxDownloads sql.NullInt64
		var isActive int
		var createdAtStr string
		if err := rows.Scan(&s.ID, &fileID, &dirPrefix, &s.ShareType, &s.CreatedBy, &spaceID, &createdAtStr, &expiresAtStr, &s.DownloadCount, &maxDownloads, &isActive, &passwordHash); err != nil {
			return nil, err
		}
		s.FileID = fileID.String
		s.DirPrefix = dirPrefix.String
		s.SpaceID = spaceID.String
		if expiresAtStr.Valid {
			t, _ := time.Parse(time.RFC3339, expiresAtStr.String)
			s.ExpiresAt = &t
		}
		if maxDownloads.Valid {
			md := int(maxDownloads.Int64)
			s.MaxDownloads = &md
		}
		s.IsActive = isActive != 0
		s.PasswordHash = passwordHash.String
		s.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		shares = append(shares, &s)
	}
	return shares, rows.Err()
}

// DeleteShare 删除分享记录（调用方应先验证所有权）。
func (db *DB) DeleteShare(id string) error {
	_, err := db.conn.Exec(`DELETE FROM shares WHERE id = ?`, id)
	return err
}

// IncrementShareDownload 记录一次下载并增加计数。
// 利用 UNIQUE(share_id, visitor_id) 约束实现幂等：
//   - 新访客：INSERT 成功，RowsAffected=1，更新 shares.download_count + 1
//   - 老访客：INSERT 被忽略，RowsAffected=0，不增加计数
func (db *DB) IncrementShareDownload(shareID, visitorID, ip, userAgent string) error {
	now := time.Now().Format(time.RFC3339)
	res, err := db.conn.Exec(
		db.insertIgnorePrefix()+` INTO share_downloads (share_id, visitor_id, ip, user_agent, downloaded_at)
		 VALUES (?, ?, ?, ?, ?)`,
		shareID, visitorID, ip, userAgent, now,
	)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected > 0 {
		// 新访客下载，增加计数
		_, err = db.conn.Exec(
			`UPDATE shares SET download_count = download_count + 1 WHERE id = ?`,
			shareID,
		)
	}
	return err
}
