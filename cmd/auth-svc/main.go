package main

import (
	"crypto/rsa"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"filesync/internal/auth"
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
	port := getEnvInt("PORT", 8081)
	dataDir := getEnv("DATA_DIR", "./data_auth")
	mysqlDSN := getEnv("MYSQL_DSN", "")
	accessTTL := time.Duration(getEnvInt("JWT_ACCESS_TTL", 900)) * time.Second
	refreshTTL := time.Duration(getEnvInt("JWT_REFRESH_TTL", 604800)) * time.Second
	ssoTTL := time.Duration(getEnvInt("SSO_SESSION_TTL", 86400)) * time.Second
	cookieDomain := getEnv("COOKIE_DOMAIN", "")
	cookieSecure := getEnvBool("COOKIE_SECURE", false)
	bootstrapClients := getEnv("OAUTH_BOOTSTRAP_CLIENTS", "")
	loginPage := getEnv("LOGIN_PAGE_URL", "/web/login.html")
	baseURL := getEnv("BASE_URL", fmt.Sprintf("http://localhost:%d", port))
	// SMTP 配置
	smtpHost := getEnv("SMTP_HOST", "")
	smtpPort := getEnvInt("SMTP_PORT", 587)
	smtpUser := getEnv("SMTP_USER", "")
	smtpPass := getEnv("SMTP_PASS", "")
	smtpFrom := getEnv("SMTP_FROM", "")
	// 默认管理员
	adminUser := getEnv("ADMIN_USERNAME", "")
	adminPass := getEnv("ADMIN_PASSWORD", "")

	// 确保数据目录
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	// --- 1. 初始化数据库 ---
	var db *store.DB
	var err error
	if mysqlDSN != "" {
		log.Printf("[Init] 使用 MySQL: %s", maskDSN(mysqlDSN))
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
	rsaPath := filepath.Join(dataDir, "jwt_key.pem")
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
	ssoMgr = auth.NewSSOSessionManager(ssoStoreAdapter{db: db}, cookieDomain, cookieSecure).WithTTL(ssoTTL)

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
	mux.HandleFunc("/auth/login", method("POST", rateLimit(authHandler.Login, 5, time.Minute)))
	mux.HandleFunc("/auth/register", method("POST", rateLimit(authHandler.Register, 3, time.Hour)))
	mux.HandleFunc("/auth/activate", method("GET", authHandler.Activate))
	mux.HandleFunc("/auth/password/forgot", method("POST", rateLimit(authHandler.ForgotPassword, 5, time.Minute)))
	mux.HandleFunc("/auth/password/reset", method("POST", rateLimit(authHandler.ResetPassword, 5, time.Minute)))
	mux.HandleFunc("/oauth/authorize", method("GET", authHandler.Authorize))
	mux.HandleFunc("/oauth/token", method("POST", rateLimit(authHandler.Token, 30, time.Minute)))

	// 带认证的端点
	jwtAuth := middleware.NewJWTAuthMiddleware(tokens)
	mux.HandleFunc("/auth/me", method("GET", jwtAuth.Wrap(authHandler.Me)))
	mux.HandleFunc("/auth/logout", method("POST", jwtAuth.WrapOptional(authHandler.Logout)))
	mux.HandleFunc("/auth/token/refresh", method("POST", authHandler.RefreshToken))
	mux.HandleFunc("/auth/admin/users", method("GET", jwtAuth.Wrap(authHandler.ListUsers)))

	// --- 9. 全局中间件包装：CORS + 安全头 + 日志 ---
	var rootHandler http.Handler = mux
	rootHandler = corsWrap(rootHandler)
	rootHandler = middleware.SecurityHeaders(cookieSecure, rootHandler)
	rootHandler = logMiddleware(rootHandler)

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
func rsaPubKey(priv *rsa.PrivateKey) *rsa.PublicKey {
	if priv == nil {
		return nil
	}
	return &priv.PublicKey
}

// method 限制 HTTP 方法
func method(m string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "OPTIONS" {
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

// logMiddleware 简易请求日志
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &logResponseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(lrw, r)
		log.Printf("[HTTP] %s %s -> %d (%s)  UA=%s",
			r.Method, r.URL.Path, lrw.status, time.Since(start), r.UserAgent())
	})
}

type logResponseWriter struct {
	http.ResponseWriter
	status int
}
func (l *logResponseWriter) WriteHeader(s int) { l.status = s; l.ResponseWriter.WriteHeader(s) }

// ssoStoreAdapter 适配 *store.DB，提供 Save 方法名（如果 auth.SSOSessionStore 接口仍是旧 Save 名）
// 和 SaveSession 方法名（如果已升级到新名），两种命名都满足。
type ssoStoreAdapter struct{ db *store.DB }

func (a ssoStoreAdapter) Save(sessionID, userID, ip, ua string, expiresAt time.Time) error {
	return a.db.SaveSession(sessionID, userID, ip, ua, expiresAt)
}
func (a ssoStoreAdapter) SaveSession(sessionID, userID, ip, ua string, expiresAt time.Time) error {
	return a.db.SaveSession(sessionID, userID, ip, ua, expiresAt)
}
func (a ssoStoreAdapter) GetBySessionID(sessionID string) (string, error) {
	return a.db.GetBySessionID(sessionID)
}
func (a ssoStoreAdapter) Delete(sessionID string) error { return a.db.Delete(sessionID) }
func (a ssoStoreAdapter) DeleteByUser(userID string) error { return a.db.DeleteByUser(userID) }

// corsWrap 内联 CORS 处理（因 middleware 包未提供类型化 CORS）
func corsWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" { origin = "*" }
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

// 避免未使用的 flag 包报错
var _ = flag.CommandLine
