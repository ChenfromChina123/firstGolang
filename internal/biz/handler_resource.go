package biz

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ============================================================
// 资源 HTTP 处理器（RES-101~111 / RES-201~210）
// ============================================================

// resources GET/POST /api/resources
//
//	GET ?status=&policy=&public=1  （前台 public=1 只看已发布）
func (h *Handler) resources(w http.ResponseWriter, r *http.Request, ac authCtx) {
	switch r.Method {
	case http.MethodGet:
		status := r.URL.Query().Get("status")
		policy := r.URL.Query().Get("policy")
		publishedOnly := r.URL.Query().Get("public") == "1"
		// 后台/中台可看全部；前台默认只看已发布
		if !ac.isStaffRole() && !publishedOnly {
			publishedOnly = true
		}
		items, err := h.store.ListResources(status, policy, publishedOnly, ac.UserID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var res Resource
		if err := jsonDecode(r, &res); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		res.OwnerID = ac.UserID
		created, err := h.store.CreateResource(res)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, created)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// resourceItem 资源详情与操作：
//
//	GET    /api/resources/{id}        详情
//	PUT    /api/resources/{id}        更新（owner/staff）
//	DELETE /api/resources/{id}        删除（owner/staff）
//	POST   /api/resources/{id}/review   审核（body: {approve, note}）staff
//	POST   /api/resources/{id}/offline  下架 staff
//	POST   /api/resources/{id}/whitelist 添加白名单（body: {user_id}）owner/staff
//	DELETE /api/resources/{id}/whitelist/{user_id} 移除白名单
//	GET    /api/resources/{id}/logs     下载留痕（staff）
func (h *Handler) resourceItem(w http.ResponseWriter, r *http.Request, ac authCtx) {
	path := r.URL.Path
	// 处理白名单子路径
	if hasSuffix(path, "/whitelist") && r.Method == http.MethodPost {
		id, ok := parseID(path, "/api/resources/")
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid_id", "resource id required")
			return
		}
		var req struct {
			UserID string `json:"user_id"`
		}
		_ = jsonDecode(r, &req)
		if req.UserID == "" {
			writeErr(w, http.StatusBadRequest, "invalid_request", "user_id required")
			return
		}
		// 白名单管理：仅资源所有者或具备 resource:write 权限的人员
		res, err := h.store.GetResource(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		if res.OwnerID != ac.UserID {
			ok, _ := h.store.HasPermission(ac.UserID, "resource:write")
			if !ok {
				writeErr(w, http.StatusForbidden, "forbidden", "not your resource")
				return
			}
		}
		if err := h.store.AddWhitelist(id, req.UserID); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	if idx := indexOf(path, "/whitelist/"); idx >= 0 && r.Method == http.MethodDelete {
		head := path[len("/api/resources/"):idx]
		userID := path[idx+len("/whitelist/"):]
		id, err := strconvParse(head)
		if err != nil || userID == "" {
			writeErr(w, http.StatusBadRequest, "invalid_id", "bad path")
			return
		}
		res, err := h.store.GetResource(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, "not_found", "resource not found")
			return
		}
		if res.OwnerID != ac.UserID {
			ok, _ := h.store.HasPermission(ac.UserID, "resource:write")
			if !ok {
				writeErr(w, http.StatusForbidden, "forbidden", "not your resource")
				return
			}
		}
		if err := h.store.RemoveWhitelist(id, userID); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}

	id, ok := parseID(path, "/api/resources/")
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_id", "resource id required")
		return
	}
	res, err := h.store.GetResource(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	// 数据权限
	isOwner := res.OwnerID == ac.UserID
	if !ac.isStaffRole() && !isOwner {
		writeErr(w, http.StatusForbidden, "forbidden", "not your resource")
		return
	}

	switch {
	case r.Method == http.MethodGet && hasSuffix(path, "/logs"):
		if !ac.isStaffRole() {
			writeErr(w, http.StatusForbidden, "forbidden", "staff only")
			return
		}
		logs, err := h.store.ListDownloadLogs(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, logs)
	case r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, res)
	case r.Method == http.MethodPut:
		var upd Resource
		if err := jsonDecode(r, &upd); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if err := h.store.UpdateResource(id, upd); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		updated, _ := h.store.GetResource(id)
		writeJSON(w, http.StatusOK, updated)
	case r.Method == http.MethodDelete:
		if err := h.store.DeleteResource(id); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && hasSuffix(path, "/review"):
		if !ac.isStaffRole() {
			writeErr(w, http.StatusForbidden, "forbidden", "staff only")
			return
		}
		var req struct {
			Approve bool   `json:"approve"`
			Note    string `json:"note"`
		}
		_ = jsonDecode(r, &req)
		if err := h.store.ReviewResource(id, req.Approve, req.Note); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	case r.Method == http.MethodPost && hasSuffix(path, "/offline"):
		if !ac.isStaffRole() {
			writeErr(w, http.StatusForbidden, "forbidden", "staff only")
			return
		}
		if err := h.store.OfflineResource(id); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
	}
}

// resourceAccess POST /api/resources/access
// 校验下载权限并留痕（RES-201/206/109）：body: {resource_id, ip}
func (h *Handler) resourceAccess(w http.ResponseWriter, r *http.Request, ac authCtx) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}
	var req struct {
		ResourceID int64  `json:"resource_id"`
		IP         string `json:"ip"`
	}
	if err := jsonDecode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.store.CheckResourceAccess(req.ResourceID, ac.UserID, true); err != nil {
		if err == ErrResourceDenied {
			writeErr(w, http.StatusForbidden, "forbidden", "resource access denied")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// 下载留痕
	if err := h.store.LogDownload(req.ResourceID, ac.UserID, req.IP); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	res, _ := h.store.GetResource(req.ResourceID)
	out := map[string]any{"success": true, "downloads": res.Downloads}
	if res.FileID != "" {
		url, err := createFileShare(res.FileID, ac.UserID, ac.Username)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "share_failed", err.Error())
			return
		}
		out["download_url"] = url
	} else {
		out["note"] = "资源未绑定文件，无法下载"
	}
	writeJSON(w, http.StatusOK, out)
}

// createFileShare 调用 FileSvc 创建 30 分钟有效期文件分享，返回下载地址
func createFileShare(fileID, userID, username string) (string, error) {
	base := strings.TrimSuffix(os.Getenv("FILE_SVC_URL"), "/")
	if base == "" {
		base = "http://localhost:8082"
	}
	payload, _ := json.Marshal(map[string]any{"file_id": fileID, "share_type": "file", "expires_in": 1800})
	req, err := http.NewRequest(http.MethodPost, base+"/api/shares", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-User-ID", userID)
	if username != "" {
		req.Header.Set("X-Auth-Username", username)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("filesvc share request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("filesvc share failed: %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("filesvc share returned empty id")
	}
	return base + "/s/" + out.ID, nil
}

// indexOf 返回子串位置，未找到返回 -1
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// strconvParse 解析整数
func strconvParse(s string) (int64, error) {
	var n int64
	var neg bool
	if len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadInt
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

var errBadInt = &parseErr{}

type parseErr struct{}

func (*parseErr) Error() string { return "invalid integer" }
