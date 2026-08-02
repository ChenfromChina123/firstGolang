package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filesync/internal/biz"
)

// ============================================================
// BizSvc 业务数据微服务（端口 8084 默认）
// 职责（V1.0 必交付）：
//   - RBAC 权限体系（AUTH-001）+ 三台一体菜单
//   - 订单管理（ORD-101~113 / BIZ-001）
//   - 工单管理（WO-101~112）
//   - 资源管理（RES-101~111 / RES-201~210 / RES-ACC-001）
//   - 服务管理（SRV-101~108）
// 数据落地 SQLite（DATA-001），按用户/角色隔离（AUTH-001）
// 认证：信任网关透传 X-Auth-* 头（开发模式与 FileSvc 一致）
// ============================================================

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("========================================")
	log.Println(" FileSync Biz Service (业务数据微服务)")
	log.Println("========================================")

	port := getEnvInt("PORT", 8084)
	dataDir := getEnv("DATA_DIR", "./data_biz")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// 1. 打开业务数据库
	dbPath := filepath.Join(dataDir, "biz.db")
	st, err := biz.NewStore(dbPath)
	if err != nil {
		log.Fatalf("open biz db: %v", err)
	}
	defer st.Close()
	log.Printf("[Init] 业务数据库: %s", dbPath)

	// 2. 处理器与路由
	h := biz.NewHandler(st)
	mux := h.Routes()

	// 3. 认证中间件（默认 JWT+JWKS；AUTH_MODE=dev 仅本机联调）
	authSvcURL := getEnv("AUTHSVC_URL", "http://localhost:8081")
	validator := biz.NewValidator(authSvcURL)
	log.Printf("[BizSvc] 认证模式: %s (AUTHSVC_URL=%s)", biz.AuthMode(), authSvcURL)

	// 4. 全局中间件：认证 + CORS + 安全头 + 日志
	var root http.Handler = mux
	root = biz.AuthMiddleware(validator)(root)
	root = corsWrap(root)
	root = securityHeaders(root)
	root = logMiddleware(root)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("[BizSvc] 监听地址: %s", addr)
	log.Printf("[BizSvc] 健康检查: http://localhost%s/health", addr)
	log.Printf("[BizSvc] API: /api/rbac/* /api/orders/* /api/tickets/* /api/resources/* /api/services/*")

	if err := http.ListenAndServe(addr, root); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ===== 辅助函数 =====

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func corsWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With, Accept, Origin, X-Auth-User-ID, X-Auth-Username, X-Auth-Role, X-Auth-Scope")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(w, r)
	})
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &logResp{ResponseWriter: w, status: 200}
		next.ServeHTTP(lrw, r)
		uid := r.Header.Get("X-Auth-User-ID")
		if uid == "" {
			uid = "-"
		}
		log.Printf("[BIZ] %s %s -> %d (%s)  user=%s", r.Method, r.URL.Path, lrw.status, time.Since(start), uid)
	})
}

type logResp struct {
	http.ResponseWriter
	status int
}

func (l *logResp) WriteHeader(s int) { l.status = s; l.ResponseWriter.WriteHeader(s) }

// 避免未使用 strings 包报错
var _ = strings.TrimSpace
