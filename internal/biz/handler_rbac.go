package biz

import (
	"encoding/json"
	"net/http"
)

// ============================================================
// RBAC HTTP 处理器（AUTH-001 / 三台一体菜单）
// ============================================================

// rbacMe 当前用户角色与权限（前端路由守卫用）
func (h *Handler) rbacMe(w http.ResponseWriter, r *http.Request, ac authCtx) {
	roles, err := h.store.GetUserRoles(ac.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	perms, err := h.store.GetUserPermissions(ac.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	roleCodes := make([]string, 0, len(roles))
	for _, r := range roles {
		roleCodes = append(roleCodes, r.Code)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":     ac.UserID,
		"username":    ac.Username,
		"roles":       roleCodes,
		"permissions": perms,
	})
}

// rbacMenus 按角色返回三台一体菜单（FR-805/806：菜单由后端驱动）
func (h *Handler) rbacMenus(w http.ResponseWriter, r *http.Request, ac authCtx) {
	menus, err := h.store.GetMenus(ac.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, menus)
}

// rbacRoles 角色列表（后台配置）
func (h *Handler) rbacRoles(w http.ResponseWriter, r *http.Request, ac authCtx) {
	roles, err := h.store.ListAllRoles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, roles)
}

// rbacUsers 用户角色绑定列表（后台用户管理）
func (h *Handler) rbacUsers(w http.ResponseWriter, r *http.Request, ac authCtx) {
	users, err := h.store.ListRoleAssignments()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// rbacAssign 给用户绑定角色（POST body: {user_id, role_code}）
func (h *Handler) rbacAssign(w http.ResponseWriter, r *http.Request, ac authCtx) {
	var req struct {
		UserID   string `json:"user_id"`
		RoleCode string `json:"role_code"`
	}
	if err := jsonDecode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.UserID == "" || req.RoleCode == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request", "user_id and role_code required")
		return
	}
	if err := h.store.AssignRole(req.UserID, req.RoleCode); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "user_id": req.UserID, "role_code": req.RoleCode})
}

// rbacUnassign 解除用户角色（POST body: {user_id, role_code}）
func (h *Handler) rbacUnassign(w http.ResponseWriter, r *http.Request, ac authCtx) {
	var req struct {
		UserID   string `json:"user_id"`
		RoleCode string `json:"role_code"`
	}
	if err := jsonDecode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.store.UnassignRole(req.UserID, req.RoleCode); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// jsonDecode 读取并解析 JSON 请求体
func jsonDecode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}
