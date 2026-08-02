package biz

import (
	"net/http"
	"strconv"
)

// ============================================================
// 订单 HTTP 处理器（ORD-101~113）
// ============================================================

// orders GET /api/orders?mine=1&status= 或 POST 创建订单
func (h *Handler) orders(w http.ResponseWriter, r *http.Request, ac authCtx) {
	switch r.Method {
	case http.MethodGet:
		status := r.URL.Query().Get("status")
		mine := r.URL.Query().Get("mine") == "1"
		// 前台只查自己的；中台/后台可全量
		queryUser := ac.UserID
		if ac.isStaffRole() && !mine {
			queryUser = ""
		}
		items, err := h.store.ListOrders(queryUser, status, mine)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var o Order
		if err := jsonDecode(r, &o); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		o.UserID = ac.UserID // 强制使用登录用户
		created, err := h.store.CreateOrder(o)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// orderItem GET/POST(支付)/PUT(状态) /api/orders/{id}
func (h *Handler) orderItem(w http.ResponseWriter, r *http.Request, ac authCtx) {
	id, ok := parseID(r.URL.Path, "/api/orders/")
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_id", "order id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		o, err := h.store.GetOrder(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		// 数据权限：前台只能看自己的
		if !ac.isStaffRole() && o.UserID != ac.UserID {
			writeErr(w, http.StatusForbidden, "forbidden", "not your order")
			return
		}
		logs, _ := h.store.ListOrderLogs(id)
		writeJSON(w, http.StatusOK, map[string]any{"order": o, "logs": logs})
	case http.MethodPost:
		// 支付确认（ORD-103）：仅订单本人可确认；中台/后台需 order:write 权限
		o, err := h.store.GetOrder(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		isOwner := o.UserID == ac.UserID
		hasPerm, _ := h.store.HasPermission(ac.UserID, "order:write")
		if !isOwner && !hasPerm {
			writeErr(w, http.StatusForbidden, "forbidden", "not your order")
			return
		}
		var req struct {
			Channel string `json:"channel"`
			PayNo   string `json:"pay_no"`
		}
		_ = jsonDecode(r, &req)
		if err := h.store.PayOrder(id, req.Channel, req.PayNo); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		o, _ = h.store.GetOrder(id)
		writeJSON(w, http.StatusOK, o)
	case http.MethodPut:
		// 状态流转（中台/后台）：要求 order:write 权限
		hasPerm, _ := h.store.HasPermission(ac.UserID, "order:write")
		if !hasPerm {
			writeErr(w, http.StatusForbidden, "forbidden", "no order:write permission")
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
		if err := h.store.UpdateOrderStatus(id, req.Status, ac.Username, req.Note); err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		o, _ := h.store.GetOrder(id)
		writeJSON(w, http.StatusOK, o)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// orderStats GET /api/orders/stats（订单统计 ORD-113）
func (h *Handler) orderStats(w http.ResponseWriter, r *http.Request, ac authCtx) {
	st, err := h.store.GetOrderStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// refunds GET/POST /api/refunds（ORD-106）
func (h *Handler) refunds(w http.ResponseWriter, r *http.Request, ac authCtx) {
	switch r.Method {
	case http.MethodGet:
		status := r.URL.Query().Get("status")
		userID := ac.UserID
		if ac.isStaffRole() {
			userID = ""
		}
		items, err := h.store.ListRefunds(userID, status)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var rf Refund
		if err := jsonDecode(r, &rf); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		// 校验订单归属与状态（防替他人订单退款/重复退款）
		order, err := h.store.GetOrder(rf.OrderID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "order not found")
			return
		}
		if order.UserID != ac.UserID && !ac.isStaffRole() {
			writeErr(w, http.StatusForbidden, "forbidden", "not your order")
			return
		}
		switch order.Status {
		case OrderStatusPending, OrderStatusCancelled, OrderStatusClosed, OrderStatusRefunding, OrderStatusRefunded:
			writeErr(w, http.StatusBadRequest, "invalid_status", "order not refundable")
			return
		}
		if rf.Amount <= 0 || rf.Amount > order.PayAmount {
			writeErr(w, http.StatusBadRequest, "invalid_amount", "refund amount out of range")
			return
		}
		rf.UserID = ac.UserID
		created, err := h.store.CreateRefund(rf)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// refundItem POST /api/refunds/{id} （审核：body {approve, note}）
func (h *Handler) refundItem(w http.ResponseWriter, r *http.Request, ac authCtx) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}
	id, ok := parseID(r.URL.Path, "/api/refunds/")
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_id", "refund id required")
		return
	}
	var req struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	_ = jsonDecode(r, &req)
	if err := h.store.AuditRefund(id, req.Approve, ac.Username, req.Note); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

var _ = strconv.Itoa
