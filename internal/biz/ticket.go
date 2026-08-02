package biz

import (
	"time"
)

// ============================================================
// 工单服务（第 13 章 WO-101~112 / FR-803）
// 状态机：待受理 → 处理中 → 待用户确认 → 已解决 → 已关闭；支持重开
// SLA（WO-106）：响应时限，超时升级预留；满意度（WO-109）：关闭后可评价
// ============================================================

// CreateTicket 创建工单（WO-101：网站表单/站内信/咨询页）
func (s *Store) CreateTicket(t Ticket) (Ticket, error) {
	now := now()
	t.TicketNo = genNo("WO")
	if t.Status == "" {
		t.Status = TicketStatusPending
	}
	// SLA：默认 24h 内响应（启用 SLA）
	slaDue := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	if !t.SLADueAt.IsZero() {
		slaDue = t.SLADueAt.Format(time.RFC3339)
	}
	res, err := s.conn.Exec(`INSERT INTO biz_tickets(
		ticket_no,user_id,title,category,priority,status,assignee_id,order_id,sla_due_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		t.TicketNo, t.UserID, t.Title, t.Category, t.Priority, t.Status,
		t.AssigneeID, t.OrderID, slaDue, now, now)
	if err != nil {
		return t, err
	}
	t.ID, _ = res.LastInsertId()
	t.SLADueAt, _ = time.Parse(time.RFC3339, slaDue)
	t.CreatedAt = time.Now()
	t.UpdatedAt = t.CreatedAt
	return t, nil
}

// GetTicket 查询工单
func (s *Store) GetTicket(id int64) (Ticket, error) {
	var t Ticket
	var createdAt, updatedAt, sla string
	err := s.conn.QueryRow(`SELECT id,ticket_no,user_id,title,category,priority,status,assignee_id,order_id,sla_due_at,created_at,updated_at
		FROM biz_tickets WHERE id=?`, id).Scan(
		&t.ID, &t.TicketNo, &t.UserID, &t.Title, &t.Category, &t.Priority, &t.Status,
		&t.AssigneeID, &t.OrderID, &sla, &createdAt, &updatedAt)
	if err != nil {
		return t, err
	}
	t.SLADueAt, _ = time.Parse(time.RFC3339, sla)
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return t, nil
}

// ListTickets 工单列表（前台 mine=本人；中台处理队列；后台全量）
func (s *Store) ListTickets(userID, status, assigneeID string, mine bool) ([]Ticket, error) {
	q := `SELECT id,ticket_no,user_id,title,category,priority,status,assignee_id,order_id,sla_due_at,created_at,updated_at FROM biz_tickets`
	var args []any
	var where string
	if mine && userID != "" {
		where = " WHERE user_id=?"
		args = append(args, userID)
	}
	if assigneeID != "" {
		if where != "" {
			where += " AND assignee_id=?"
		} else {
			where = " WHERE assignee_id=?"
		}
		args = append(args, assigneeID)
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
	var items []Ticket
	for rows.Next() {
		var t Ticket
		var createdAt, updatedAt, sla string
		if err := rows.Scan(&t.ID, &t.TicketNo, &t.UserID, &t.Title, &t.Category, &t.Priority, &t.Status,
			&t.AssigneeID, &t.OrderID, &sla, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		t.SLADueAt, _ = time.Parse(time.RFC3339, sla)
		t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		items = append(items, t)
	}
	return items, rows.Err()
}

// UpdateTicketStatus 工单状态流转（WO-103）
func (s *Store) UpdateTicketStatus(id int64, to, operator, note string) error {
	cur, err := s.GetTicket(id)
	if err != nil {
		return err
	}
	if cur.Status == to {
		return nil
	}
	_, err = s.conn.Exec(`UPDATE biz_tickets SET status=?, updated_at=? WHERE id=?`, to, now(), id)
	if err != nil {
		return err
	}
	// 状态变更记录内部备注
	_, _ = s.conn.Exec(`INSERT INTO biz_ticket_replies(ticket_id,user_id,content,is_internal,created_at)
		VALUES(?,?,?,1,?)`, id, operator, "状态变更: "+cur.Status+" → "+to+"（"+note+"）", now())
	return nil
}

// AssignTicket 派单（WO-104）
func (s *Store) AssignTicket(id int64, assigneeID, operator string) error {
	_, err := s.conn.Exec(`UPDATE biz_tickets SET assignee_id=?, status=?, updated_at=? WHERE id=?`,
		assigneeID, TicketStatusHandling, now(), id)
	if err != nil {
		return err
	}
	_, _ = s.conn.Exec(`INSERT INTO biz_ticket_replies(ticket_id,user_id,content,is_internal,created_at)
		VALUES(?,?,?,1,?)`, id, operator, "派单给: "+assigneeID, now())
	return nil
}

// AddTicketReply 添加回复（WO-105：isInternal=1 时客户不可见）
func (s *Store) AddTicketReply(ticketID int64, userID, content string, isInternal bool) (TicketReply, error) {
	internal := 0
	if isInternal {
		internal = 1
	}
	nowStr := now()
	res, err := s.conn.Exec(`INSERT INTO biz_ticket_replies(ticket_id,user_id,content,is_internal,created_at) VALUES(?,?,?,?,?)`,
		ticketID, userID, content, internal, nowStr)
	if err != nil {
		return TicketReply{}, err
	}
	r := TicketReply{TicketID: ticketID, UserID: userID, Content: content, IsInternal: isInternal}
	r.ID, _ = res.LastInsertId()
	r.CreatedAt, _ = time.Parse(time.RFC3339, nowStr)
	return r, nil
}

// ListTicketReplies 工单回复（客户视角过滤内部备注）
func (s *Store) ListTicketReplies(ticketID int64, isStaff bool) ([]TicketReply, error) {
	q := `SELECT id,ticket_id,user_id,content,is_internal,created_at FROM biz_ticket_replies WHERE ticket_id=?`
	if !isStaff {
		q += " AND is_internal=0"
	}
	q += " ORDER BY id"
	rows, err := s.conn.Query(q, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TicketReply
	for rows.Next() {
		var r TicketReply
		var internal int
		var createdAt string
		if err := rows.Scan(&r.ID, &r.TicketID, &r.UserID, &r.Content, &internal, &createdAt); err != nil {
			return nil, err
		}
		r.IsInternal = internal == 1
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		items = append(items, r)
	}
	return items, rows.Err()
}

// AddTicketEvaluation 工单满意度评价（WO-109 / 统一评价）
func (s *Store) AddTicketEvaluation(ticketID int64, ev TicketEvaluation) (TicketEvaluation, error) {
	ev.CreatedAt = time.Now()
	_, err := s.conn.Exec(`INSERT OR REPLACE INTO biz_ticket_evaluations(ticket_id,score,speed,quality,communication,comment,created_at)
		VALUES(?,?,?,?,?,?,?)`,
		ticketID, ev.Score, ev.Speed, ev.Quality, ev.Communication, ev.Comment, now())
	if err != nil {
		return ev, err
	}
	// 评价后工单关闭
	_ = s.UpdateTicketStatus(ticketID, TicketStatusClosed, ev.CreatedAt.Format("2006-01-02"), "用户评价完成")
	return ev, nil
}

// ---------- 工单统计（WO-110）----------

// TicketStats 工单统计
type TicketStats struct {
	TotalCount    int64   `json:"total_count"`
	PendingCount  int64   `json:"pending_count"`
	HandlingCount int64   `json:"handling_count"`
	ResolvedCount int64   `json:"resolved_count"`
	AvgScore      float64 `json:"avg_score"`
	OverdueCount  int64   `json:"overdue_count"` // SLA 超时数
}

// GetTicketStats 工单统计
func (s *Store) GetTicketStats() (TicketStats, error) {
	var st TicketStats
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_tickets`).Scan(&st.TotalCount)
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_tickets WHERE status='pending'`).Scan(&st.PendingCount)
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_tickets WHERE status='handling'`).Scan(&st.HandlingCount)
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_tickets WHERE status='resolved'`).Scan(&st.ResolvedCount)
	_ = s.conn.QueryRow(`SELECT COALESCE(AVG(score),0) FROM biz_ticket_evaluations`).Scan(&st.AvgScore)
	_ = s.conn.QueryRow(`SELECT COUNT(*) FROM biz_tickets WHERE status IN ('pending','handling') AND sla_due_at < ?`, now()).Scan(&st.OverdueCount)
	return st, nil
}
