package biz

import (
	"net/http"
)

// ============================================================
// 服务 HTTP 处理器（SRV-101~108）
// ============================================================

// serviceCatalogs GET /api/services/catalogs（前台只看 active，后台全量）
func (h *Handler) serviceCatalogs(w http.ResponseWriter, r *http.Request, ac authCtx) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}
	activeOnly := !ac.isStaffRole()
	items, err := h.store.ListServiceCatalogs(activeOnly)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// 附带 SKU
	type catalogWithSKU struct {
		ServiceCatalog
		SKUs []ServiceSKU `json:"skus"`
	}
	var out []catalogWithSKU
	for _, c := range items {
		skus, _ := h.store.ListServiceSKUs(c.ID)
		out = append(out, catalogWithSKU{ServiceCatalog: c, SKUs: skus})
	}
	writeJSON(w, http.StatusOK, out)
}

// serviceCatalogItem POST /api/services/catalogs/{id}/sku 添加 SKU
//
//	PUT /api/services/catalogs/{id} 更新目录
func (h *Handler) serviceCatalogItem(w http.ResponseWriter, r *http.Request, ac authCtx) {
	id, ok := parseID(r.URL.Path, "/api/services/catalogs/")
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_id", "catalog id required")
		return
	}
	switch {
	case r.Method == http.MethodPut:
		var c ServiceCatalog
		if err := jsonDecode(r, &c); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if err := h.store.UpdateServiceCatalog(id, c); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		updated, _ := h.store.GetServiceCatalog(id)
		writeJSON(w, http.StatusOK, updated)
	case r.Method == http.MethodPost && hasSuffix(r.URL.Path, "/sku"):
		var sku ServiceSKU
		if err := jsonDecode(r, &sku); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		sku.ServiceID = id
		created, err := h.store.AddServiceSKU(sku)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// serviceSKUs POST /api/services/skus （直接创建目录+SKU 二合一辅助）
func (h *Handler) serviceSKUs(w http.ResponseWriter, r *http.Request, ac authCtx) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}
	// 简化：body {catalog: {...}, sku: {...}} 一次创建
	var req struct {
		Catalog ServiceCatalog `json:"catalog"`
		SKU     ServiceSKU     `json:"sku"`
	}
	if err := jsonDecode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	cat, err := h.store.CreateServiceCatalog(req.Catalog)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if req.SKU.Name != "" {
		req.SKU.ServiceID = cat.ID
		sku, err := h.store.AddServiceSKU(req.SKU)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"catalog": cat, "sku": sku})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"catalog": cat})
}

// serviceOrders GET/POST /api/services/orders
func (h *Handler) serviceOrders(w http.ResponseWriter, r *http.Request, ac authCtx) {
	switch r.Method {
	case http.MethodGet:
		status := r.URL.Query().Get("status")
		mine := r.URL.Query().Get("mine") == "1"
		queryUser := ac.UserID
		var ownerID string
		if ac.isStaffRole() && !mine {
			queryUser = ""
			if r.URL.Query().Get("assigned") == "1" {
				ownerID = ac.UserID
			}
		}
		items, err := h.store.ListServiceOrders(queryUser, status, ownerID, mine)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var so ServiceOrder
		if err := jsonDecode(r, &so); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		// 服务单必须关联已支付订单（BIZ-001：服务 → 订单 → 服务单）
		if so.OrderID <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid_request", "order_id required")
			return
		}
		order, err := h.store.GetOrder(so.OrderID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "order not found")
			return
		}
		if ac.isStaffRole() {
			ok, _ := h.store.HasPermission(ac.UserID, "order:read")
			if !ok {
				writeErr(w, http.StatusForbidden, "forbidden", "no order:read permission")
				return
			}
		} else {
			if order.UserID != ac.UserID {
				writeErr(w, http.StatusForbidden, "forbidden", "not your order")
				return
			}
			switch order.Status {
			case OrderStatusPaid, OrderStatusDelivering, OrderStatusDelivered, OrderStatusCompleted:
			default:
				writeErr(w, http.StatusBadRequest, "invalid_status", "order must be paid first")
				return
			}
		}
		so.UserID = ac.UserID
		created, err := h.store.CreateServiceOrder(so)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// serviceOrderItem 服务单详情与操作：
//
//	GET  /api/services/orders/{id}                    详情
//	POST /api/services/orders/{id}/assign             分配负责人（body: {owner_id}）
//	POST /api/services/orders/{id}/status             状态流转（body: {status, note}）
//	POST /api/services/orders/{id}/acceptance         验收（body: {result, note}）
func (h *Handler) serviceOrderItem(w http.ResponseWriter, r *http.Request, ac authCtx) {
	id, ok := parseID(r.URL.Path, "/api/services/orders/")
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_id", "service order id required")
		return
	}
	so, err := h.store.GetServiceOrder(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if !ac.isStaffRole() && so.UserID != ac.UserID {
		writeErr(w, http.StatusForbidden, "forbidden", "not your service order")
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, so)
	case http.MethodPost:
		path := r.URL.Path
		switch {
		case hasSuffix(path, "/assign"):
			if !ac.isStaffRole() {
				writeErr(w, http.StatusForbidden, "forbidden", "staff only")
				return
			}
			var req struct {
				OwnerID string `json:"owner_id"`
			}
			_ = jsonDecode(r, &req)
			if err := h.store.AssignServiceOwner(id, req.OwnerID, ac.Username); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		case hasSuffix(path, "/status"):
			if !ac.isStaffRole() {
				writeErr(w, http.StatusForbidden, "forbidden", "staff only")
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
			if err := h.store.UpdateServiceOrderStatus(id, req.Status, ac.Username, req.Note); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		case hasSuffix(path, "/acceptance"):
			var req struct {
				Result string `json:"result"`
				Note   string `json:"note"`
			}
			if err := jsonDecode(r, &req); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			if err := h.store.SubmitAcceptance(id, req.Result, req.Note); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
		default:
			writeErr(w, http.StatusBadRequest, "invalid_action", "unsupported action")
		}
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// serviceStats GET /api/services/stats（SRV-108）
func (h *Handler) serviceStats(w http.ResponseWriter, r *http.Request, ac authCtx) {
	st, err := h.store.GetServiceStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}
