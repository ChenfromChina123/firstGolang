package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"filesync/internal/cmdutil"
	"filesync/internal/config"
	"filesync/internal/jwks"
	"filesync/internal/middleware"
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

	port := cmdutil.GetEnvInt("PORT", 8083)
	dataDir := cmdutil.GetEnv("DATA_DIR", "./data_workbench")

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
	authSvcURL := config.AuthSvcURL()
	validator := jwks.New(authSvcURL)
	log.Printf("[WorkbenchSvc] 认证模式: %s (AUTHSVC_URL=%s)", os.Getenv("AUTH_MODE"), authSvcURL)

	// 5. 全局中间件：认证 + CORS + 安全头 + 日志
	var root http.Handler = mux
	root = workbench.AuthMiddleware(validator)(root)
	root = cmdutil.CorsWrap(root)
	root = middleware.SecurityHeaders(false, root)
	root = cmdutil.LogMiddleware("WB", func(r *http.Request) string {
		if owner, ok := workbench.OwnerFromContext(r.Context()); ok {
			return owner
		}
		return r.Header.Get("X-Auth-User-ID")
	}, root)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("[WorkbenchSvc] 监听地址: %s", addr)
	log.Printf("[WorkbenchSvc] 健康检查: http://localhost%s/health", addr)
	log.Printf("[WorkbenchSvc] API: /api/workbench/* (需 X-Auth-User-ID 头)")

	if err := http.ListenAndServe(addr, root); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
