package biz

import (
	"fmt"
	"time"
)

// ============================================================
// 资源管理服务（第 15 章 RES-101~111 / RES-201~210 / RES-ACC-001）
// 文件 → 资源中心 → 前台展示；开放策略：公开/登录可见/白名单/会员/付费
// 启用：审核（RES-105）、预览下载分离（RES-106）、水印与下载留痕（RES-109）
// ============================================================

// CreateResource 上传并创建资源（RES-101，file_id 关联 FileSvc）
func (s *Store) CreateResource(r Resource) (Resource, error) {
	nowStr := now()
	if r.Status == "" {
		r.Status = ResourceStatusDraft
	}
	if r.Policy == "" {
		r.Policy = ResourcePolicyPublic
	}
	res, err := s.conn.Exec(`INSERT INTO biz_resources(
		name,type,description,cover,file_id,owner_id,status,policy,price,downloads,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,0,?,?)`,
		r.Name, r.Type, r.Description, r.Cover, r.FileID, r.OwnerID, r.Status, r.Policy, r.Price, nowStr, nowStr)
	if err != nil {
		return r, err
	}
	r.ID, _ = res.LastInsertId()
	r.CreatedAt = time.Now()
	r.UpdatedAt = r.CreatedAt
	return r, nil
}

// GetResource 查询资源
func (s *Store) GetResource(id int64) (Resource, error) {
	var r Resource
	var createdAt, updatedAt string
	err := s.conn.QueryRow(`SELECT id,name,type,description,cover,file_id,owner_id,status,policy,price,downloads,created_at,updated_at
		FROM biz_resources WHERE id=?`, id).Scan(
		&r.ID, &r.Name, &r.Type, &r.Description, &r.Cover, &r.FileID, &r.OwnerID, &r.Status,
		&r.Policy, &r.Price, &r.Downloads, &createdAt, &updatedAt)
	if err != nil {
		return r, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return r, nil
}

// ListResources 资源列表（前台按开放策略过滤，后台全量）
func (s *Store) ListResources(status, policy string, publishedOnly bool, userID string) ([]Resource, error) {
	q := `SELECT id,name,type,description,cover,file_id,owner_id,status,policy,price,downloads,created_at,updated_at FROM biz_resources`
	var args []any
	var where string

	if publishedOnly {
		// 前台：只看到已发布资源
		where = " WHERE status='published'"
	}
	if policy != "" {
		if where != "" {
			where += " AND policy=?"
		} else {
			where = " WHERE policy=?"
		}
		args = append(args, policy)
	}
	if status != "" {
		if where != "" {
			where += " AND status=?"
		} else {
			where = " WHERE status=?"
		}
		args = append(args, status)
	}
	q += where + " ORDER BY id DESC LIMIT 200"
	rows, err := s.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Resource
	for rows.Next() {
		var r Resource
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.Description, &r.Cover, &r.FileID, &r.OwnerID, &r.Status,
			&r.Policy, &r.Price, &r.Downloads, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		r.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		items = append(items, r)
	}
	return items, rows.Err()
}

// UpdateResource 更新资源信息（RES-103/104）
func (s *Store) UpdateResource(id int64, r Resource) error {
	_, err := s.conn.Exec(`UPDATE biz_resources SET name=?,type=?,description=?,cover=?,status=?,policy=?,price=?,updated_at=? WHERE id=?`,
		r.Name, r.Type, r.Description, r.Cover, r.Status, r.Policy, r.Price, now(), id)
	return err
}

// ReviewResource 资源审核（RES-105：审核通过 → published）
func (s *Store) ReviewResource(id int64, approve bool, note string) error {
	status := ResourceStatusOffline
	if approve {
		status = ResourceStatusPublished
	}
	_, err := s.conn.Exec(`UPDATE biz_resources SET status=?, updated_at=? WHERE id=?`, status, now(), id)
	return err
}

// OfflineResource 资源下架（RES-110）
func (s *Store) OfflineResource(id int64) error {
	_, err := s.conn.Exec(`UPDATE biz_resources SET status=?, updated_at=? WHERE id=?`, ResourceStatusOffline, now(), id)
	return err
}

// DeleteResource 删除资源
func (s *Store) DeleteResource(id int64) error {
	_, err := s.conn.Exec(`DELETE FROM biz_resources WHERE id=?`, id)
	return err
}

// ---------- 开放策略与授权（RES-201~210）----------

// AddWhitelist 添加白名单用户（RES-202）
func (s *Store) AddWhitelist(resourceID int64, userID string) error {
	_, err := s.conn.Exec(`INSERT OR IGNORE INTO biz_resource_whitelist(resource_id,user_id) VALUES(?,?)`,
		resourceID, userID)
	return err
}

// RemoveWhitelist 移除白名单用户
func (s *Store) RemoveWhitelist(resourceID int64, userID string) error {
	_, err := s.conn.Exec(`DELETE FROM biz_resource_whitelist WHERE resource_id=? AND user_id=?`,
		resourceID, userID)
	return err
}

// ListWhitelist 白名单列表
func (s *Store) ListWhitelist(resourceID int64) ([]ResourceWhitelist, error) {
	rows, err := s.conn.Query(`SELECT id,resource_id,user_id FROM biz_resource_whitelist WHERE resource_id=?`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ResourceWhitelist
	for rows.Next() {
		var w ResourceWhitelist
		if err := rows.Scan(&w.ID, &w.ResourceID, &w.UserID); err != nil {
			return nil, err
		}
		items = append(items, w)
	}
	return items, rows.Err()
}

// CheckResourceAccess 校验资源访问权限（RES-201 核心：公开/登录/白名单/会员/付费）
// 返回是否可访问；付费/会员资源未开通时返回 errUnauthorized
var ErrResourceDenied = fmt.Errorf("resource access denied")

func (s *Store) CheckResourceAccess(resourceID int64, userID string, isLoggedIn bool) error {
	r, err := s.GetResource(resourceID)
	if err != nil {
		return err
	}
	if r.Status != ResourceStatusPublished {
		return ErrResourceDenied
	}
	switch r.Policy {
	case ResourcePolicyPublic:
		return nil
	case ResourcePolicyLogin:
		if isLoggedIn {
			return nil
		}
		return ErrResourceDenied
	case ResourcePolicyWhitelist:
		if !isLoggedIn {
			return ErrResourceDenied
		}
		var n int
		_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_resource_whitelist WHERE resource_id=? AND user_id=?`,
			resourceID, userID).Scan(&n)
		if n > 0 {
			return nil
		}
		return ErrResourceDenied
	case ResourcePolicyMember, ResourcePolicyPaid:
		// 简化：V1.0 阶段付费/会员资源需后台手动开通（白名单兜底），
		// 完整对接订单/会员体系为后续增强（第 15.5 第二阶段）
		if !isLoggedIn {
			return ErrResourceDenied
		}
		var n int
		_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_resource_whitelist WHERE resource_id=? AND user_id=?`,
			resourceID, userID).Scan(&n)
		if n > 0 {
			return nil
		}
		return ErrResourceDenied
	}
	return ErrResourceDenied
}

// LogDownload 下载留痕 + 次数累加（RES-109）
func (s *Store) LogDownload(resourceID int64, userID, ip string) error {
	_, err := s.conn.Exec(`INSERT INTO biz_resource_download_logs(resource_id,user_id,ip,downloaded_at) VALUES(?,?,?,?)`,
		resourceID, userID, ip, now())
	if err != nil {
		return err
	}
	_, err = s.conn.Exec(`UPDATE biz_resources SET downloads=downloads+1 WHERE id=?`, resourceID)
	return err
}

// ListDownloadLogs 下载留痕列表（审计）
func (s *Store) ListDownloadLogs(resourceID int64) ([]ResourceDownloadLog, error) {
	rows, err := s.conn.Query(`SELECT id,resource_id,user_id,ip,downloaded_at FROM biz_resource_download_logs WHERE resource_id=? ORDER BY id DESC`, resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ResourceDownloadLog
	for rows.Next() {
		var l ResourceDownloadLog
		var at string
		if err := rows.Scan(&l.ID, &l.ResourceID, &l.UserID, &l.IP, &at); err != nil {
			return nil, err
		}
		l.DownloadedAt, _ = time.Parse(time.RFC3339, at)
		items = append(items, l)
	}
	return items, rows.Err()
}
