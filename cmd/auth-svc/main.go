package main

import (
	"crypto/rsa"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filesync/internal/auth"
	"filesync/internal/cmdutil"
	"filesync/internal/email"
	"filesync/internal/handler"
	"filesync/internal/middleware"
	"filesync/internal/service"
	"filesync/internal/store"
)

// ============================================================
// AuthSvc 主入口（端口 8081 默认）
// 职责：统一身份认证 + OAuth2 授权 + SSO 会话 + JWKS 分发
// Phase 1.7
// ============================================================

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("========================================")
	log.Println(" FileSync Auth Service (统一身份认证微服务)")
	log.Println("========================================")

	// --- 读取环境变量 ---
	port := cmdutil.GetEnvInt("PORT", 8081)
	dataDir := cmdutil.GetEnv("DATA_DIR", "./data_auth")
	mysqlDSN := cmdutil.GetEnv("MYSQL_DSN", "")
	accessTTL := time.Duration(cmdutil.GetEnvInt("JWT_ACCESS_TTL", 900)) * time.Second
	refreshTTL := time.Duration(cmdutil.GetEnvInt("JWT_REFRESH_TTL", 604800)) * time.Second
	ssoTTL := time.Duration(cmdutil.GetEnvInt("SSO_SESSION_TTL", 86400)) * time.Second
	cookieDomain := cmdutil.GetEnv("COOKIE_DOMAIN", "")
	cookieSecure := cmdutil.GetEnvBool("COOKIE_SECURE", false)
	bootstrapClients := cmdutil.GetEnv("OAUTH_BOOTSTRAP_CLIENTS", "")
	loginPage := cmdutil.GetEnv("LOGIN_PAGE_URL", "/web/login.html")
	baseURL := cmdutil.GetEnv("BASE_URL", fmt.Sprintf("http://localhost:%d", port))
	// SMTP 配置
	smtpHost := cmdutil.GetEnv("SMTP_HOST", "")
	smtpPort := cmdutil.GetEnvInt("SMTP_PORT", 587)
	smtpUser := cmdutil.GetEnv("SMTP_USER", "")
	smtpPass := cmdutil.GetEnv("SMTP_PASS", "")
	smtpFrom := cmdutil.GetEnv("SMTP_FROM", "")
	// 默认管理员
	adminUser := cmdutil.GetEnv("ADMIN_USERNAME", "")
	adminPass := cmdutil.GetEnv("ADMIN_PASSWORD", "")

	// 确保数据目录
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// --- 1. 初始化数据库 ---
	var db *store.DB
	var err error
	if mysqlDSN != "" {
		log.Printf("[Init] 使用 MySQL: %s", cmdutil.MaskDSN(mysqlDSN))
		db, err = store.NewMySQL(mysqlDSN)
	} else {
		sqlitePath := filepath.Join(dataDir, "auth.db")
		log.Printf("[Init] 使用 SQLite: %s", sqlitePath)
		db, err = store.New(sqlitePath)
	}
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// --- 2. 确保 OAuth 相关表存在 + 导入初始客户端 ---
	if err := db.EnsureOAuthClientsTables(); err != nil {
		log.Fatalf("ensure oauth tables: %v", err)
	}
	if err := db.EnsureBlacklistTable(); err != nil {
		log.Fatalf("ensure blacklist table: %v", err)
	}
	if bootstrapClients != "" {
		if err := db.BootstrapOAuthClients(bootstrapClients); err != nil {
			log.Fatalf("bootstrap oauth clients: %v", err)
		}
		log.Println("[Init] OAuth 预注册客户端已导入")
	}
	// 确保初始管理员
	if err := handler.EnsureInitialAdmin(db, adminUser, adminPass); err != nil {
		log.Fatalf("ensure initial admin: %v", err)
	}

	// --- 3. 加载/生成 RSA 密钥对（JWT RS256 签名）---
	// 认证唯一化：与 server 单体共享密钥时配置 JWT_RSA_KEY_FILE（指向同一私钥文件），
	// 保证 server 签发的 Access Token 能被本服务的 JWKS 公钥验证（下游服务透传验签闭环）。
	rsaPath := filepath.Join(dataDir, "jwt_key.pem")
	if v := cmdutil.GetEnv("JWT_RSA_KEY_FILE", ""); v != "" {
		rsaPath = v
	}
	var rsaKeys *auth.RSAKeys
	rsaKeys, err = auth.LoadOrGenerateKeys(rsaPath)
	if err != nil {
		log.Fatalf("load or generate rsa keys: %v", err)
	}
	log.Printf("[Init] RSA 密钥对就绪: %s", rsaPath)
	privKey := rsaKeys.PrivateKey()
	if privKey == nil {
		log.Fatalf("RSA private key is nil")
	}

	// --- 4. 初始化双 Token 管理器 + SSO 管理器 ---
	tokens := auth.NewRefreshTokenManager(
		privKey, cookieDomain, cookieSecure,
		db, // RefreshSessionStore
		db, // BlacklistStore
	).WithTTL(accessTTL, refreshTTL)

	var ssoMgr *auth.SSOSessionManager
	ssoMgr = auth.NewSSOSessionManager(
		service.SSOStoreAdapter{DB: db}, cookieDomain, cookieSecure,
	).WithTTL(ssoTTL)

	// --- 5. 邮件服务（可选）---
	var mailer *email.SMTPMailer
	if smtpHost != "" && smtpUser != "" {
		mailer = email.NewSMTPMailer(smtpHost, smtpPort, smtpUser, smtpPass, smtpFrom)
		log.Printf("[Init] SMTP 邮件服务已启用: %s:%d", smtpHost, smtpPort)
	} else {
		log.Println("[Init] SMTP 未配置 - 注册/忘记密码功能不可用")
	}

	// --- 6. 业务服务层 ---
	authSvc := service.NewAuthService(
		db, tokens, ssoMgr, mailer, baseURL, rsaKeys, "filesync-web",
	)
	oauthSvc := service.NewOAuthService(db, tokens, ssoMgr)
	pubKey := rsaPubKey(privKey)
	jwksSvc := service.NewJWKSService(pubKey, "rsa-key-1")

	// --- 7. Handler ---
	authHandler := handler.NewAuthSvcHandler(authSvc, oauthSvc, jwksSvc, loginPage)

	// --- 8. 路由 + 中间件链 ---
	mux := http.NewServeMux()

	// 公开端点（无认证）
	mux.HandleFunc("/health", authHandler.Health)
	mux.HandleFunc("/.well-known/jwks.json", authHandler.JWKS)
	mux.HandleFunc("/.well-known/openid-configuration", authHandler.OpenIDConfig)
	mux.HandleFunc("/auth/login", cmdutil.Method("POST", rateLimit(authHandler.Login, 5, time.Minute)))
	mux.HandleFunc("/auth/register", cmdutil.Method("POST", rateLimit(authHandler.Register, 3, time.Hour)))
	mux.HandleFunc("/auth/activate", cmdutil.Method("GET", authHandler.Activate))
	mux.HandleFunc("/auth/resend-activation", cmdutil.Method("POST", rateLimit(authHandler.ResendActivation, 3, time.Hour)))
	mux.HandleFunc("/auth/password/forgot", cmdutil.Method("POST", rateLimit(authHandler.ForgotPassword, 5, time.Minute)))
	mux.HandleFunc("/auth/password/reset", cmdutil.Method("POST", rateLimit(authHandler.ResetPassword, 5, time.Minute)))
	mux.HandleFunc("/oauth/authorize", cmdutil.Method("GET", authHandler.Authorize))
	mux.HandleFunc("/oauth/token", cmdutil.Method("POST", rateLimit(authHandler.Token, 30, time.Minute)))

	// 带认证的端点
	jwtAuth := middleware.NewJWTAuthMiddleware(tokens)
	mux.HandleFunc("/auth/me", cmdutil.Method("GET", jwtAuth.Wrap(authHandler.Me)))
	mux.HandleFunc("/auth/logout", cmdutil.Method("POST", jwtAuth.WrapOptional(authHandler.Logout)))
	mux.HandleFunc("/auth/token/refresh", cmdutil.Method("POST", authHandler.RefreshToken))
	mux.HandleFunc("/auth/admin/users", cmdutil.Method("GET", jwtAuth.Wrap(authHandler.ListUsers)))

	// --- 9. 全局中间件包装：CORS + 安全头 + 日志 ---
	var rootHandler http.Handler = mux
	rootHandler = cmdutil.CorsWrap(rootHandler)
	rootHandler = middleware.SecurityHeaders(cookieSecure, rootHandler)
	rootHandler = cmdutil.LogMiddleware("HTTP", nil, rootHandler)

	// --- 10. 启动 ---
	addr := fmt.Sprintf(":%d", port)
	log.Printf("[AuthSvc] 监听地址: %s", addr)
	log.Printf("[AuthSvc] 健康检查: http://localhost%s/health", addr)
	log.Printf("[AuthSvc] JWKS 端点: http://localhost%s/.well-known/jwks.json", addr)
	log.Printf("[AuthSvc] OpenID 配置: http://localhost%s/.well-known/openid-configuration", addr)
	log.Printf("[AuthSvc] 授权端点: http://localhost%s/oauth/authorize", addr)
	log.Printf("[AuthSvc] Access TTL=%s, Refresh TTL=%s, SSO TTL=%s", accessTTL, refreshTTL, ssoTTL)

	if err := http.ListenAndServe(addr, rootHandler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ===== 辅助函数（公共实现见 internal/cmdutil） =====

func rsaPubKey(priv *rsa.PrivateKey) *rsa.PublicKey {
	if priv == nil {
		return nil
	}
	return &priv.PublicKey
}

// rateLimit 简易速率限制（单进程内存实现）
// n 次 / 窗口 每 IP
func rateLimit(next http.HandlerFunc, n int, window time.Duration) http.HandlerFunc {
	type bucketEntry struct {
		count int
		reset time.Time
	}
	var buckets sync.Map // map[string]*bucketEntry
	return func(w http.ResponseWriter, r *http.Request) {
		ip := service.ClientIP(r)
		now := time.Now()
		v, _ := buckets.LoadOrStore(ip, &bucketEntry{reset: now.Add(window)})
		e := v.(*bucketEntry)
		if now.After(e.reset) {
			e.count = 0
			e.reset = now.Add(window)
		}
		if e.count >= n {
			w.Header().Set("Retry-After", fmt.Sprintf("%d", int(time.Until(e.reset).Seconds())))
			http.Error(w, `{"error":"rate_limit_exceeded","message":"too many requests"}`, http.StatusTooManyRequests)
			return
		}
		e.count++
		next(w, r)
	}
}

// 避免未使用的 flag 包报错
var _ = flag.CommandLine
