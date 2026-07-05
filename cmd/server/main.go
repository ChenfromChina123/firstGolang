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
	"strings"
	"time"

	"filesync/internal/auth"
	"filesync/internal/handler"
	"filesync/internal/storage"
	"filesync/internal/store"

	"golang.org/x/crypto/acme/autocert"
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

	// === 认证系统初始化 ===
	// JWT 密钥：优先从环境变量读取，否则随机生成（每次重启失效）
	jwtSecret := getEnv("JWT_SECRET", "")
	if jwtSecret == "" {
		jwtSecret = auth.GenerateSecret()
		log.Printf("[Auth] WARNING: JWT_SECRET not set, generated random secret (tokens invalidated on restart)")
	}

	// 域名：用于 Cookie Domain 和 autocert
	domain := getEnv("DOMAIN", "")
	// HTTPS 模式：DOMAIN 设置时启用
	enableHTTPS := domain != ""

	jwtManager := auth.NewJWTManager(jwtSecret, domain, enableHTTPS)

	// 确保初始管理员存在
	initialUser := os.Getenv("FILESYNC_INITIAL_USERNAME")
	initialPass := os.Getenv("FILESYNC_INITIAL_PASSWORD")
	if err := handler.EnsureInitialAdmin(db, initialUser, initialPass); err != nil {
		log.Fatalf("ensure initial admin: %v", err)
	}

	// 登录速率限制器：5次/分钟/IP（rps=5/60≈0.083, burst=5）
	loginLimiter := auth.NewLoginRateLimiter(0.083, 5)
	authHandler := handler.NewAuthHandler(db, jwtManager)

	mux := http.NewServeMux()

	// 公开路由（无需认证）
	// /api/login 单独应用速率限制（5次/分钟/IP）
	loginHandler := loginLimiter.Middleware(http.HandlerFunc(authHandler.Login))
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		loginHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/logout", authHandler.Logout)
	mux.Handle("/api/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if rc != nil {
			stats := rc.WatchdogStats()
			stats["service"] = "filesync"
			json.NewEncoder(w).Encode(stats)
		} else {
			w.Write([]byte(`{"status":"ok","service":"filesync","redis":"disabled"}`))
		}
	}))

	// 受保护路由（需要认证）
	mux.HandleFunc("/api/me", authHandler.Me)
	mux.Handle("/api/upload/", uploadHandler)
	mux.Handle("/api/download/", downloadHandler)
	mux.Handle("/api/files/", fileHandler)
	mux.Handle("/api/files", fileHandler)

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

	// === JWT 认证中间件 ===
	// 白名单：登录/登出/健康检查/Web 静态资源/根路径
	whitelist := []string{
		"/api/login",
		"/api/logout",
		"/api/health",
		"/web/",
		"/",
	}

	// 用 JWT 中间件包装 mux
	authedHandler := jwtManager.Middleware(whitelist, mux)

	log.Printf("API endpoints:")
	log.Printf("  POST   /api/login          - Login (rate limited: 5/min)")
	log.Printf("  POST   /api/logout         - Logout")
	log.Printf("  GET    /api/me             - Current user info (auth required)")
	log.Printf("  POST   /api/upload/init     - Initialize upload (auth)")
	log.Printf("  POST   /api/upload/chunk    - Upload chunk (auth)")
	log.Printf("  GET    /api/upload/status   - Upload progress (auth)")
	log.Printf("  POST   /api/upload/complete - Complete upload (auth)")
	log.Printf("  GET    /api/download/{id}   - Download file (auth, supports Range)")
	log.Printf("  GET    /api/download/dir    - Download directory as ZIP (auth)")
	log.Printf("  GET    /api/files           - List files (auth, prefix=xxx)")
	log.Printf("  GET    /api/files/{id}      - File info (auth)")
	log.Printf("  DELETE /api/files/{id}      - Delete file (auth)")
	log.Printf("  DELETE /api/files           - Delete directory (auth, prefix=xxx)")
	log.Printf("  POST   /api/files/mkdir     - Create directory (auth, path=xxx)")
	log.Printf("  POST   /api/files/rename    - Rename/move file (auth, id, new_filename)")
	log.Printf("  GET    /api/health          - Health check (public)")
	log.Printf("  GET    /web/                - Web console (public static)")

	// === 启动服务器 ===
	if enableHTTPS {
		// HTTPS 模式：autocert 自动申请 Let's Encrypt 证书
		log.Printf("[HTTPS] enabled, domain=%s", domain)
		log.Printf("[HTTPS] autocert will request Let's Encrypt certificate")

		// 证书缓存目录
		certDir := getEnv("CERT_DIR", "/opt/filesync/certs")
		os.MkdirAll(certDir, 0700)

		hostPolicy := autocert.HostWhitelist(domain)
		if extraDomains := os.Getenv("EXTRA_DOMAINS"); extraDomains != "" {
			extras := strings.Split(extraDomains, ",")
			for i := range extras {
				extras[i] = strings.TrimSpace(extras[i])
			}
			all := append([]string{domain}, extras...)
			hostPolicy = autocert.HostWhitelist(all...)
		}

		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: hostPolicy,
			Cache:      autocert.DirCache(certDir),
		}

		// HTTPS server (443)
		httpsServer := &http.Server{
			Addr:         ":443",
			Handler:      authedHandler,
			TLSConfig:    manager.TLSConfig(),
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 30 * time.Minute,
			IdleTimeout:  60 * time.Second,
		}

		// HTTP server (80) → HTTPS 重定向 + ACME 验证
		httpHandler := manager.HTTPHandler(nil)
		httpServer := &http.Server{
			Addr:         ":80",
			Handler:      httpHandler,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}

		// 启动 HTTP (80) 用于 ACME 验证 + 重定向
		go func() {
			log.Printf("[HTTPS] HTTP server on :80 (ACME + redirect)")
			if err := httpServer.ListenAndServe(); err != nil {
				log.Fatalf("[HTTPS] HTTP server error: %v", err)
			}
		}()

		// 启动 HTTPS (443)
		log.Printf("[HTTPS] HTTPS server on :443")
		log.Printf("FileSync Server starting on https://%s", domain)
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil {
			log.Fatalf("[HTTPS] server error: %v", err)
		}
	} else {
		// HTTP 模式（开发环境）
		addr := fmt.Sprintf(":%s", port)
		log.Printf("FileSync Server starting on http://0.0.0.0%s", addr)
		server := &http.Server{
			Addr:           addr,
			Handler:        authedHandler,
			ReadTimeout:    10 * time.Second,
			WriteTimeout:   30 * time.Minute,
			IdleTimeout:    60 * time.Second,
			MaxHeaderBytes: 1 << 16,
		}
		if err := server.ListenAndServe(); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
