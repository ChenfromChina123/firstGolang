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
	"filesync/internal/email"
	"filesync/internal/handler"
	"filesync/internal/middleware"
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
	// STORAGE_TYPE 已废弃：混合存储由 S3_ENDPOINT 是否配置决定（见下方存储初始化）。

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

	// Initialize storage backends (hybrid: local always + optional S3/OSS)
	// 本地存储始终启用：承载历史文件、缩略图/转码缓存目录、Mkdir 默认后端。
	localSt, err := storage.NewLocal(absDataDir)
	if err != nil {
		log.Fatalf("init local storage: %v", err)
	}
	log.Printf("Storage: local -> %s", absDataDir)

	// S3/OSS 仅在配置了 S3_ENDPOINT 时启用（混合存储：旧文件留本地，新文件可选 OSS）。
	var s3St storage.Storage
	if s3Endpoint := getEnv("S3_ENDPOINT", ""); s3Endpoint != "" {
		s3Cfg := storage.S3Config{
			Endpoint:  s3Endpoint,
			Region:    getEnv("S3_REGION", "us-east-1"),
			Bucket:    getEnv("S3_BUCKET", "filesync"),
			AccessKey: getEnv("S3_ACCESS_KEY", ""),
			SecretKey: getEnv("S3_SECRET_KEY", ""),
			UseSSL:    getEnv("S3_USE_SSL", "false") == "true",
		}
		s3St, err = storage.NewS3(s3Cfg)
		if err != nil {
			log.Fatalf("init s3 storage: %v", err)
		}
		log.Printf("Storage: s3(oss) -> bucket=%s endpoint=%s", s3Cfg.Bucket, s3Cfg.Endpoint)
	} else {
		log.Printf("Storage: s3 not configured (S3_ENDPOINT empty) — OSS disabled, local only")
	}

	// Router 处理读取链路路由；storages map 供写入链路按 type 选后端。
	router := storage.NewRouter(localSt, s3St)
	storages := map[string]storage.Storage{"local": localSt}
	if s3St != nil {
		storages["s3"] = s3St
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
		uploadHandler = handler.NewUploadHandlerWithRedis(db, router, storages, rc)

	} else if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		// === Mode 2: Single-instance Redis ===
		redisPassword := os.Getenv("REDIS_PASSWORD")
		redisDB, _ := strconv.Atoi(getEnv("REDIS_DB", "0"))

		rc = store.NewRedisCache(redisAddr, redisPassword, redisDB, 10*time.Minute)
		log.Printf("Redis: STANDALONE mode -> addr=%s db=%d", redisAddr, redisDB)
		uploadHandler = handler.NewUploadHandlerWithRedis(db, router, storages, rc)

	} else {
		// === Mode 3: No Redis (SQLite only) ===
		uploadHandler = handler.NewUploadHandler(db, router, storages)
	}

	// Register handlers
	downloadHandler := handler.NewDownloadHandler(db, router)
	fileHandler := handler.NewFileHandler(db, router, storages, rc)
	trashHandler := handler.NewTrashHandler(db, router, rc)
	settingsHandler := handler.NewSettingsHandler(db)
	adminHandler := handler.NewAdminHandler(db)

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

	// 分享 handler 创建：传入 JWT secret 用于签名下载 token（防盗链）
	shareHandler := handler.NewShareHandler(db, router, []byte(jwtSecret))

	// === 邮件服务初始化（用于账号注册和忘记密码） ===
	// SMTP_HOST/SMTP_PORT/SMTP_USER/SMTP_PASS 必填，SMTP_FROM/APP_BASE_URL 可选
	smtpHost := getEnv("SMTP_HOST", "smtp.qiye.aliyun.com")
	smtpPortStr := getEnv("SMTP_PORT", "465")
	smtpUser := getEnv("SMTP_USER", "")
	smtpPass := getEnv("SMTP_PASS", "")
	smtpFrom := getEnv("SMTP_FROM", "")
	appBaseURL := getEnv("APP_BASE_URL", "")
	// 默认 APP_BASE_URL 根据 domain 推导
	if appBaseURL == "" && domain != "" {
		appBaseURL = "https://" + domain
	}

	var mailer *email.SMTPMailer
	if smtpUser != "" && smtpPass != "" {
		smtpPort, err := strconv.Atoi(smtpPortStr)
		if err != nil || smtpPort <= 0 || smtpPort > 65535 {
			smtpPort = 465
		}
		mailer = email.NewSMTPMailer(smtpHost, smtpPort, smtpUser, smtpPass, smtpFrom)
		log.Printf("[Email] SMTP enabled: %s:%d user=%s base_url=%s", smtpHost, smtpPort, smtpUser, appBaseURL)
	} else {
		log.Printf("[Email] SMTP disabled (SMTP_USER/SMTP_PASS not set), registration & forgot-password unavailable")
	}

	// 确保初始管理员存在
	initialUser := os.Getenv("FILESYNC_INITIAL_USERNAME")
	initialPass := os.Getenv("FILESYNC_INITIAL_PASSWORD")
	if err := handler.EnsureInitialAdmin(db, initialUser, initialPass); err != nil {
		log.Fatalf("ensure initial admin: %v", err)
	}

	// 清理过期的激活 token 和重置验证码（启动时执行，避免表膨胀）
	if n, err := db.DeleteExpiredActivationTokens(); err == nil && n > 0 {
		log.Printf("[Auth] cleaned %d expired activation tokens", n)
	}
	if n, err := db.DeleteExpiredResetCodes(); err == nil && n > 0 {
		log.Printf("[Auth] cleaned %d expired reset codes", n)
	}

	// 清理过期回收站文件（30 天保留期，物理删除数据库记录 + 删除存储文件）
	// 在 router 初始化后执行，避免 storage 未就绪导致存储文件残留
	// 全局存储：删物理文件前检查引用计数，有其他记录引用时不删
	if expired, err := db.CleanupExpiredTrash(30); err == nil && len(expired) > 0 {
		var deletedCount int
		for _, f := range expired {
			refCount, refErr := db.CountByStoragePath(f.StoragePath)
			if refErr != nil {
				log.Printf("[Trash] cleanup: count refs %s error: %v (skip physical delete)", f.StoragePath, refErr)
				continue
			}
			if refCount > 0 {
				log.Printf("[Trash] cleanup: skip physical delete %s: %d refs exist", f.StoragePath, refCount)
				continue
			}
			if err := router.DeleteFile(f.StoragePath); err != nil {
				log.Printf("[Trash] cleanup: delete storage %s error: %v", f.StoragePath, err)
			}
			deletedCount++
		}
		log.Printf("[Trash] cleaned %d expired trash files (%d physical deleted)", len(expired), deletedCount)
	} else if err != nil {
		log.Printf("[Trash] cleanup expired error: %v", err)
	}

	// 登录速率限制器：rps=0.5（每 2 秒恢复 1 个 token），burst=10
	// 平衡正常用户并发登录需求与暴力破解防护：
	// - 1 分钟可恢复 30 个 token + 10 突发 ≈ 40 次/分钟/IP
	// - 多标签页/多设备同时登录不再被误杀
	// - 暴力破解 8 位密码仍需约 19 万年，防护有效
	loginLimiter := auth.NewLoginRateLimiter(0.5, 10)
	// 注册/重发激活/忘记密码速率限制器：5次突发，每2分钟恢复1次（rps=0.0083, burst=5）
	// 1小时可恢复约30次，对正常用户友好，对自动化攻击仍有阻拦
	registerLimiter := auth.NewLoginRateLimiter(0.0083, 5)
	forgotLimiter := auth.NewLoginRateLimiter(0.0083, 5)

	authHandler := handler.NewAuthHandler(db, jwtManager, mailer, appBaseURL)

	// 类型断言注入 OSS 转码缓存能力（S3Storage 实现 TranscodeCacheStore 接口）
	var tcs storage.TranscodeCacheStore
	if s3St != nil {
		if v, ok := s3St.(storage.TranscodeCacheStore); ok {
			tcs = v
			log.Printf("[Transcode] OSS cache store enabled: HLS transcode + presigned URL")
		}
	}
	if tcs == nil {
		log.Printf("[Transcode] OSS cache store disabled: video transcode falls back to local MP4")
	}

	// 预览 handler：注入 cacheStore 以支持 HLS 转码 + OSS 缓存
	previewHandler := handler.NewPreviewHandler(db, router, appBaseURL, tcs)

	// 启动转码优先级队列调度器（单 worker，替换原 transcodeGlobalSem 信号量）
	// 必须在入队前启动，否则任务永远不执行
	storage.StartTranscodeScheduler()

	// 启动预转码 worker（仅 cacheStore 可用时启动）
	// 服务器空闲时（转码队列为空）提前转码近30天 local 视频的 medium/low 画质到 OSS。
	// listRecentVideos 回调：查询近30天 local 视频文件（避免 storage 包依赖 store 包）
	storage.StartPreemptWorker(tcs, router.BasePath(), func() ([]storage.VideoFile, error) {
		files, err := db.ListRecentVideoFiles(30)
		if err != nil {
			return nil, err
		}
		result := make([]storage.VideoFile, 0, len(files))
		for _, f := range files {
			result = append(result, storage.VideoFile{
				FileID:   f.ID,
				SrcPath:  strings.TrimPrefix(f.StoragePath, storage.LocalPrefix),
				Filename: f.Filename,
			})
		}
		return result, nil
	})

	mux := http.NewServeMux()

	// 公开路由（无需认证）
	// /api/login 单独应用速率限制（5次/分钟/IP）
	loginHandler := loginLimiter.Middleware(http.HandlerFunc(authHandler.Login))
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		loginHandler.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/logout", authHandler.Logout)
	// 注册：3次/小时/IP
	mux.HandleFunc("/api/register", func(w http.ResponseWriter, r *http.Request) {
		registerLimiter.Middleware(http.HandlerFunc(authHandler.Register)).ServeHTTP(w, r)
	})
	// 激活：无速率限制（用户从邮件点击，IP 不固定）
	mux.HandleFunc("/api/activate", authHandler.Activate)
	// 重新发送激活邮件：3次/小时/IP
	mux.HandleFunc("/api/resend-activation", func(w http.ResponseWriter, r *http.Request) {
		registerLimiter.Middleware(http.HandlerFunc(authHandler.ResendActivation)).ServeHTTP(w, r)
	})
	// 忘记密码：3次/小时/IP
	mux.HandleFunc("/api/forgot-password", func(w http.ResponseWriter, r *http.Request) {
		forgotLimiter.Middleware(http.HandlerFunc(authHandler.ForgotPassword)).ServeHTTP(w, r)
	})
	// 重置密码：无速率限制（验证码本身有限流，且用户可能多次尝试）
	mux.HandleFunc("/api/reset-password", authHandler.ResetPassword)
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
	// 回收站（需认证）：列出/恢复/永久删除/清空
	mux.Handle("/api/trash", trashHandler)
	mux.Handle("/api/trash/", trashHandler)
	// 分享管理（需认证）：创建/列出/删除分享
	mux.Handle("/api/share", shareHandler)
	mux.Handle("/api/share/", shareHandler)
	// 分享公开访问（无需认证）：/api/s/ 在白名单中
	mux.Handle("/api/s/", shareHandler)
	// 用户配置同步（需认证）：跨浏览器同步分片大小和并发数
	mux.Handle("/api/settings", settingsHandler)
	// 存储用量查询（需认证）：返回当前用户已用空间，admin 可查指定用户
	mux.HandleFunc("/api/storage-usage", settingsHandler.StorageUsage)
	// 管理员后台 API（需认证 + admin 权限）：系统统计/用户管理/分享管理
	mux.Handle("/api/admin/", adminHandler)
	// 文件预览（需认证）：元数据/缩略图/原始内容流，权限同下载
	mux.Handle("/api/preview/", previewHandler)

	// 静态文件服务：前端 Web 控制台（/web/ 路径 + 根路径重定向）
	// 前端仅做页面展示，所有业务方法走 /api/* 后端（规则15）
	webDir := getEnv("WEB_DIR", "./web")
	if _, err := os.Stat(webDir); err == nil {
		mux.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.Dir(webDir))))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/web/intro.html", http.StatusFound)
				return
			}
			http.NotFound(w, r)
		})
		log.Printf("Web console: /web/ -> %s", webDir)
	} else {
		log.Printf("Web console: directory %s not found (set WEB_DIR to enable)", webDir)
	}

	// === JWT 认证中间件 ===
	// 白名单：登录/登出/注册/激活/忘记密码/重置密码/健康检查/分享公开访问/Web 静态资源/根路径
	whitelist := []string{
		"/api/login",
		"/api/logout",
		"/api/register",
		"/api/activate",
		"/api/resend-activation",
		"/api/forgot-password",
		"/api/reset-password",
		"/api/health",
		"/api/s/", // 分享公开访问（获取分享信息、下载）
		"/web/",
		"/",
	}

	// 用 JWT 中间件包装 mux
	jwtAuthed := jwtManager.Middleware(whitelist, mux)

	// === 防盗链中间件 ===
	// Referer 白名单：主域名 + www 子域 + 环境变量额外配置
	allowedDomains := []string{}
	if domain != "" {
		allowedDomains = append(allowedDomains, domain, "www."+domain)
	}
	if extra := getEnv("ALLOWED_REFERERS", ""); extra != "" {
		for _, d := range strings.Split(extra, ",") {
			d = strings.TrimSpace(d)
			if d != "" {
				allowedDomains = append(allowedDomains, d)
			}
		}
	}
	log.Printf("[Middleware] Referer whitelist: %v", allowedDomains)

	// 中间件链：SecurityHeaders -> MethodGuard -> PathGuard -> RefererCheck -> JWT -> mux
	// SecurityHeaders 最外层：所有响应（含 405/403/400）都带安全头
	// MethodGuard 次外层：拒绝 TRACE/CONNECT 方法，避免触发后续不必要检查
	// PathGuard：拒绝包含 .. 或 . 序列的恶意路径，纵深防御
	// RefererCheck：防盗链
	// JWT：认证白名单检查
	referered := middleware.RefererCheck(allowedDomains, jwtAuthed)
	pathGuarded := middleware.PathGuard(referered)
	guarded := middleware.MethodGuard(pathGuarded)
	finalHandler := middleware.SecurityHeaders(enableHTTPS, guarded)

	log.Printf("API endpoints:")
	log.Printf("  POST   /api/login               - Login (rate limited: 5/min)")
	log.Printf("  POST   /api/logout              - Logout")
	log.Printf("  POST   /api/register            - Register (rate limited: 3/hour, sends activation email)")
	log.Printf("  GET    /api/activate?token=xxx  - Activate account (public, redirect to /web/activate.html)")
	log.Printf("  POST   /api/resend-activation   - Resend activation email (rate limited: 3/hour)")
	log.Printf("  POST   /api/forgot-password     - Forgot password (rate limited: 3/hour, sends reset code)")
	log.Printf("  POST   /api/reset-password      - Reset password (verify code + set new password)")
	log.Printf("  GET    /api/me                  - Current user info (auth required)")
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
	log.Printf("  POST   /api/files/move-dir  - Move directory (auth, src, dst)")
	log.Printf("  POST   /api/files/batch-delete - Batch delete files (auth)")
	log.Printf("  GET    /api/trash           - List trash files (auth, ?all=true for admin)")
	log.Printf("  POST   /api/trash/{id}/restore - Restore trash file (auth)")
	log.Printf("  DELETE /api/trash/{id}      - Permanently delete trash file (auth)")
	log.Printf("  DELETE /api/trash           - Empty trash (auth, ?all=true for admin)")
	log.Printf("  GET    /api/share           - List my shares (auth)")
	log.Printf("  POST   /api/share           - Create share (auth, file_id|dir_prefix)")
	log.Printf("  DELETE /api/share/{id}      - Delete share (auth)")
	log.Printf("  POST   /api/share/save      - Save shared files to my files (auth, share_id, file_ids, target_dir)")
	log.Printf("  GET    /api/s/{id}          - Get share info (public)")
	log.Printf("  GET    /api/s/{id}/download - Download shared file/dir, ?path= for single file (public)")
	log.Printf("  GET    /api/s/{id}/list     - List shared dir contents, ?path= for subdir (public)")
	log.Printf("  POST   /api/s/{id}/batch    - Batch download shared files as ZIP (public, paths[])")
	log.Printf("  GET    /api/settings        - Get user settings (auth)")
	log.Printf("  POST   /api/settings        - Save user settings (auth)")
	log.Printf("  GET    /api/preview/{id}              - Preview metadata (auth)")
	log.Printf("  GET    /api/preview/{id}/thumb        - Image thumbnail (auth, ?size=small|medium|large)")
	log.Printf("  GET    /api/preview/{id}/poster       - Video poster (auth, ffmpeg)")
	log.Printf("  GET    /api/preview/{id}/content     - Stream file content (auth, supports Range)")
	log.Printf("  GET    /api/preview/{id}/transcode   - Video transcode stream (auth, ?quality=high|medium|low, supports Range)")
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
			Handler:      finalHandler,
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
			Handler:        finalHandler,
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
