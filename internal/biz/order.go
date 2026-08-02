package biz

import (
	"database/sql"
	"fmt"
	"time"
)

// ============================================================
// 订单服务（第 13 章 ORD-101~113 / BIZ-001）
// 状态机：待支付 → 已支付 → 待交付 → 已交付 → 已完成；支持取消/关闭/退款
// 统一支付接口：先做渠道抽象与回调幂等预留（ORD-103），不接入具体支付渠道
// ============================================================

// CreateOrder 创建订单（ORD-101）
func (s *Store) CreateOrder(o Order) (Order, error) {
	now := now()
	o.OrderNo = genNo("ORD")
	if o.Status == "" {
		o.Status = OrderStatusPending
	}
	if o.PayAmount == 0 {
		o.PayAmount = o.Amount
	}
	res, err := s.conn.Exec(`INSERT INTO biz_orders(
		order_no,user_id,type,title,amount,pay_amount,status,pay_channel,pay_no,note,created_at,updated_at,paid_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL)`,
		o.OrderNo, o.UserID, o.Type, o.Title, o.Amount, o.PayAmount, o.Status,
		o.PayChannel, o.PayNo, o.Note, now, now)
	if err != nil {
		return o, err
	}
	o.ID, _ = res.LastInsertId()
	o.CreatedAt = time.Now()
	o.UpdatedAt = o.CreatedAt
	s.logOrderStatus(o.ID, "", o.Status, o.UserID, "创建订单")
	return o, nil
}

// GetOrder 查询订单
func (s *Store) GetOrder(id int64) (Order, error) {
	var o Order
	var createdAt, updatedAt string
	var paidAt sql.NullString
	err := s.conn.QueryRow(`SELECT id,order_no,user_id,type,title,amount,pay_amount,status,pay_channel,pay_no,note,created_at,updated_at,paid_at
		FROM biz_orders WHERE id=?`, id).Scan(
		&o.ID, &o.OrderNo, &o.UserID, &o.Type, &o.Title, &o.Amount, &o.PayAmount, &o.Status,
		&o.PayChannel, &o.PayNo, &o.Note, &createdAt, &updatedAt, &paidAt)
	if err != nil {
		return o, err
	}
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	o.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if paidAt.Valid && paidAt.String != "" {
		t, _ := time.Parse(time.RFC3339, paidAt.String)
		o.PaidAt = &t
	}
	return o, nil
}

