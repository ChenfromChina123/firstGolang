package biz

import (
	"time"
)

// ============================================================
// 服务管理服务（第 16 章 SRV-101~108 / BIZ-001）
// 服务目录 → 服务商品(SKU) → 订单 → 服务单 → 交付(里程碑) → 验收 → 评价/售后
// 状态机：待确认 → 待分配 → 交付中 → 待验收 → 完成 → 取消
// ============================================================

// ---------- 服务目录（SRV-101）----------

// CreateServiceCatalog 创建服务目录项
func (s *Store) CreateServiceCatalog(c ServiceCatalog) (ServiceCatalog, error) {
	if c.Status == "" {
		c.Status = "active"
	}
	res, err := s.conn.Exec(`INSERT INTO biz_service_catalogs(name,category,description,price,cycle,deliverable,sla,status)
		VALUES(?,?,?,?,?,?,?,?)`,
		c.Name, c.Category, c.Description, c.Price, c.Cycle, c.Deliverable, c.SLA, c.Status)
	if err != nil {
		return c, err
	}
	c.ID, _ = res.LastInsertId()
	return c, nil
}

// ListServiceCatalogs 服务目录列表（前台看 active）
func (s *Store) ListServiceCatalogs(activeOnly bool) ([]ServiceCatalog, error) {
	q := `SELECT id,name,category,description,price,cycle,deliverable,sla,status FROM biz_service_catalogs`
	if activeOnly {
		q += " WHERE status='active'"
	}
	q += " ORDER BY id"
	rows, err := s.conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ServiceCatalog
	for rows.Next() {
		var c ServiceCatalog
		if err := rows.Scan(&c.ID, &c.Name, &c.Category, &c.Description, &c.Price, &c.Cycle, &c.Deliverable, &c.SLA, &c.Status); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

// GetServiceCatalog 查询服务目录
func (s *Store) GetServiceCatalog(id int64) (ServiceCatalog, error) {
	var c ServiceCatalog
	err := s.conn.QueryRow(`SELECT id,name,category,description,price,cycle,deliverable,sla,status FROM biz_service_catalogs WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.Category, &c.Description, &c.Price, &c.Cycle, &c.Deliverable, &c.SLA, &c.Status)
	return c, err
}

// UpdateServiceCatalog 更新服务目录
func (s *Store) UpdateServiceCatalog(id int64, c ServiceCatalog) error {
	_, err := s.conn.Exec(`UPDATE biz_service_catalogs SET name=?,category=?,description=?,price=?,cycle=?,deliverable=?,sla=?,status=? WHERE id=?`,
		c.Name, c.Category, c.Description, c.Price, c.Cycle, c.Deliverable, c.SLA, c.Status, id)
	return err
}

// ---------- 服务 SKU（SRV-102）----------

// AddServiceSKU 添加服务 SKU
func (s *Store) AddServiceSKU(sku ServiceSKU) (ServiceSKU, error) {
	res, err := s.conn.Exec(`INSERT INTO biz_service_skus(service_id,name,price,desc) VALUES(?,?,?,?)`,
		sku.ServiceID, sku.Name, sku.Price, sku.Desc)
	if err != nil {
		return sku, err
	}
	sku.ID, _ = res.LastInsertId()
	return sku, nil
}

// ListServiceSKUs 服务 SKU 列表
func (s *Store) ListServiceSKUs(serviceID int64) ([]ServiceSKU, error) {
	rows, err := s.conn.Query(`SELECT id,service_id,name,price,desc FROM biz_service_skus WHERE service_id=? ORDER BY id`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ServiceSKU
	for rows.Next() {
		var sku ServiceSKU
		if err := rows.Scan(&sku.ID, &sku.ServiceID, &sku.Name, &sku.Price, &sku.Desc); err != nil {
			return nil, err
		}
		items = append(items, sku)
	}
	return items, rows.Err()
}

// ---------- 服务单（SRV-103）----------

// CreateServiceOrder 创建服务单（用户购买服务 → 自动生成）
func (s *Store) CreateServiceOrder(so ServiceOrder) (ServiceOrder, error) {
	nowStr := now()
	so.SRVNo = genNo("SRV")
	if so.Status == "" {
		so.Status = SRVStatusWaitConfirm
	}
	res, err := s.conn.Exec(`INSERT INTO biz_service_orders(
		srv_no,order_id,service_id,sku_id,user_id,owner_id,status,requirement,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		so.SRVNo, so.OrderID, so.ServiceID, so.SKUID, so.UserID, so.OwnerID, so.Status, so.Requirement, nowStr, nowStr)
	if err != nil {
		return so, err
	}
	so.ID, _ = res.LastInsertId()
	so.CreatedAt = time.Now()
	so.UpdatedAt = so.CreatedAt
	return so, nil
}

// GetServiceOrder 查询服务单
func (s *Store) GetServiceOrder(id int64) (ServiceOrder, error) {
	var so ServiceOrder
	var createdAt, updatedAt string
	err := s.conn.QueryRow(`SELECT id,srv_no,order_id,service_id,sku_id,user_id,owner_id,status,requirement,created_at,updated_at
		FROM biz_service_orders WHERE id=?`, id).Scan(
		&so.ID, &so.SRVNo, &so.OrderID, &so.ServiceID, &so.SKUID, &so.UserID, &so.OwnerID, &so.Status,
		&so.Requirement, &createdAt, &updatedAt)
	if err != nil {
		return so, err
	}
	so.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	so.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return so, nil
}

// ListServiceOrders 服务单列表（mine=客户本人 / assignee=处理人 / 全量）
func (s *Store) ListServiceOrders(userID, status, ownerID string, mine bool) ([]ServiceOrder, error) {
	q := `SELECT id,srv_no,order_id,service_id,sku_id,user_id,owner_id,status,requirement,created_at,updated_at FROM biz_service_orders`
	var args []any
	var where string
	if mine && userID != "" {
		where = " WHERE user_id=?"
		args = append(args, userID)
	}
	if ownerID != "" {
		if where != "" {
			where += " AND owner_id=?"
		} else {
			where = " WHERE owner_id=?"
		}
		args = append(args, ownerID)
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
	var items []ServiceOrder
	for rows.Next() {
		var so ServiceOrder
		var createdAt, updatedAt string
		if err := rows.Scan(&so.ID, &so.SRVNo, &so.OrderID, &so.ServiceID, &so.SKUID, &so.UserID, &so.OwnerID, &so.Status,
			&so.Requirement, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		so.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		so.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		items = append(items, so)
	}
	return items, rows.Err()
}

// UpdateServiceOrderStatus 服务单状态流转（16.2 里程碑状态机）
func (s *Store) UpdateServiceOrderStatus(id int64, to, operator, note string) error {
	cur, err := s.GetServiceOrder(id)
	if err != nil {
		return err
	}
	if cur.Status == to {
		return nil
	}
	_, err = s.conn.Exec(`UPDATE biz_service_orders SET status=?, updated_at=? WHERE id=?`, to, now(), id)
	if err != nil {
		return err
	}
	// 记录里程碑（状态变更）
	_, _ = s.conn.Exec(`INSERT INTO biz_service_milestones(service_order_id,name,status,due_at)
		VALUES(?,?,?,?)`, id, "状态变更: "+cur.Status+" → "+to, "done", now())
	return nil
}

// AssignServiceOwner 分配服务负责人（SRV-104）
func (s *Store) AssignServiceOwner(id int64, ownerID, operator string) error {
	_, err := s.conn.Exec(`UPDATE biz_service_orders SET owner_id=?, status=?, updated_at=? WHERE id=?`,
		ownerID, SRVStatusDelivering, now(), id)
	if err != nil {
		return err
	}
	_, _ = s.conn.Exec(`INSERT INTO biz_service_milestones(service_order_id,name,status,due_at)
		VALUES(?,?,?,?)`, id, "负责人分配: "+ownerID, "done", now())
	return nil
}

// ---------- 验收（SRV-105）----------

// SubmitAcceptance 提交/确认验收
func (s *Store) SubmitAcceptance(serviceOrderID int64, result, note string) error {
	_, err := s.conn.Exec(`INSERT INTO biz_service_acceptances(service_order_id,result,note,accepted_at) VALUES(?,?,?,?)`,
		serviceOrderID, result, note, now())
	if err != nil {
		return err
	}
	to := SRVStatusWaitAccept
	if result == "pass" {
		to = SRVStatusCompleted
	}
	return s.UpdateServiceOrderStatus(serviceOrderID, to, "system", "验收:"+result+" "+note)
}

// ---------- 服务统计（SRV-108）----------

// ServiceStats 服务统计
type ServiceStats struct {
	CatalogCount int64   `json:"catalog_count"`
	OrderCount   int64   `json:"order_count"`
	CompletedCount int64 `json:"completed_count"`
	DeliveringCount int64 `json:"delivering_count"`
	Income       int64   `json:"income"` // 已完成服务金额（简化：按订单金额口径）
}

// GetServiceStats 服务统计
func (s *Store) GetServiceStats() (ServiceStats, error) {
	var st ServiceStats
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_service_catalogs`).Scan(&st.CatalogCount)
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_service_orders`).Scan(&st.OrderCount)
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_service_orders WHERE status='COMPLETED'`).Scan(&st.CompletedCount)
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_service_orders WHERE status IN ('DELIVERING','WAIT_ACCEPT')`).Scan(&st.DeliveringCount)
	_ = s.conn.QueryRow(`
		SELECT COALESCE(SUM(o.pay_amount),0) FROM biz_orders o
		JOIN biz_service_orders so ON so.order_id = o.id
		WHERE so.status='COMPLETED'`).Scan(&st.Income)
	return st, nil
}
