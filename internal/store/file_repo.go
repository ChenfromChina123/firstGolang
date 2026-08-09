package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"filesync/internal/model"
)

// CreateFile records a completed file
func (db *DB) CreateFile(f *model.FileRecord) error {
	_, err := db.conn.Exec(
		`INSERT INTO files (id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, space_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Filename, f.Size, f.Hash, f.StoragePath, f.StorageType,
		f.ChunkSize, f.TotalChunks, f.Status, f.Owner, f.SpaceID,
		f.CreatedAt.Format(time.RFC3339), f.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

// GetFile retrieves a file record by ID（仅返回未删除的文件，回收站文件不可访问）
func (db *DB) GetFile(id string) (*model.FileRecord, error) {
	row := db.conn.QueryRow(
		`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, space_id, created_at, updated_at
		 FROM files WHERE id = ? AND deleted_at IS NULL`, id,
	)
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &f.SpaceID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// FindFileByName checks if a file with the given name exists (filtered by owner + space)
// spaceID 语义见 spaceFilter（""=默认空间，"*"=不过滤，其他=指定空间）。
// owner 为空时不过滤（兼容旧调用方），非空时加 WHERE owner = ? 条件
// 仅返回未删除的文件（deleted_at IS NULL），回收站文件不参与冲突检测
func (db *DB) FindFileByName(spaceID, filename, owner string) (*model.FileRecord, error) {
	filter, args := spaceFilter(spaceID)
	base := `SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, space_id, created_at, updated_at
		 FROM files WHERE filename = ? AND status = 'completed' AND deleted_at IS NULL` + filter
	if owner == "" {
		args = append([]interface{}{filename}, args...)
	} else {
		base += ` AND owner = ?`
		args = append([]interface{}{filename}, args...)
		args = append(args, owner)
	}
	base += ` ORDER BY created_at DESC LIMIT 1`
	row := db.conn.QueryRow(base, args...)
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &f.SpaceID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// GetFileByHash 按 hash + size 查询已完成文件（秒传检查用）。
// hash 必须非空（完整 SHA256 hex，64 字符）。
// owner 非空时按用户过滤（仅查自己的）；owner 为空时全局查找（跨用户秒传）。
// 返回 sql.ErrNoRows 时表示未命中（调用方据此判断是否秒传）。
// 校验条件：hash 匹配 + size 匹配 + status='completed'，三重校验避免误判。
func (db *DB) GetFileByHash(hash, owner string, fileSize int64) (*model.FileRecord, error) {
	var row *sql.Row
	if owner == "" {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
		 FROM files WHERE hash = ? AND size = ? AND status = 'completed' AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, hash, fileSize)
	} else {
		row = db.conn.QueryRow(
			`SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
		 FROM files WHERE hash = ? AND size = ? AND status = 'completed' AND owner = ? AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`, hash, fileSize, owner)
	}
	f := &model.FileRecord{}
	var createdAt, updatedAt string
	err := row.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
		&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return f, nil
}

// ListFiles returns all completed files
// ListFiles 返回所有已完成文件。prefix 非空时按路径前缀过滤（递归匹配子目录）。
// owner 非空时按归属用户过滤（用户隔离）；owner 为空时返回所有用户的文件（兼容旧调用方）。
// spaceID 语义见 spaceFilter（""=默认空间，"*"=不过滤，其他=指定空间）。
// 路径枚举方案：filename 中的 "/" 作为虚拟目录分隔符，无需单独 directories 表。
func (db *DB) ListFiles(spaceID, prefix, owner string) ([]model.FileRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	filter, sfArgs := spaceFilter(spaceID)
	cols := `SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, space_id, created_at, updated_at
		     FROM files WHERE status = 'completed' AND deleted_at IS NULL` + filter
	switch {
	case prefix == "" && owner == "":
		rows, err = db.conn.Query(cols+` ORDER BY created_at DESC`, sfArgs...)
	case prefix == "" && owner != "":
		rows, err = db.conn.Query(cols+` AND owner = ? ORDER BY created_at DESC`, append(sfArgs, owner)...)
	case prefix != "" && owner == "":
		// ESCAPE '|': 用 | 作为 LIKE 转义符，避免 MySQL 对 '\' 的歧义解析
		rows, err = db.conn.Query(cols+` AND filename LIKE ? ESCAPE '|' ORDER BY created_at DESC`,
			append(sfArgs, escapeLikePrefix(prefix)+"%")...)
	default:
		rows, err = db.conn.Query(cols+` AND filename LIKE ? ESCAPE '|' AND owner = ? ORDER BY created_at DESC`,
			append(sfArgs, escapeLikePrefix(prefix)+"%", owner)...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.FileRecord
	for rows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &f.SpaceID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		files = append(files, f)
	}
	return files, rows.Err()
}

// ListDir 按目录分层返回当前目录的直接子项（子目录 + 直接文件），不递归子目录。
// prefix 为空时返回根目录子项。owner 非空时按归属用户过滤（用户隔离）。
// spaceID 语义见 spaceFilter（""=默认空间，"*"=不过滤，其他=指定空间）。
// dirs 为子目录列表（含递归文件数，排除 .keep），files 为当前目录的直接文件列表。
// 相比 ListFiles 的递归返回所有文件，分层返回显著减少数据量（根目录从 1319 文件降到几十项）。
func (db *DB) ListDir(spaceID, prefix, owner string) (dirs []model.DirEntry, files []model.FileRecord, err error) {
	filter, sfArgs := spaceFilter(spaceID)
	cols := `SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, space_id, created_at, updated_at
		     FROM files WHERE status = 'completed' AND deleted_at IS NULL` + filter
	escapedPrefix := escapeLikePrefix(prefix)

	// 1. 查询直接文件（filename 不含更多 /，即去掉 prefix 后没有 /）
	var fileRows *sql.Rows
	if prefix == "" {
		// 根目录：filename 不含 /
		keepName := ".keep"
		if owner == "" {
			fileRows, err = db.conn.Query(cols+` AND filename NOT LIKE '%/%' AND filename != ? ORDER BY created_at DESC`, append(sfArgs, keepName)...)
		} else {
			fileRows, err = db.conn.Query(cols+` AND filename NOT LIKE '%/%' AND filename != ? AND owner = ? ORDER BY created_at DESC`, append(sfArgs, keepName, owner)...)
		}
	} else {
		// 子目录：filename LIKE 'prefix%' AND filename NOT LIKE 'prefix%/%'
		likePattern := escapedPrefix + "%"
		notLikePattern := escapedPrefix + "%/%"
		keepName := prefix + ".keep"
		if owner == "" {
			fileRows, err = db.conn.Query(cols+` AND filename LIKE ? ESCAPE '|' AND filename NOT LIKE ? ESCAPE '|' AND filename != ? ORDER BY created_at DESC`,
				append(sfArgs, likePattern, notLikePattern, keepName)...)
		} else {
			fileRows, err = db.conn.Query(cols+` AND filename LIKE ? ESCAPE '|' AND filename NOT LIKE ? ESCAPE '|' AND filename != ? AND owner = ? ORDER BY created_at DESC`,
				append(sfArgs, likePattern, notLikePattern, keepName, owner)...)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	defer fileRows.Close()

	for fileRows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt string
		if e := fileRows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &f.SpaceID, &createdAt, &updatedAt); e != nil {
			return nil, nil, e
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		files = append(files, f)
	}
	if e := fileRows.Err(); e != nil {
		return nil, nil, e
	}

	// 2. 查询子目录名和递归文件数（GROUP BY 提取第一级目录名）
	// startPos 必须是【字符数】而非字节数（prefixLen = len() 是字节数）：
	// SQLite SUBSTR/INSTR 按字符位置计算，中文等多字节字符会导致字节/字符错位，
	// 例如 prefix="测试/"（len=7 字节）时 startPos=8 会从错误位置截取，产生空目录名。
	// startPos = 字符数 + 1（SQL SUBSTR 是 1-based）
	startPos := utf8.RuneCountInString(prefix) + 1
	dirBase := `FROM files WHERE status = 'completed' AND deleted_at IS NULL` + filter
	var dirRows *sql.Rows
	if prefix == "" {
		// 根目录：LIKE '%/%'
		if owner == "" {
			dirRows, err = db.conn.Query(`SELECT SUBSTR(filename, 1, INSTR(filename, '/') - 1) AS dir_name, COUNT(CASE WHEN filename NOT LIKE '%/.keep' THEN 1 END) AS cnt
				`+dirBase+`
				AND filename LIKE '%/%'
				GROUP BY dir_name ORDER BY dir_name`, sfArgs...)
		} else {
			dirRows, err = db.conn.Query(`SELECT SUBSTR(filename, 1, INSTR(filename, '/') - 1) AS dir_name, COUNT(CASE WHEN filename NOT LIKE '%/.keep' THEN 1 END) AS cnt
				`+dirBase+`
				AND filename LIKE '%/%' AND owner = ?
				GROUP BY dir_name ORDER BY dir_name`, append(sfArgs, owner)...)
		}
	} else {
		// 子目录：LIKE 'prefix%/%'
		likePattern := escapedPrefix + "%/%"
		if owner == "" {
			args := make([]interface{}, 0, 6)
			args = append(args, startPos, startPos)
			args = append(args, sfArgs...)
			args = append(args, likePattern)
			dirRows, err = db.conn.Query(`SELECT SUBSTR(SUBSTR(filename, ?), 1, INSTR(SUBSTR(filename, ?), '/') - 1) AS dir_name, COUNT(CASE WHEN filename NOT LIKE '%/.keep' THEN 1 END) AS cnt
				`+dirBase+`
				AND filename LIKE ? ESCAPE '|'
				GROUP BY dir_name ORDER BY dir_name`, args...)
		} else {
			args := make([]interface{}, 0, 6)
			args = append(args, startPos, startPos)
			args = append(args, sfArgs...)
			args = append(args, likePattern, owner)
			dirRows, err = db.conn.Query(`SELECT SUBSTR(SUBSTR(filename, ?), 1, INSTR(SUBSTR(filename, ?), '/') - 1) AS dir_name, COUNT(CASE WHEN filename NOT LIKE '%/.keep' THEN 1 END) AS cnt
				`+dirBase+`
				AND filename LIKE ? ESCAPE '|' AND owner = ?
				GROUP BY dir_name ORDER BY dir_name`, args...)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	defer dirRows.Close()

	dirs = []model.DirEntry{}
	for dirRows.Next() {
		var d model.DirEntry
		if e := dirRows.Scan(&d.Name, &d.Count); e != nil {
			return nil, nil, e
		}
		dirs = append(dirs, d)
	}
	if e := dirRows.Err(); e != nil {
		return nil, nil, e
	}

	return dirs, files, nil
}

// ListRecentVideoFiles 列出近 N 天的视频文件（按文件名后缀过滤，仅 local 存储）。
// 用于预转码 worker 扫描待转码视频。days<=0 时默认30天。
// 视频后缀集合与 handler.getFileType 保持一致：mp4/webm/mkv/avi/mov。
func (db *DB) ListRecentVideoFiles(days int) ([]model.FileRecord, error) {
	if days <= 0 {
		days = 30
	}
	since := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	const query = `SELECT id, filename, size, hash, storage_path, storage_type, chunk_size, total_chunks, status, owner, created_at, updated_at
                   FROM files WHERE status = 'completed' AND deleted_at IS NULL
                   AND storage_type = 'local' AND created_at >= ?
                   ORDER BY created_at DESC`
	rows, err := db.conn.Query(query, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.FileRecord
	// 视频后缀集合（与 handler.getFileType 的 video 分支一致）
	videoExts := map[string]bool{
		".mp4": true, ".webm": true, ".mkv": true, ".avi": true, ".mov": true,
	}
	for rows.Next() {
		var f model.FileRecord
		var createdAt, updatedAt string
		if err := rows.Scan(&f.ID, &f.Filename, &f.Size, &f.Hash, &f.StoragePath,
			&f.StorageType, &f.ChunkSize, &f.TotalChunks, &f.Status, &f.Owner, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		f.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		f.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		// Go 代码过滤视频类型（SQL 不方便做后缀匹配）
		ext := strings.ToLower(filepath.Ext(f.Filename))
		if videoExts[ext] {
			files = append(files, f)
		}
	}
	return files, rows.Err()
}

// escapeLikePrefix 转义 LIKE 模式中的特殊字符（% _ |），防止前缀注入。
// 转义符使用 |（与 SQL 中的 ESCAPE '|' 对应），避免 MySQL 对 '\' 的歧义解析。
func escapeLikePrefix(s string) string {
	out := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' || c == '_' || c == '|' {
			out = append(out, '|')
		}
		out = append(out, c)
	}
	return string(out)
}

// UpdateFilename 更新文件名（含路径前缀），用于重命名/移动。
// newFilename 必须通过 validateFilePath 校验（由 handler 层保证）。
// owner 非空时加 WHERE owner = ? 条件防止越权改他人文件名；为空时不过滤（兼容旧调用方）。
func (db *DB) UpdateFilename(id, newFilename, owner string) error {
	if owner == "" {
		_, err := db.conn.Exec(
			`UPDATE files SET filename = ?, updated_at = ? WHERE id = ?`,
			newFilename, time.Now().Format(time.RFC3339), id,
		)
		return err
	}
	_, err := db.conn.Exec(
		`UPDATE files SET filename = ?, updated_at = ? WHERE id = ? AND owner = ?`,
		newFilename, time.Now().Format(time.RFC3339), id, owner,
	)
	return err
}

// UpdateFileMeta 更新文件的 size/hash/updated_at（用于在线编辑保存内容）。
// owner 为空时表示 admin 操作，不限制 owner；非空时仅更新 owner 匹配的记录。
func (db *DB) UpdateFileMeta(id string, size int64, hash, owner string) error {
	if owner == "" {
		_, err := db.conn.Exec(
			`UPDATE files SET size = ?, hash = ?, updated_at = ? WHERE id = ?`,
			size, hash, time.Now().Format(time.RFC3339), id,
		)
		return err
	}
	_, err := db.conn.Exec(
		`UPDATE files SET size = ?, hash = ?, updated_at = ? WHERE id = ? AND owner = ?`,
		size, hash, time.Now().Format(time.RFC3339), id, owner,
	)
	return err
}
