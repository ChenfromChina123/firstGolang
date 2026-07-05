package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"filesync/internal/handler"
	"filesync/internal/store"
	"filesync/internal/storage"
)

func main() {
	// Explicit CPU affinity — prevents container/VM environment misdetection
	runtime.GOMAXPROCS(runtime.NumCPU())

	// Configuration
	port := getEnv("PORT", "8080")
	dataDir := getEnv("DATA_DIR", "./data")
	storageType := getEnv("STORAGE_TYPE", "local")

	// Ensure data directory exists
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		log.Fatalf("invalid data dir: %v", err)
	}
	if err := os.MkdirAll(absDataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// Initialize database
	// 优先级：MYSQL_DSN > SQLite（默认）
	// 双驱动共存：MYSQL_DSN 环境变量存在则用 MySQL，否则用 SQLite
	var db *store.DB
	mysqlDSN := os.Getenv("MYSQL_DSN")
	if mysqlDSN != "" {
		db, err = store.NewMySQL(mysqlDSN)
		if err != nil {
			log.Fatalf("init mysql: %v", err)
		}
	} else {
		dbPath := filepath.Join(absDataDir, "filesync.db")
		db, err = store.New(dbPath)
		if err != nil {
			log.Fatalf("init sqlite: %v", err)
		}
	}
	defer db.Close()

	// Enable async batch write (bypasses SQLite SetMaxOpenConns(1) bottleneck)
	// MySQL 模式下也启用，批量写入减少 RTT
	db.EnableAsyncWrite()

	// Initialize storage backend
	var st storage.Storage
	switch storageType {
	case "local":
		st, err = storage.NewLocal(absDataDir)
		if err != nil {
			log.Fatalf("init local storage: %v", err)
		}
		log.Printf("Storage: local -> %s", absDataDir)
	case "s3":
		// S3 configuration from environment
		s3Cfg := storage.S3Config{
			Endpoint:  getEnv("S3_ENDPOINT", "localhost:9000"),
			Region:    getEnv("S3_REGION", "us-east-1"),
			Bucket:    getEnv("S3_BUCKET", "filesync"),
			AccessKey: getEnv("S3_ACCESS_KEY", ""),
			SecretKey: getEnv("S3_SECRET_KEY", ""),
			UseSSL:    getEnv("S3_USE_SSL", "false") == "true",
		}
		st, err = storage.NewS3(s3Cfg)
		if err != nil {
			log.Fatalf("init s3 storage: %v", err)
		}
		log.Printf("Storage: s3 -> bucket=%s endpoint=%s", s3Cfg.Bucket, s3Cfg.Endpoint)
	default:
		log.Fatalf("unknown storage type: %s (use 'local' or 's3')", storageType)
	}

	// Initialize Redis cache (optional)
	// Priority: Sentinel > Single-instance > None
	redisSentinelAddrs := os.Getenv("REDIS_SENTINEL_ADDRS")
	var rc *store.RedisCache
	var uploadHandler *handler.UploadHandler

	if redisSentinelAddrs != "" {
		// === Mode 1: Redis Sentinel (分布式 + 投票选主) ===
		masterName := getEnv("REDIS_SENTINEL_MASTER", "mymaster")
		redisPassword := os.Getenv("REDIS_PASSWORD")
		redisDB, _ := strconv.Atoi(getEnv("REDIS_DB", "0"))
		cacheTTL := 10 * time.Minute

		cfg := store.RedisSentinelConfig{
			MasterName:    masterName,
			SentinelAddrs: store.ParseSentinelAddrs(redisSentinelAddrs),
			Password:      redisPassword,
			DB:            redisDB,
			TTL:           cacheTTL,
		}
		rc = store.NewRedisSentinel(cfg)
		log.Printf("Redis: SENTINEL mode -> master=%s addrs=%v db=%d",
			masterName, cfg.SentinelAddrs, redisDB)
		uploadHandler = handler.NewUploadHandlerWithRedis(db, st, rc)

	} else if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		// === Mode 2: Single-instance Redis ===
		redisPassword := os.Getenv("REDIS_PASSWORD")
		redisDB, _ := strconv.Atoi(getEnv("REDIS_DB", "0"))

		rc = store.NewRedisCache(redisAddr, redisPassword, redisDB, 10*time.Minute)
		log.Printf("Redis: STANDALONE mode -> addr=%s db=%d", redisAddr, redisDB)
		uploadHandler = handler.NewUploadHandlerWithRedis(db, st, rc)

	} else {
		// === Mode 3: No Redis (SQLite only) ===
		uploadHandler = handler.NewUploadHandler(db, st)
	}

	// Register handlers
	downloadHandler := handler.NewDownloadHandler(db, st)
	fileHandler := handler.NewFileHandler(db, st, rc)

	mux := http.NewServeMux()
	mux.Handle("/api/upload/", uploadHandler)
	mux.Handle("/api/download/", downloadHandler)
	mux.Handle("/api/files/", fileHandler)
	mux.Handle("/api/files", fileHandler)

	// Health check (with Redis watchdog status if available)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if rc != nil {
			stats := rc.WatchdogStats()
			stats["service"] = "filesync"
			json.NewEncoder(w).Encode(stats)
		} else {
			w.Write([]byte(`{"status":"ok","service":"filesync","redis":"disabled"}`))
		}
	})

	// 静态文件服务：前端 Web 控制台（/web/ 路径 + 根路径重定向）
	// 前端仅做页面展示，所有业务方法走 /api/* 后端（规则15）
	webDir := getEnv("WEB_DIR", "./web")
	if _, err := os.Stat(webDir); err == nil {
		mux.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.Dir(webDir))))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/web/index.html", http.StatusFound)
				return
			}
			http.NotFound(w, r)
		})
		log.Printf("Web console: /web/ -> %s", webDir)
	} else {
		log.Printf("Web console: directory %s not found (set WEB_DIR to enable)", webDir)
	}

	addr := fmt.Sprintf(":%s", port)
	log.Printf("FileSync Server starting on http://0.0.0.0%s", addr)
	log.Printf("API endpoints:")
	log.Printf("  POST   /api/upload/init     - Initialize upload")
	log.Printf("  POST   /api/upload/chunk    - Upload chunk")
	log.Printf("  GET    /api/upload/status   - Upload progress")
	log.Printf("  POST   /api/upload/complete - Complete upload")
	log.Printf("  GET    /api/download/{id}   - Download file (supports Range)")
	log.Printf("  GET    /api/download/dir    - Download directory as ZIP (prefix=xxx)")
	log.Printf("  GET    /api/files           - List files (prefix=xxx for dir)")
	log.Printf("  GET    /api/files/{id}      - File info")
	log.Printf("  DELETE /api/files/{id}      - Delete file")
	log.Printf("  DELETE /api/files           - Delete directory (prefix=xxx)")
	log.Printf("  POST   /api/files/mkdir     - Create directory (path=xxx)")
	log.Printf("  POST   /api/files/rename    - Rename/move file (id, new_filename)")
	log.Printf("  GET    /api/health          - Health check")
	log.Printf("  GET    /web/                - Web console (static)")

	server := &http.Server{
		Addr:           addr,
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   30 * time.Minute, // 大文件下载需要足够长的写超时（30MB/1MB/s=30s，30min 可覆盖 1GB 慢速下载）
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 16, // 64KB
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
