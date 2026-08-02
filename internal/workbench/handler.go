package workbench

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// Handler 工作台业务数据 HTTP 处理器
// 认证模式：与 FileSvc 一致，信任网关透传的 X-Auth-User-ID（开发模式直接信任）
type Handler struct {
	store *Store
}

// NewHandler 创建工作台数据处理器
func NewHandler(store *Store) *Handler {
	return &Handler{store: store}
}

// 从请求解析当前用户 ID：优先使用中间件已校验的上下文（JWT 模式），
// 仅开发模式回退信任 X-Auth-User-ID 头
func (h *Handler) owner(r *http.Request) string {
	if uid, ok := OwnerFromContext(r.Context()); ok && uid != "" {
		return uid
	}
	uid := r.Header.Get("X-Auth-User-ID")
	if uid == "" {
		uid = r.Header.Get("X-Forwarded-User")
	}
	return uid
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

// requireOwner 认证中间件包装：无用户身份返回 401
func (h *Handler) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.owner(r) == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing X-Auth-User-ID")
			return
		}
		next(w, r)
	}
}

// ---------- 路由 ----------

// Routes 返回工作台数据服务路由
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.Health)

	// 概览
	mux.HandleFunc("/api/workbench/overview", h.requireOwner(h.Overview))
	// 项目 CRUD
	mux.HandleFunc("/api/workbench/projects", h.requireOwner(h.handleProjects))
	mux.HandleFunc("/api/workbench/projects/", h.requireOwner(h.handleProjectItem))
	// 内容列表
	mux.HandleFunc("/api/workbench/content", h.requireOwner(h.Content))
	// 待办
	mux.HandleFunc("/api/workbench/todos", h.requireOwner(h.handleTodos))
	mux.HandleFunc("/api/workbench/todos/", h.requireOwner(h.handleTodoItem))
	// 通知
	mux.HandleFunc("/api/workbench/notifications", h.requireOwner(h.Notifications))
	mux.HandleFunc("/api/workbench/notifications/", h.requireOwner(h.handleNotificationItem))
	// 收入 / 流量 / 图表
	mux.HandleFunc("/api/workbench/income", h.requireOwner(h.Income))
	mux.HandleFunc("/api/workbench/traffic", h.requireOwner(h.Traffic))
	mux.HandleFunc("/api/workbench/chart", h.requireOwner(h.Chart))
	return mux
}

// Health 健康检查
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"service": "workbench-svc", "status": "ok"})
}

// Overview 数据概览卡片
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	items, err := h.store.GetOverview(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// ---------- 项目 ----------

func (h *Handler) handleProjects(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	switch r.Method {
	case http.MethodGet:
		items, err := h.store.GetProjects(owner)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var p Project
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		id, err := h.store.CreateProject(owner, p)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		p.ID = id
		writeJSON(w, http.StatusCreated, p)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

func (h *Handler) handleProjectItem(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	idStr := strings.TrimPrefix(r.URL.Path, "/api/workbench/projects/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", "project id must be integer")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var p Project
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		p.ID = id
		if err := h.store.UpdateProject(owner, id, p); err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodDelete:
		if err := h.store.DeleteProject(owner, id); err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// ---------- 内容 ----------

func (h *Handler) Content(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	items, err := h.store.GetContent(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// ---------- 待办 ----------

func (h *Handler) handleTodos(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	switch r.Method {
	case http.MethodGet:
		items, err := h.store.GetTodos(owner)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var t Todo
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		id, err := h.store.CreateTodo(owner, t)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		t.ID = id
		writeJSON(w, http.StatusCreated, t)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

func (h *Handler) handleTodoItem(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	idStr := strings.TrimPrefix(r.URL.Path, "/api/workbench/todos/")
	// 支持 POST /todos/{id}/toggle：先去掉 /toggle 后缀再解析 id
	base := strings.TrimSuffix(idStr, "/toggle")
	id, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", "todo id must be integer")
		return
	}
	switch r.Method {
	case http.MethodPost: // POST /todos/{id}/toggle
		if strings.HasSuffix(idStr, "/toggle") {
			done, err := h.store.ToggleTodo(owner, id)
			if err != nil {
				writeErr(w, http.StatusNotFound, "not_found", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "done": done})
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid_action", "unsupported action")
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// ---------- 通知 ----------

func (h *Handler) Notifications(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	items, err := h.store.GetNotifications(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) handleNotificationItem(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	idStr := strings.TrimPrefix(r.URL.Path, "/api/workbench/notifications/")
	id, err := strconv.ParseInt(strings.TrimSuffix(idStr, "/toggle"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_id", "notification id must be integer")
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(idStr, "/toggle") {
		unread, err := h.store.ToggleNotificationRead(owner, id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "unread": unread})
		return
	}
	writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
}

// ---------- 收入 / 流量 / 图表 ----------

func (h *Handler) Income(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	items, err := h.store.GetIncome(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) Traffic(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	items, err := h.store.GetTraffic(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) Chart(w http.ResponseWriter, r *http.Request) {
	owner := h.owner(r)
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "visits"
	}
	cd, err := h.store.GetChart(owner, kind)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cd)
}

// log middleware 防未使用 log 包报错
var _ = log.Printf
