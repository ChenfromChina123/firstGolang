package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/model"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// shareTokenTTL 分享下载 token 有效期（30 分钟）
const shareTokenTTL = 30 * time.Minute

// ShareHandler 处理分享链接的创建、管理和公开访问
type ShareHandler struct {
	db              *store.DB
	storage         storage.Storage
	secret          []byte                 // HMAC 签名密钥（复用 JWT secret）
	downloadLimiter *auth.LoginRateLimiter // 分享下载频率限制（每 IP 每分钟 10 次）
	infoLimiter     *auth.LoginRateLimiter // 分享信息查询频率限制（每 IP 每分钟 60 次，防暴力枚举）
}

// NewShareHandler 创建分享 handler
// secret 用于签名下载 token（防盗链），通常传入 JWT secret
func NewShareHandler(db *store.DB, st storage.Storage, secret []byte) *ShareHandler {
	return &ShareHandler{
		db:              db,
		storage:         st,
		secret:          secret,
		downloadLimiter: auth.NewLoginRateLimiter(0.1667, 10), // 10 次/分钟
		// infoLimiter: 30 次/分钟/IP（rps=0.2 即每 5 秒恢复 1 个 token，burst=30）
		// 收紧原因：rps=1.0 时请求间隔 >1s 不会限流，调整为 rps=0.2 使手动测试也能触发
		infoLimiter: auth.NewLoginRateLimiter(0.2, 30), // 30 次/分钟/IP，阻拦分享 ID 枚举攻击
	}
}

// generateShareToken 生成分享下载 token（HMAC-SHA256，绑定 share_id + 过期时间）。
// token 格式：{expire_unix}.{hmac_hex}，有效期 30 分钟。
// 不绑定 IP 避免 NAT 环境下正常用户被拒。
func generateShareToken(shareID string, secret []byte, ttl time.Duration) string {
	expire := time.Now().Add(ttl).Unix()
	msg := fmt.Sprintf("%s:%d", shareID, expire)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%d.%s", expire, sig)
}

// validateShareToken 校验 token 有效性（未过期 + 签名匹配 + share_id 绑定）。
// 使用 hmac.Equal 做常量时间比较，防止时序攻击。
func validateShareToken(token, shareID string, secret []byte) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expire, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expire {
		return false
	}
	msg := fmt.Sprintf("%s:%d", shareID, expire)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(msg))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(expectedSig))
}

// createShareRequest 创建分享请求体
type createShareRequest struct {
	FileID    string `json:"file_id,omitempty"`    // 单文件分享：文件 ID
	DirPrefix string `json:"dir_prefix,omitempty"` // 目录分享：目录前缀
	ShareType string `json:"share_type"`           // "file" | "dir"
	ExpiresIn int    `json:"expires_in"`           // 过期秒数（0=永久）
	Password  string `json:"password,omitempty"`   // 访问密码（可选，1-64 字符；空字符串=无密码）
	SpaceID   string `json:"space_id,omitempty"`   // 目录分享所在空间 ID（空=默认空间）
}

// shareAuthSessionTTL 分享密码会话 cookie 有效期（7 天）
const shareAuthSessionTTL = 7 * 24 * time.Hour

// shareAuthCookieName 返回指定分享的认证 cookie 名
func shareAuthCookieName(shareID string) string {
	return "share_auth_" + shareID
}

// signShareAuth 生成分享认证 cookie 的 HMAC 签名值
// 格式：{expire_unix}.{hmac_hex}，绑定 share_id + 过期时间
func (h *ShareHandler) signShareAuth(shareID string, expire int64) string {
	msg := fmt.Sprintf("%s:%d", shareID, expire)
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(msg))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("%d.%s", expire, sig)
}

// validateShareAuth 校验分享认证 cookie 是否有效（未过期 + 签名匹配 + share_id 绑定）
func (h *ShareHandler) validateShareAuth(cookieValue, shareID string) bool {
	parts := strings.SplitN(cookieValue, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expire, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expire {
		return false
	}
	msg := fmt.Sprintf("%s:%d", shareID, expire)
	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte(msg))
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(parts[1]), []byte(expectedSig))
}

// isShareAuthed 检查请求是否已通过指定分享的密码认证
func (h *ShareHandler) isShareAuthed(r *http.Request, shareID string) bool {
	c, err := r.Cookie(shareAuthCookieName(shareID))
	if err != nil {
		return false
	}
	return h.validateShareAuth(c.Value, shareID)
}

// requireShareAuth 检查分享是否需要密码认证；若需要且未认证则返回 401
// 返回 true 表示已通过认证（或无需认证），可继续后续处理
func (h *ShareHandler) requireShareAuth(w http.ResponseWriter, r *http.Request, s *model.Share) bool {
	if s.PasswordHash == "" {
		return true
	}
	if h.isShareAuthed(r, s.ID) {
		return true
	}
	http.Error(w, `{"error":"password_required","message":"此分享需要密码访问"}`, http.StatusUnauthorized)
	return false
}

// createShareResponse 创建分享响应体
type createShareResponse struct {
	ID        string `json:"id"`
	ShareType string `json:"share_type"`
	URL       string `json:"url"`                  // 分享页面 URL（/web/share.html?id=xxx）
	ExpiresAt *int64 `json:"expires_at,omitempty"` // Unix 时间戳
}

// ServeHTTP 路由分发：
// 认证路由（/api/share, /api/share/）：
//
//	GET    /api/share           - 列出当前用户的分享
//	POST   /api/share           - 创建分享
//	DELETE /api/share/{id}      - 删除分享
//
// 公开路由（/api/s/）：
//
//	GET    /api/s/{id}          - 获取分享信息
//	GET    /api/s/{id}/download - 下载文件或目录
func (h *ShareHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := r.URL.Path

	// 公开路由：/api/s/{id} 和 /api/s/{id}/download
	if strings.HasPrefix(path, "/api/s/") {
		h.handlePublic(w, r)
		return
	}

	// 认证路由：/api/share 和 /api/share/{id}
	username := auth.UsernameFromContext(r.Context())
	if username == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// /api/share（无 ID 后缀）
	if path == "/api/share" || path == "/api/share/" {
		switch r.Method {
		case http.MethodGet:
			h.listShares(w, r, username)
		case http.MethodPost:
			h.createShare(w, r, username)
		default:
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		}
		return
	}

	// /api/share/save - 转存分享文件到自己账户
	if path == "/api/share/save" && r.Method == http.MethodPost {
		h.saveShareToMyFiles(w, r, username)
		return
	}

	// /api/share/{id}
	if strings.HasPrefix(path, "/api/share/") {
		id := strings.TrimPrefix(path, "/api/share/")
		id = strings.TrimSuffix(id, "/")
		if id == "" {
			http.Error(w, `{"error":"id_required"}`, http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodDelete {
			h.deleteShare(w, r, username, id)
		} else {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		}
		return
	}

	http.NotFound(w, r)
}
