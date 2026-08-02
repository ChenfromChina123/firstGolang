package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/handler"
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

	port := getEnvInt("PORT", 8082)
	dataDir := getEnv("DATA_DIR", "./data")
	mysqlDSN := getEnv("MYSQL_DSN", "")
	_ = getEnv("AUTHSVC_URL", "http://127.0.0.1:8081")

	// OSS 占位（未来扩展，暂未使用）
	for _, v := range []string{"S3_ENDPOINT", "S3_REGION", "S3_BUCKET", "S3_ACCESS_KEY", "S3_SECRET_KEY"} {
		_ = os.Getenv(v)
	}
	_ = getEnvBool("S3_USE_SSL", false)

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// 1. 数据库
	var db *store.DB
	var err error
	if mysqlDSN != "" {
		log.Printf("[Init] 使用 MySQL: %s", maskDSN(mysqlDSN))
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

	// 2. JWT 中间件（开发模式：nil=纯网关透传信任 X-Auth-User-ID）
	jwtAuth := middleware.NewJWTAuthMiddleware(nil)

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
	shareSecret := getEnv("SHARE_SECRET", "")
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

	// 文件列表 + 详情
	mux.HandleFunc("/api/files", method("GET", jwtAuth.Wrap(fileH.ListFiles)))
	mux.HandleFunc("/api/files/info/", method("GET", jwtAuth.Wrap(fileH.GetFileInfo)))
	// 下载
	mux.HandleFunc("/api/files/download", method("GET", jwtAuth.Wrap(downloadH.DownloadFile)))
	mux.HandleFunc("/api/files/download_dir", method("GET", jwtAuth.Wrap(downloadH.DownloadDir)))
	// 上传
	mux.HandleFunc("/api/upload/init", method("POST", jwtAuth.Wrap(uploadH.InitUpload)))
	mux.HandleFunc("/api/upload/chunk", method("POST", jwtAuth.Wrap(uploadH.UploadChunk)))
	mux.HandleFunc("/api/upload/complete", method("POST", jwtAuth.Wrap(uploadH.CompleteUpload)))
	mux.HandleFunc("/api/upload/status", method("GET", jwtAuth.Wrap(uploadH.GetUploadStatus)))
	mux.HandleFunc("/api/upload/check", method("POST", jwtAuth.Wrap(uploadH.CheckUpload)))
	// 回收站
	mux.HandleFunc("/api/files/trash", method("GET", jwtAuth.Wrap(trashH.ListTrash)))
	mux.HandleFunc("/api/files/trash/", method("POST", jwtAuth.Wrap(fileH.DeleteFileByID)))
	mux.HandleFunc("/api/files/restore/", method("POST", jwtAuth.Wrap(trashH.RestoreFile)))
	mux.HandleFunc("/api/files/purge/", method("DELETE", jwtAuth.Wrap(trashH.PermanentDelete)))
	// 分享
	mux.HandleFunc("/api/shares", method("POST", jwtAuth.Wrap(func(w http.ResponseWriter, r *http.Request) {
		username := middleware.UsernameFromContext(r.Context())
		if username == "" {
			username = auth.UserIDFromContext(r.Context())
		}
		shareH.CreateShareCompat(w, r, username)
	})))
	mux.HandleFunc("/api/shares/", method("GET", jwtAuth.Wrap(func(w http.ResponseWriter, r *http.Request) {
		shareH.GetShareInfoCompat(w, r)
	})))
	// 公开分享端点
	mux.Handle("/s/", shareH)

	// 5. 全局中间件：CORS + 安全头 + 日志
	var rootHandler http.Handler = mux
	rootHandler = corsWrapFS(rootHandler)
	rootHandler = middleware.SecurityHeaders(getEnvBool("HTTPS", false), rootHandler)
	rootHandler = logFileMiddleware(rootHandler)

	addr := fmt.Sprintf(":%d", port)
	log.Printf("[FileSvc] 监听地址: %s", addr)
	log.Printf("[FileSvc] 健康检查: http://localhost%s/health", addr)
	log.Printf("[FileSvc] 数据目录: %s", absData)
	log.Printf("[FileSvc] 认证模式: 网关透传 (X-Auth-User-ID)")

	if err := http.ListenAndServe(addr, rootHandler); err != nil {
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
func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.EqualFold(v, "true") || v == "1"
	}
	return fallback
}
func maskDSN(dsn string) string {
	parts := strings.SplitN(dsn, "@", 2)
	if len(parts) != 2 {
		return dsn
	}
	up := strings.SplitN(parts[0], ":", 2)
	if len(up) != 2 {
		return dsn
	}
	return up[0] + ":***@" + parts[1]
}

func method(m string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.EqualFold(r.Method, m) {
			w.Header().Set("Allow", m)
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

func corsWrapFS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With, Accept, Origin, Range, X-Auth-User-ID, X-Auth-Username, X-Auth-Role, X-Auth-Scope")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, Content-Length, Accept-Ranges")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logFileMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &logFileResp{ResponseWriter: w, status: 200}
		next.ServeHTTP(lrw, r)
		uid := r.Header.Get("X-Auth-User-ID")
		if uid == "" {
			uid = middleware.UserIDFromContext(r.Context())
		}
		log.Printf("[FS] %s %s -> %d (%s)  user=%s",
			r.Method, r.URL.Path, lrw.status, time.Since(start), uid)
	})
}

type logFileResp struct {
	http.ResponseWriter
	status int
}

func (l *logFileResp) WriteHeader(s int) { l.status = s; l.ResponseWriter.WriteHeader(s) }

// 避免未使用的 flag 包报错
var _ = flag.CommandLine
