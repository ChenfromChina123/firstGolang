package biz

import (
	"net/http"
)

// ============================================================
// 工单 HTTP 处理器（WO-101~112）
// ============================================================

// tickets GET/POST /api/tickets（WO-101 创建 / 列表）
func (h *Handler) tickets(w http.ResponseWriter, r *http.Request, ac authCtx) {
	switch r.Method {
	case http.MethodGet:
		status := r.URL.Query().Get("status")
		mine := r.URL.Query().Get("mine") == "1"
		var assigneeID string
		queryUser := ac.UserID
		if ac.isStaffRole() && !mine {
			queryUser = ""
			// 中台可按派单给我过滤
			if r.URL.Query().Get("assigned") == "1" {
				assigneeID = ac.UserID
			}
		}
		items, err := h.store.ListTickets(queryUser, status, assigneeID, mine)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var t Ticket
		if err := jsonDecode(r, &t); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		t.UserID = ac.UserID
		created, err := h.store.CreateTicket(t)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// ticketItem 工单详情与操作：
//
//	GET  /api/tickets/{id}                 详情+回复（客户隐藏内部备注）
//	POST /api/tickets/{id}/reply           回复（body: {content, is_internal}）
//	POST /api/tickets/{id}/assign          派单（body: {assignee_id}）
//	POST /api/tickets/{id}/status          状态流转（body: {status, note}）
//	POST /api/tickets/{id}/evaluate        满意度评价（body: {score,speed,quality,communication,comment}）
func (h *Handler) ticketItem(w http.ResponseWriter, r *http.Request, ac authCtx) {
	id, ok := parseID(r.URL.Path, "/api/tickets/")
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_id", "ticket id required")
		return
	}
	t, err := h.store.GetTicket(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	// 数据权限
	if !ac.isStaffRole() && t.UserID != ac.UserID {
		writeErr(w, http.StatusForbidden, "forbidden", "not your ticket")
		return
	}

	switch r.Method {
	case http.MethodGet:
		replies, _ := h.store.ListTicketReplies(id, ac.isStaffRole())
		writeJSON(w, http.StatusOK, map[string]any{"ticket": t, "replies": replies})
	case http.MethodPost:
		path := r.URL.Path
		switch {
		case hasSuffix(path, "/reply"):
			var req struct {
				Content    string `json:"content"`
				IsInternal bool   `json:"is_internal"`
			}
			if err := jsonDecode(r, &req); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			// 内部备注：需 ticket:write 权限（客户无该权限）
			if req.IsInternal {
				ok, _ := h.store.HasPermission(ac.UserID, "ticket:write")
				if !ok {
					writeErr(w, http.StatusForbidden, "forbidden", "no ticket:write permission")
					return
				}
			}
			reply, err := h.store.AddTicketReply(id, ac.UserID, req.Content, req.IsInternal)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, reply)
		case hasSuffix(path, "/assign"):
			ok, _ := h.store.HasPermission(ac.UserID, "ticket:assign")
			if !ok {
				writeErr(w, http.StatusForbidden, "forbidden", "no ticket:assign permission")
				return
			}
			var req struct {
				AssigneeID string `json:"assignee_id"`
			}
			_ = jsonDecode(r, &req)
			if err := h.store.AssignTicket(id, req.AssigneeID, ac.Username); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		case hasSuffix(path, "/status"):
			ok, _ := h.store.HasPermission(ac.UserID, "ticket:write")
			if !ok {
				writeErr(w, http.StatusForbidden, "forbidden", "no ticket:write permission")
				return
			}
			var req struct {
				Status string `json:"status"`
				Note   string `json:"note"`
			}
			if err := jsonDecode(r, &req); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			if err := h.store.UpdateTicketStatus(id, req.Status, ac.Username, req.Note); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		case hasSuffix(path, "/evaluate"):
			var req TicketEvaluation
			if err := jsonDecode(r, &req); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			if req.Score == 0 {
				req.Score = 5
			}
			ev, err := h.store.AddTicketEvaluation(id, req)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, ev)
		default:
			writeErr(w, http.StatusBadRequest, "invalid_action", "unsupported action")
		}
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// ticketStats GET /api/tickets/stats（WO-110）
func (h *Handler) ticketStats(w http.ResponseWriter, r *http.Request, ac authCtx) {
	st, err := h.store.GetTicketStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// hasSuffix 字符串后缀判断
func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
