package store

import (
	"database/sql"
	"time"

	"filesync/internal/model"
)

// === Upload Event（上传事件历史）仓储 ===
// 记录 MCP / 外部上传事件，供前端传输中心历史回溯。
// 表结构见 migrate.go 的 upload_events 建表语句。

// CreateUploadEvent 写入一条上传事件记录。
func (db *DB) CreateUploadEvent(ev *model.UploadEvent) error {
	if ev.CreatedAt == "" {
		ev.CreatedAt = time.Now().Format(time.RFC3339)
	}
	_, err := db.conn.Exec(
		`INSERT INTO upload_events (username, filename, size, space_id, tool, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.Username, ev.Filename, ev.Size, ev.SpaceID, ev.Tool, ev.CreatedAt,
	)
	return err
}

// ListUploadEvents 按用户返回最近 N 条上传事件（默认 100）。
// 供传输中心打开时回填历史。
func (db *DB) ListUploadEvents(username string, limit int) ([]model.UploadEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.conn.Query(
		`SELECT id, username, filename, size, space_id, tool, created_at
		 FROM upload_events WHERE username = ?
		 ORDER BY id DESC LIMIT ?`,
		username, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]model.UploadEvent, 0, limit)
	for rows.Next() {
		var ev model.UploadEvent
		var size sql.NullInt64
		if err := rows.Scan(&ev.ID, &ev.Username, &ev.Filename, &size,
			&ev.SpaceID, &ev.Tool, &ev.CreatedAt); err != nil {
			return nil, err
		}
		ev.Size = size.Int64
		events = append(events, ev)
	}
	return events, rows.Err()
}

// ClearUploadEvents 清空指定用户的上传事件历史（返回删除条数）。
func (db *DB) ClearUploadEvents(username string) (int64, error) {
	res, err := db.conn.Exec(
		`DELETE FROM upload_events WHERE username = ?`, username)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
