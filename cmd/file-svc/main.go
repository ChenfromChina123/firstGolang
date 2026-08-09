package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"filesync/internal/auth"
	"filesync/internal/cmdutil"
	"filesync/internal/config"
	"filesync/internal/handler"
	"filesync/internal/jwks"
	"filesync/internal/middleware"
	"filesync/internal/storage"
	"filesync/internal/store"
)

// ============================================================
// FileSvc 主入口（端口 8082 默认）
// 职责：文件上传/下载/列表/分享/回收站等文件操作
// ============================================================

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("========================================")
	log.Println(" FileSync File Service (文件微服务)")
	log.Println("========================================")

	port := cmdutil.GetEnvInt("PORT", 8082)
	dataDir := cmdutil.GetEnv("DATA_DIR", "./data")
	mysqlDSN := cmdutil.GetEnv("MYSQL_DSN", "")
	authSvcURL := config.AuthSvcURL()

	// OSS 占位（未来扩展，暂未使用）
	for _, v := range []string{"S3_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY", "S3_SECRET_KEY"} {
		_ = os.Getenv(v)
	}
	_ = cmdutil.GetEnvBool("S3_USE_SSL", false)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// 1. 数据库
	var db *store.DB
	var err error
	if mysqlDSN != "" {
		log.Printf("[Init] 使用 MySQL: %s", cmdutil.MaskDSN(mysqlDSN))
		db, err = store.NewMySQL(mysqlDSN)
	} else {
		sqlitePath := filepath.Join(dataDir, "filesync.db")
		log.Printf("[Init] 使用 SQLite: %s", sqlitePath)
		db, err = store.New(sqlitePath)
	}
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// 2. JWT 中间件：JWKS 公钥验签（AuthSvc 签发的 Access Token）
	// 安全修复：不再信任 X-Auth-* 透传头（AUTH_MODE=dev 时仅限本机联调开放）
	jwtAuth := middleware.NewJWKSJWTAuthMiddleware(jwks.New(authSvcURL))

	// 3. 初始化 storage.LocalStorage 并构造所有 Handler
	absData, _ := filepath.Abs(dataDir)
	local, err := storage.NewLocal(absData)
	if err != nil {
		log.Fatalf("init local storage: %v", err)
	}
	storages := map[string]storage.Storage{"local": local}
	fileH := handler.NewFileHandler(db, local, storages, nil)
	downloadH := handler.NewDownloadHandler(db, local)
	uploadH := handler.NewUploadHandler(db, local, storages)
	trashH := handler.NewTrashHandler(db, local, nil)
	// ShareHandler 使用 HMAC 密钥，未配置则使用随机（调试模式）
	shareSecret := cmdutil.GetEnv("SHARE_SECRET", "")
	if shareSecret == "" {
		shareSecret = auth.GenerateSecret()
		log.Println("[Warn] SHARE_SECRET 未配置，使用随机密钥（重启后分享链接失效）")
	}
	shareH := handler.NewShareHandler(db, local, []byte(shareSecret))

	// 4. 路由
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fmt.Sprintf(
			`{"service":"file-svc","status":"ok","time":"%s"}`,
			time.Now().Format(time.RFC3339),
		)))
	})

	// 文件列表 + 详情（与 server 一致：FileHandler.ServeHTTP 全量分流）
	mux.Handle("/api/files", jwtAuth.Wrap(fileH.ServeHTTP))
	mux.Handle("/api/files/", jwtAuth.Wrap(fileH.ServeHTTP))
	// 下载（内部解析前缀 /api/download/{id}，不可改挂 /api/files/download）
	mux.Handle("/api/download/", jwtAuth.Wrap(downloadH.ServeHTTP))
	// 上传（UploadHandler.ServeHTTP 内部按 /api/upload 前缀分流）
	mux.HandleFunc("/api/upload/init", cmdutil.Method("POST", jwtAuth.Wrap(uploadH.InitUpload)))
	mux.HandleFunc("/api/upload/chunk", cmdutil.Method("POST", jwtAuth.Wrap(uploadH.UploadChunk)))
	mux.HandleFunc("/api/upload/complete", cmdutil.Method("POST", jwtAuth.Wrap(uploadH.CompleteUpload)))
	mux.HandleFunc("/api/upload/status", cmdutil.Method("GET", jwtAuth.Wrap(uploadH.GetUploadStatus)))
	mux.HandleFunc("/api/upload/check", cmdutil.Method("POST", jwtAuth.Wrap(uploadH.CheckUpload)))
	// 回收站（TrashHandler.ServeHTTP 内部按 /api/trash 前缀分流）
	mux.Handle("/api/trash", jwtAuth.Wrap(trashH.ServeHTTP))
	mux.Handle("/api/trash/", jwtAuth.Wrap(trashH.ServeHTTP))
	// 分享
	mux.HandleFunc("/api/shares", cmdutil.Method("POST", jwtAuth.Wrap(func(w http.ResponseWriter, r *http.Request) {
		username := middleware.UsernameFromContext(r.Context())
		if username == "" {
			username = auth.UserIDFromContext(r.Context())
		}
		shareH.CreateShareCompat(w, r, username)
	})))
	mux.HandleFunc("/api/shares/", cmdutil.Method("GET", jwtAuth.Wrap(func(w http.ResponseWriter, r *http.Request) {
		shareH.GetShareInfoCompat(w, r)
	})))
	// 公开分享端点（内部只识别 /api/s/ 前缀，不可挂到 /s/）
	mux.Handle("/api/s/", shareH)

// 5. 全局中间件：CORS + 安全头 + 日志
	var rootHandler http.Handler = mux
	rootHandler = cmdutil.CorsWrap(rootHandler)
	rootHandler = middleware.SecurityHeaders(cmdutil.GetEnvBool("HTTPS", false), rootHandler)
	rootHandler = cmdutil.LogMiddleware("FS", func(r *http.Request) string {
		return middleware.UserIDFromContext(r.Context())
	}, rootHandler)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("[FileSvc] 监听地址: %s", addr)
	log.Printf("[FileSvc] 健康检查: http://localhost%s/health", addr)
	log.Printf("[FileSvc] 数据目录: %s", absData)
	log.Printf("[FileSvc] 认证模式: JWKS 验签 (AuthSvc: %s)", authSvcURL)

	if err := http.ListenAndServe(addr, rootHandler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ===== 辅助函数（公共实现见 internal/cmdutil） =====

// 避免未使用的 flag 包报错
var _ = flag.CommandLine