// ListOrders 查询订单列表（支持按用户/状态筛选，中台/后台全量检索 ORD-104）
func (s *Store) ListOrders(userID, status string, mine bool) ([]Order, error) {
	q := `SELECT id,order_no,user_id,type,title,amount,pay_amount,status,pay_channel,pay_no,note,created_at,updated_at,paid_at FROM biz_orders`
	var args []any
	var where string
	if mine && userID != "" {
		where = " WHERE user_id=?"
		args = append(args, userID)
	} else if userID != "" {
		where = " WHERE user_id=?"
		args = append(args, userID)
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
	var items []Order
	for rows.Next() {
		var o Order
		var createdAt, updatedAt string
		var paidAt sql.NullString
		if err := rows.Scan(&o.ID, &o.OrderNo, &o.UserID, &o.Type, &o.Title, &o.Amount, &o.PayAmount,
			&o.Status, &o.PayChannel, &o.PayNo, &o.Note, &createdAt, &updatedAt, &paidAt); err != nil {
			return nil, err
		}
		o.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		o.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		items = append(items, o)
	}
	return items, rows.Err()
}

// UpdateOrderStatus 更新订单状态（带状态流转日志，ORD-102）
func (s *Store) UpdateOrderStatus(id int64, to string, operator, note string) error {
	cur, err := s.GetOrder(id)
	if err != nil {
		return err
	}
	if cur.Status == to {
		return nil
	}
	paidAt := "NULL"
	if to == OrderStatusPaid {
		paidAt = "'" + now() + "'"
	}
	_, err = s.conn.Exec(`UPDATE biz_orders SET status=?, updated_at=?, paid_at=`+paidAt+` WHERE id=?`,
		to, now(), id)
	if err != nil {
		return err
	}
	return s.logOrderStatus(id, cur.Status, to, operator, note)
}

// PayOrder 统一支付成功回调（ORD-103 幂等预留：重复回调仅记录，不重复改状态）
func (s *Store) PayOrder(id int64, channel, payNo string) error {
	cur, err := s.GetOrder(id)
	if err != nil {
		return err
	}
	if cur.Status == OrderStatusPaid || cur.Status == OrderStatusDelivering {
		return nil // 幂等：已支付直接忽略
	}
	_, err = s.conn.Exec(`UPDATE biz_orders SET status=?, pay_channel=?, pay_no=?, updated_at=?, paid_at=? WHERE id=?`,
		OrderStatusPaid, channel, payNo, now(), now(), id)
	if err != nil {
		return err
	}
	return s.logOrderStatus(id, cur.Status, OrderStatusPaid, "system", "支付成功 "+channel)
}

func (s *Store) logOrderStatus(orderID int64, from, to, operator, note string) error {
	_, err := s.conn.Exec(`INSERT INTO biz_order_logs(order_id,from_status,to_status,operator,note,created_at) VALUES(?,?,?,?,?,?)`,
		orderID, from, to, operator, note, now())
	return err
}

// ListOrderLogs 订单状态流转日志（第 22 章）
func (s *Store) ListOrderLogs(orderID int64) ([]OrderStatusLog, error) {
	rows, err := s.conn.Query(`SELECT id,order_id,from_status,to_status,operator,note,created_at FROM biz_order_logs WHERE order_id=? ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []OrderStatusLog
	for rows.Next() {
		var l OrderStatusLog
		var createdAt string
		if err := rows.Scan(&l.ID, &l.OrderID, &l.From, &l.To, &l.Operator, &l.Note, &createdAt); err != nil {
			return nil, err
		}
		l.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		items = append(items, l)
	}
	return items, rows.Err()
}

// ---------- 退款（ORD-106 / 第 18 章）----------

// CreateRefund 提交退款申请
func (s *Store) CreateRefund(r Refund) (Refund, error) {
	r.RefundNo = genNo("REF")
	r.Status = "pending"
	now := now()
	res, err := s.conn.Exec(`INSERT INTO biz_refunds(refund_no,order_id,user_id,amount,reason,type,status,audit_note,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.RefundNo, r.OrderID, r.UserID, r.Amount, r.Reason, r.Type, r.Status, "", now, now)
	if err != nil {
		return r, err
	}
	r.ID, _ = res.LastInsertId()
	// 订单进入退款中
	_ = s.UpdateOrderStatus(r.OrderID, OrderStatusRefunding, r.UserID, "申请退款")
	return r, nil
}

// AuditRefund 审核退款（approve → 订单退款完成；reject → 恢复原状）
func (s *Store) AuditRefund(id int64, approve bool, operator, note string) error {
	var r Refund
	var createdAt, updatedAt string
	err := s.conn.QueryRow(`SELECT id,refund_no,order_id,user_id,amount,reason,type,status,audit_note,created_at,updated_at FROM biz_refunds WHERE id=?`, id).
		Scan(&r.ID, &r.RefundNo, &r.OrderID, &r.UserID, &r.Amount, &r.Reason, &r.Type, &r.Status, &r.AuditNote, &createdAt, &updatedAt)
	if err != nil {
		return err
	}
	status := "rejected"
	if approve {
		status = "completed"
	}
	_, err = s.conn.Exec(`UPDATE biz_refunds SET status=?, audit_note=?, updated_at=? WHERE id=?`,
		status, note, now(), id)
	if err != nil {
		return err
	}
	if approve {
		_ = s.UpdateOrderStatus(r.OrderID, OrderStatusRefunded, operator, "退款完成 "+note)
	} else {
		_ = s.UpdateOrderStatus(r.OrderID, OrderStatusPaid, operator, "退款驳回 "+note)
	}
	return nil
}

// ListRefunds 退款列表
func (s *Store) ListRefunds(userID, status string) ([]Refund, error) {
	q := `SELECT id,refund_no,order_id,user_id,amount,reason,type,status,audit_note,created_at,updated_at FROM biz_refunds`
	var args []any
	var where string
	if userID != "" {
		where = " WHERE user_id=?"
		args = append(args, userID)
	}
	if status != "" {
		if where != "" {
			where += " AND status=?"
		} else {
			where = " WHERE status=?"
		}
		args = append(args, status)
	}
	q += where + " ORDER BY id DESC"
	rows, err := s.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Refund
	for rows.Next() {
		var r Refund
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.RefundNo, &r.OrderID, &r.UserID, &r.Amount, &r.Reason, &r.Type, &r.Status, &r.AuditNote, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

// ---------- 订单统计（ORD-113 / 第 18.3 收入统计）----------

// OrderStats 订单统计
type OrderStats struct {
	TotalCount   int64  `json:"total_count"`
	PaidCount    int64  `json:"paid_count"`
	TotalAmount  int64  `json:"total_amount"`
	PaidAmount   int64  `json:"paid_amount"`
	RefundAmount int64  `json:"refund_amount"`
}

// GetOrderStats 汇总订单统计
func (s *Store) GetOrderStats() (OrderStats, error) {
	var st OrderStats
	if err := s.conn.QueryRow(`SELECT COUNT(*), COALESCE(SUM(amount),0) FROM biz_orders`).Scan(&st.TotalCount, &st.TotalAmount); err != nil {
		return st, err
	}
	if err := s.conn.QueryRow(`SELECT COUNT(*), COALESCE(SUM(pay_amount),0) FROM biz_orders WHERE status IN ('paid','delivering','delivered','completed','refunding')`).
		Scan(&st.PaidCount, &st.PaidAmount); err != nil {
		return st, err
	}
	if err := s.conn.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM biz_refunds WHERE status='completed'`).Scan(&st.RefundAmount); err != nil {
		return st, err
	}
	return st, nil
}

var _ = fmt.Sprintf
