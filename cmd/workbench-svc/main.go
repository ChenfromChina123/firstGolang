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

	"filesync/internal/jwks"
	"filesync/internal/workbench"
)

// ============================================================
// WorkbenchSvc 工作台业务数据微服务（端口 8083 默认）
// 职责：工作台（三台一体）业务数据持久化与查询
//   - 项目 / 待办 / 内容 / 通知 / 概览卡片 / 收入 / 流量 / 图表
//   - 数据落地 SQLite（DATA-001 数据底座），按用户隔离
//   - 认证：信任网关透传 X-Auth-User-ID（开发模式与 FileSvc 一致）
// ============================================================

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("========================================")
	log.Println(" FileSync Workbench Service (工作台业务数据微服务)")
	log.Println("========================================")

	port := getEnvInt("PORT", 8083)
	dataDir := getEnv("DATA_DIR", "./data_workbench")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// 1. 打开工作台数据库（SQLite）
	dbPath := filepath.Join(dataDir, "workbench.db")
	st, err := workbench.NewStore(dbPath)
	if err != nil {
		log.Fatalf("open workbench db: %v", err)
	}
	defer st.Close()
	log.Printf("[Init] 工作台数据库: %s", dbPath)

	// 2. 初始化默认管理员工作台数据（owner=admin，首次使用时 seed）
	if err := st.SeedIfEmpty("admin"); err != nil {
		log.Printf("[Warn] seed admin workbench data: %v", err)
	} else {
		log.Println("[Init] admin 工作台数据就绪")
	}

	// 3. 处理器与路由
	h := workbench.NewHandler(st)
	mux := h.Routes()

	// 4. 认证中间件（默认 JWT+JWKS；AUTH_MODE=dev 仅本机联调）
	authSvcURL := getEnv("AUTHSVC_URL", "http://localhost:8081")
	validator := jwks.New(authSvcURL)
	log.Printf("[WorkbenchSvc] 认证模式: %s (AUTHSVC_URL=%s)", os.Getenv("AUTH_MODE"), authSvcURL)

	// 5. 全局中间件：认证 + CORS + 安全头 + 日志
	var root http.Handler = mux
	root = workbench.AuthMiddleware(validator)(root)
	root = corsWrap(root)
	root = securityHeaders(root)
	root = logMiddleware(root)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("[WorkbenchSvc] 监听地址: %s", addr)
	log.Printf("[WorkbenchSvc] 健康检查: http://localhost%s/health", addr)
	log.Printf("[WorkbenchSvc] API: /api/workbench/* (需 X-Auth-User-ID 头)")

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
		log.Printf("[WB] %s %s -> %d (%s)  user=%s", r.Method, r.URL.Path, lrw.status, time.Since(start), uid)
	})
}

type logResp struct {
	http.ResponseWriter
	status int
}

func (l *logResp) WriteHeader(s int) { l.status = s; l.ResponseWriter.WriteHeader(s) }

// 避免未使用的 strings 包报错
var _ = strings.TrimSpace
