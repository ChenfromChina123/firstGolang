package handler

import (
	"crypto/rand"
	"encoding/hex"
	"filesync/internal/model"
	"filesync/internal/store"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func generateShareID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:8], nil
}

// getOrSetVisitorCookie 获取或设置 visitor cookie。
// cookie 名 "visitor"，值 32 字符 hex，HttpOnly, SameSite=Lax, 30 天有效。
// 跨所有分享复用同一 visitor ID（标识同一访客）。
func getOrSetVisitorCookie(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("visitor")
	if err == nil && cookie.Value != "" {
		return cookie.Value
	}
	// 生成新 visitor ID
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 降级：用时间戳
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	visitorID := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     "visitor",
		Value:    visitorID,
		Path:     "/",
		MaxAge:   30 * 24 * 3600, // 30 天
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return visitorID
}

// resolveSharePath 规范化并校验分享内子路径，防止路径遍历逃逸。
// shareDirPrefix：分享目录前缀（如 "docs/"）；requestPath：访客请求的子路径（如 "sub/file.pdf" 或 ""）。
// 返回完整路径（如 "docs/sub/file.pdf"）。requestPath 为空时返回 shareDirPrefix。
// 校验：去开头/、合并//、validateFilePath、结果必须以 shareDirPrefix 为前缀。
func resolveSharePath(shareDirPrefix, requestPath string) (string, error) {
	// 规范化请求路径
	p := strings.TrimSpace(requestPath)
	p = strings.TrimPrefix(p, "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return shareDirPrefix, nil
	}
	// 校验路径合法性（禁止 .. \ 等）
	if err := validateFilePath(p); err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	// 拼接完整路径并校验前缀（双重防护）
	full := shareDirPrefix + p
	if !strings.HasPrefix(full, shareDirPrefix) {
		return "", fmt.Errorf("path escape detected")
	}
	return full, nil
}

// isFileInShare 校验文件是否属于分享范围。
// file 类型：f.ID == s.FileID；dir 类型：f.Filename 以 s.DirPrefix 为前缀。
// 用于转存接口防止越权转存非分享范围内的文件。
func isFileInShare(f *model.FileRecord, s *model.Share) bool {
	if s.ShareType == "file" {
		return f.ID == s.FileID
	}
	// dir 类型：filename 必须以分享前缀开头
	return s.DirPrefix != "" && strings.HasPrefix(f.Filename, s.DirPrefix)
}

// generateUniqueFilename 生成不冲突的文件名（同 owner + 目标空间下）。
// 若 filename 已存在，自动追加 _1, _2, ... 后缀直到不冲突。
// 例：report.pdf → report_1.pdf → report_2.pdf
// 用于转存功能避免覆盖已有同名文件。
func generateUniqueFilename(filename, spaceID, owner string, db *store.DB) string {
	// 快速路径：原名不冲突直接返回
	existing, _ := db.FindFileByName(spaceID, filename, owner)
	if existing == nil {
		return filename
	}
	// 拆分扩展名
	ext := ""
	base := filename
	if dot := strings.LastIndex(filename, "."); dot > 0 {
		ext = filename[dot:]
		base = filename[:dot]
	}
	// 尝试 base_1.ext, base_2.ext, ... 最多 9999
	for i := 1; i < 10000; i++ {
		candidate := fmt.Sprintf("%s_%d%s", base, i, ext)
		existing, _ := db.FindFileByName(spaceID, candidate, owner)
		if existing == nil {
			return candidate
		}
	}
	// 兜底：用时间戳
	return fmt.Sprintf("%s_%d%s", base, time.Now().Unix(), ext)
}

// ==================== FileSvc 微服务兼容方法 ====================

// CreateShareCompat 适配 FileSvc 的 /api/shares 端点。
// 内部调用 ShareHandler 的 createShare(username)，避免暴露非导出方法。
func (h *ShareHandler) CreateShareCompat(w http.ResponseWriter, r *http.Request, username string) {
	if username == "" {
		// 网关透传模式下，X-Auth-Username 可能为空，用 X-Auth-User-ID 兜底
		username = r.Header.Get("X-Auth-User-ID")
	}
	if username == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	h.createShare(w, r, username)
}

// GetShareInfoCompat 适配 FileSvc 的 /api/shares/{id} 端点。
// 内部调用 getSharePublic。
func (h *ShareHandler) GetShareInfoCompat(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	id := strings.TrimPrefix(p, "/api/shares/")
	id = strings.Trim(id, "/")
	if id == "" {
		// 没有 id → 返回用户自己的分享列表（需要用户名）
		username := r.Header.Get("X-Auth-Username")
		if username == "" {
			username = r.Header.Get("X-Auth-User-ID")
		}
		if username == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		h.listShares(w, r, username)
		return
	}
	h.getSharePublic(w, r, id)
}
