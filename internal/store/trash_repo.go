package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

func (db *DB) DeleteFile(id string) (int64, error) {
	res, err := db.conn.Exec(
		`UPDATE files SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().Format(time.RFC3339), id,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteFilesByPrefix 软删除指定前缀下所有文件（递归匹配子目录，移入回收站）。返回删除文件列表。
// 路径枚举方案：用 LIKE 'prefix%' ESCAPE '|' 匹配。调用方不再需要删除存储文件（软删除保留存储）。
// spaceID 语义见 spaceFilter（""=默认空间，"*"=不过滤，其他=指定空间）。
func (db *DB) DeleteFilesByPrefix(spaceID, prefix, owner string) ([]model.FileRecord, error) {
	// 先查询出待删除文件（返回给调用方用于 UI 反馈）
	files, err := db.ListFiles(spaceID, prefix, owner)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return files, nil
	}
	// 批量软删除（事务）
	now := time.Now().Format(time.RFC3339)
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, err := tx.Exec(
			`UPDATE files SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
			now, f.ID,
		); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return files, nil
}

// ListTrashedFiles 列出回收站中的文件（deleted_at IS NOT NULL）。
// owner 非空时按归属用户过滤（用户隔离）；owner 为空时返回所有用户的回收站文件（admin 用）。
// spaceID 语义见 spaceFilter（""=默认空间，"*"=不过滤，其他=指定空间）。
// 按 deleted_at DESC 排序（最近删除的在前）。
func (db *DB) ListTrashedFiles(spaceID, owner string) ([]model.FileRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	filter, sfArgs := spaceFilter(spaceID)
	cols := `SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, space_id, created_at, updated_at, deleted_at
		     FROM files WHERE deleted_at IS NOT NULL` + filter
	if owner == "" {
		rows, err = db.conn.Query(cols+` ORDER BY deleted_at DESC`, sfArgs...)
	} else {
		rows, err = db.conn.Query(cols+` AND owner = ? ORDER BY deleted_at DESC`, append(sfArgs, owner)...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.FileRecord
	for rows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt, deletedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &f.SpaceID, &createdAt, &updatedAt, &deletedAt); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		if t, err := time.Parse(time.RFC3339, deletedAt); err == nil {
			f.DeletedAt = &t
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// RestoreFile 恢复回收站中的文件（SET deleted_at = NULL）。
// owner 非空时加 WHERE owner = ? 条件防止越权恢复他人文件；为空时不过滤（admin 用）。
// 返回受影响行数（0 表示文件不存在或不属于该用户）。
func (db *DB) RestoreFile(id, owner string) (int64, error) {
	var res sql.Result
	var err error
	if owner == "" {
		res, err = db.conn.Exec(
			`UPDATE files SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
			time.Now().Format(time.RFC3339), id,
		)
	} else {
		res, err = db.conn.Exec(
			`UPDATE files SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL AND owner = ?`,
			time.Now().Format(time.RFC3339), id, owner,
		)
	}
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PermanentlyDeleteFile 物理删除回收站中的文件（DELETE FROM files）。
// 仅允许删除已软删除的文件（deleted_at IS NOT NULL），防止误删正常文件。
// owner 非空时加 WHERE owner = ? 条件防止越权删除他人文件；为空时不过滤（admin 用）。
// 返回文件记录（调用方需要 storage_path 删除存储文件）和受影响行数。
func (db *DB) PermanentlyDeleteFile(id, owner string) (*model.FileRecord, int64, error) {
	// 先查询文件记录（获取 storage_path 用于删除存储）
	var row *sql.Row
	if owner == "" {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
			 FROM files WHERE id = ? AND deleted_at IS NOT NULL`, id)
	} else {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
			 FROM files WHERE id = ? AND deleted_at IS NOT NULL AND owner = ?`, id, owner)
	}
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, 0, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	// 物理删除数据库记录
	res, err := db.conn.Exec(`DELETE FROM files WHERE id = ? AND deleted_at IS NOT NULL`, id)
	if err != nil {
		return nil, 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, 0, err
	}
	return f, affected, nil
}

// CountByStoragePath 统计引用同一 storage_path 的记录数（全局存储引用计数）。
// 用于永久删除时判断是否可以删除物理文件：count > 0 说明还有其他记录引用，不能删物理文件。
// 统计范围：所有未永久删除的记录（包括回收站中的软删除记录，因为恢复后还需要物理文件）。
func (db *DB) CountByStoragePath(storagePath string) (int64, error) {
	var count int64
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM files WHERE storage_path = ?`, storagePath).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CleanupExpiredTrash 清理回收站中超过保留期的文件（物理删除数据库记录 + 删除存储文件）。
// retentionDays 为保留天数（默认 30 天）。返回清理的文件数量和错误。
// 此方法在服务启动时自动调用，避免回收站无限膨胀。
// 注意：调用方需要传入 storage.Storage 实例用于删除存储文件，此处仅返回待清理的文件列表。
func (db *DB) CleanupExpiredTrash(retentionDays int) ([]model.FileRecord, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format(time.RFC3339)

	// 查询过期文件（deleted_at < cutoff）
	rows, err := db.conn.Query(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
		 FROM files WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	var expired []model.FileRecord
	for rows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		expired = append(expired, f)
	}
	rows.Close()

	if len(expired) == 0 {
		return expired, nil
	}

	// 批量物理删除数据库记录
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	for _, f := range expired {
		if _, err := tx.Exec(`DELETE FROM files WHERE id = ? AND deleted_at IS NOT NULL`, f.ID); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return expired, nil
}
