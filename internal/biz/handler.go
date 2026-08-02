package biz

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ============================================================
// BizSvc HTTP 处理器（业务服务 :8084）
// 模块：RBAC / 订单 / 工单 / 资源 / 服务
// 认证：网关透传 X-Auth-User-ID / X-Auth-Username / X-Auth-Role
// ============================================================

type Handler struct {
	store *Store
}

func NewHandler(store *Store) *Handler { return &Handler{store: store} }

// ---------- 认证工具 ----------

type authCtx struct {
	UserID   string
	Username string
	Role     string
	Scope    string
}

// resolveAuth 从请求解析用户身份：优先使用中间件已校验的上下文（JWT 模式），
// 仅开发模式回退信任 X-Auth-* 头
func resolveAuth(r *http.Request) authCtx {
	if ac, ok := AuthFromContext(r.Context()); ok && ac.UserID != "" {
		return ac
	}
	return authCtx{
		UserID:   firstNonEmpty(r.Header.Get("X-Auth-User-ID"), r.Header.Get("X-Forwarded-User")),
		Username: r.Header.Get("X-Auth-Username"),
		Role:     r.Header.Get("X-Auth-Role"),
		Scope:    r.Header.Get("X-Auth-Scope"),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isStaffRole 是否为中台/后台角色（可看内部备注、可处理工单等）
func (ac authCtx) isStaffRole() bool {
	switch ac.Role {
	case "admin", "super_admin", "operator", "staff":
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": code, "message": msg})
}

// requireAuth 必须登录（X-Auth-User-ID 非空）
func requireAuth(next func(http.ResponseWriter, *http.Request, authCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ac := resolveAuth(r)
		if ac.UserID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing X-Auth-User-ID")
			return
		}
		next(w, r, ac)
	}
}

// requirePerm 校验权限码（RBAC 服务端兜底）
func (h *Handler) requirePerm(permCode string, next func(http.ResponseWriter, *http.Request, authCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ac := resolveAuth(r)
		if ac.UserID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing X-Auth-User-ID")
			return
		}
		ok, err := h.store.HasPermission(ac.UserID, permCode)
		if err != nil || !ok {
			writeErr(w, http.StatusForbidden, "forbidden", "no permission: "+permCode)
			return
		}
		next(w, r, ac)
	}
}

// parseID 解析路径中的整数 ID（支持 /api/xxx/{id}/action 形式）
func parseID(path string, prefix string) (int64, bool) {
	raw := strings.TrimPrefix(path, prefix)
	// 取第一段：{id} 或 {id}/action
	raw = strings.SplitN(raw, "/", 2)[0]
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil && id > 0
}

// ---------- 路由 ----------

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// 健康检查
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"service": "biz-svc", "status": "ok"})
	})

	// ===== RBAC（AUTH-001 / 三台一体菜单）=====
	mux.HandleFunc("/api/rbac/me", requireAuth(h.rbacMe))
	mux.HandleFunc("/api/rbac/menus", requireAuth(h.rbacMenus))
	mux.HandleFunc("/api/rbac/roles", h.requirePerm("admin:role", h.rbacRoles))
	mux.HandleFunc("/api/rbac/users", h.requirePerm("admin:user", h.rbacUsers))
	mux.HandleFunc("/api/rbac/assign", h.requirePerm("admin:role", h.rbacAssign))
	mux.HandleFunc("/api/rbac/unassign", h.requirePerm("admin:role", h.rbacUnassign))

	// ===== 订单（ORD）=====
	mux.HandleFunc("/api/orders", requireAuth(h.orders))
	mux.HandleFunc("/api/orders/", requireAuth(h.orderItem))
	mux.HandleFunc("/api/orders/stats", h.requirePerm("order:read", h.orderStats))
	mux.HandleFunc("/api/refunds", requireAuth(h.refunds))
	mux.HandleFunc("/api/refunds/", h.requirePerm("order:refund", h.refundItem))

	// ===== 工单（WO）=====
	mux.HandleFunc("/api/tickets", requireAuth(h.tickets))
	mux.HandleFunc("/api/tickets/", requireAuth(h.ticketItem))
	mux.HandleFunc("/api/tickets/stats", h.requirePerm("ticket:read", h.ticketStats))

	// ===== 资源（RES）=====
	mux.HandleFunc("/api/resources", requireAuth(h.resources))
	mux.HandleFunc("/api/resources/", requireAuth(h.resourceItem))
	mux.HandleFunc("/api/resources/access", requireAuth(h.resourceAccess))

	// ===== 服务（SRV）=====
	mux.HandleFunc("/api/services/catalogs", requireAuth(h.serviceCatalogs))
	mux.HandleFunc("/api/services/catalogs/", h.requirePerm("service:write", h.serviceCatalogItem))
	mux.HandleFunc("/api/services/skus", h.requirePerm("service:write", h.serviceSKUs))
	mux.HandleFunc("/api/services/orders", requireAuth(h.serviceOrders))
	mux.HandleFunc("/api/services/orders/", requireAuth(h.serviceOrderItem))
	mux.HandleFunc("/api/services/stats", h.requirePerm("service:read", h.serviceStats))

	return mux
}
