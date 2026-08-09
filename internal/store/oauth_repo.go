package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"filesync/internal/auth"

	"golang.org/x/crypto/bcrypt"
)

// ============================================================
// OAuth2 仓储实现（SQLite + MySQL 双兼容）
// 实现接口：
//   - auth.OAuthClientStore (GetByID / VerifySecret)
//   - auth.AuthCodeStore (SaveAuthCode / GetAndMarkUsedAuthCode)   ← 方法重命名，避免 Save 冲突
//   - auth.RefreshSessionStore
//   - auth.SSOSessionStore (SaveSSOSession / GetBySessionID / Delete / DeleteByUser) ← 方法重命名
//   - auth.BlacklistStore (AddToBlacklist / IsBlacklisted)
// ============================================================

// ---------------- OAuth 表迁移 ----------------

// EnsureOAuthClientsTables 确保 oauth_* + refresh_sessions + sso_sessions 表存在
func (db *DB) EnsureOAuthClientsTables() error {
	switch db.dialect {
	case "mysql":
		return db.execOneByOne([]string{
			`CREATE TABLE IF NOT EXISTS oauth_clients (
				client_id VARCHAR(64) PRIMARY KEY,
				client_secret_hash VARCHAR(255) NOT NULL,
				name VARCHAR(128) NOT NULL,
				redirect_uris TEXT NOT NULL,
				allowed_scopes VARCHAR(512) NOT NULL DEFAULT 'filesync:read filesync:write',
				owner_username VARCHAR(64) NOT NULL DEFAULT '',
				is_public TINYINT(1) NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS oauth_codes (
				code VARCHAR(64) PRIMARY KEY,
				client_id VARCHAR(64) NOT NULL,
				user_id VARCHAR(64) NOT NULL,
				redirect_uri VARCHAR(512) NOT NULL,
				scope VARCHAR(255) NOT NULL DEFAULT '',
				state VARCHAR(128),
				nonce VARCHAR(128),
				code_challenge VARCHAR(255),
				code_challenge_method VARCHAR(16),
				expires_at DATETIME NOT NULL,
				used TINYINT(1) NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				INDEX idx_oc_user (user_id),
				INDEX idx_oc_client (client_id),
				INDEX idx_oc_expires (expires_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS refresh_sessions (
				session_id VARCHAR(64) PRIMARY KEY,
				user_id VARCHAR(64) NOT NULL,
				client_id VARCHAR(64) NOT NULL,
				jti VARCHAR(64) NOT NULL UNIQUE,
				scope VARCHAR(255) NOT NULL DEFAULT '',
				ip VARCHAR(64),
				user_agent VARCHAR(512),
				expires_at DATETIME NOT NULL,
				revoked_at DATETIME NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				INDEX idx_rs_user (user_id),
				INDEX idx_rs_client (client_id),
				INDEX idx_rs_expires (expires_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`CREATE TABLE IF NOT EXISTS sso_sessions (
				session_id VARCHAR(64) PRIMARY KEY,
				user_id VARCHAR(64) NOT NULL,
				ip VARCHAR(64),
				user_agent VARCHAR(512),
				expires_at DATETIME NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				INDEX idx_sso_user (user_id),
				INDEX idx_sso_expires (expires_at)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		})
	default: // sqlite
		return db.execOneByOne([]string{
			`CREATE TABLE IF NOT EXISTS oauth_clients (
				client_id TEXT PRIMARY KEY,
				client_secret_hash TEXT NOT NULL,
				name TEXT NOT NULL,
				redirect_uris TEXT NOT NULL,
				allowed_scopes TEXT NOT NULL DEFAULT 'filesync:read filesync:write',
				owner_username TEXT NOT NULL DEFAULT '',
				is_public INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS oauth_codes (
				code TEXT PRIMARY KEY,
				client_id TEXT NOT NULL,
				user_id TEXT NOT NULL,
				redirect_uri TEXT NOT NULL,
				scope TEXT NOT NULL DEFAULT '',
				state TEXT,
				nonce TEXT,
				code_challenge TEXT,
				code_challenge_method TEXT,
				expires_at TEXT NOT NULL,
				used INTEGER NOT NULL DEFAULT 0,
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_oc_user ON oauth_codes(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_oc_client ON oauth_codes(client_id)`,
			`CREATE INDEX IF NOT EXISTS idx_oc_expires ON oauth_codes(expires_at)`,
			`CREATE TABLE IF NOT EXISTS refresh_sessions (
				session_id TEXT PRIMARY KEY,
				user_id TEXT NOT NULL,
				client_id TEXT NOT NULL,
				jti TEXT NOT NULL UNIQUE,
				scope TEXT NOT NULL DEFAULT '',
				ip TEXT,
				user_agent TEXT,
				expires_at TEXT NOT NULL,
				revoked_at TEXT,
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_rs_user ON refresh_sessions(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_rs_jti ON refresh_sessions(jti)`,
			`CREATE INDEX IF NOT EXISTS idx_rs_expires ON refresh_sessions(expires_at)`,
			`CREATE TABLE IF NOT EXISTS sso_sessions (
				session_id TEXT PRIMARY KEY,
				user_id TEXT NOT NULL,
				ip TEXT,
				user_agent TEXT,
				expires_at TEXT NOT NULL,
				created_at TEXT NOT NULL
			)`,
			`CREATE INDEX IF NOT EXISTS idx_sso_user ON sso_sessions(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_sso_expires ON sso_sessions(expires_at)`,
		})
	}
}

// migrateOAuthClientsAddOwner 为已存在的 oauth_clients 表追加 owner_username 列（旧库升级路径）。
// 用于 client_credentials grant：client 绑定一个 FileSync 用户作为令牌身份。
func (db *DB) migrateOAuthClientsAddOwner() error {
	// oauth_clients 表可能由 EnsureOAuthClientsTables() 在 migrate() 之后创建
	// （新表 CREATE 语句已含 owner_username 列）。此处表不存在则跳过。
	var exists int
	var err error
	if db.dialect == "mysql" {
		err = db.conn.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'oauth_clients'`).Scan(&exists)
	} else {
		err = db.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='oauth_clients'`).Scan(&exists)
	}
	if err != nil || exists == 0 {
		return nil // 表不存在，等待后续创建（新表已含该列）
	}

	var columns []string
	switch db.dialect {
	case "mysql":
		rows, err := db.conn.Query(
			`SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMNS
			 WHERE TABLE_NAME = 'oauth_clients' AND TABLE_SCHEMA = DATABASE()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(c))
		}
		rows.Close()
	default:
		rows, err := db.conn.Query(`PRAGMA table_info(oauth_clients)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			columns = append(columns, strings.ToLower(name))
		}
		rows.Close()
	}
	for _, c := range columns {
		if c == "owner_username" {
			return nil // 已存在
		}
	}
	var stmt string
	if db.dialect == "mysql" {
		stmt = `ALTER TABLE oauth_clients ADD COLUMN owner_username VARCHAR(64) NOT NULL DEFAULT ''`
	} else {
		stmt = `ALTER TABLE oauth_clients ADD COLUMN owner_username TEXT NOT NULL DEFAULT ''`
	}
	if _, err := db.conn.Exec(stmt); err != nil {
		return fmt.Errorf("add oauth_clients.owner_username column: %w", err)
	}
	return nil
}

// execOneByOne 逐条执行 SQL（MySQL 不支持单 Exec 多语句）
func (db *DB) execOneByOne(stmts []string) error {
	for _, s := range stmts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := db.conn.Exec(s); err != nil {
			return fmt.Errorf("exec stmt: %w\nSQL: %s", err, s[:min(len(s), 200)])
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------- 初始客户端导入 ----------------

// BootstrapOAuthClients 从环境变量格式批量插入预注册客户端
func (db *DB) BootstrapOAuthClients(clientsStr string) error {
	if clientsStr == "" {
		return nil
	}
	entries := strings.Split(clientsStr, ";;")
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, "|")
		if len(parts) < 5 {
			continue
		}
		clientID := strings.TrimSpace(parts[0])
		secretPlain := strings.TrimSpace(parts[1])
		name := strings.TrimSpace(parts[2])
		urisRaw := strings.TrimSpace(parts[3])
		scope := strings.TrimSpace(parts[4])
		isPublic := false
		owner := ""
		if len(parts) >= 6 {
			t := strings.TrimSpace(parts[5])
			isPublic = t == "1" || strings.EqualFold(t, "true")
		}
		if len(parts) >= 7 {
			owner = strings.TrimSpace(parts[6])
		}
		if clientID == "" || secretPlain == "" {
			continue
		}
		var existing string
		err := db.conn.QueryRow(`SELECT client_id FROM oauth_clients WHERE client_id = ?`, clientID).Scan(&existing)
		if err == nil {
			continue // 已存在
		}
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("check existing client %s: %w", clientID, err)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(secretPlain), 12)
		if err != nil {
			return fmt.Errorf("hash client secret: %w", err)
		}
		uris := splitAndTrim(urisRaw, ",")
		urisJSON, _ := json.Marshal(uris)
		nowStr := formatDBTime(time.Now(), db.dialect)
		_, err = db.conn.Exec(
			`INSERT INTO oauth_clients (client_id, client_secret_hash, name, redirect_uris, allowed_scopes, owner_username, is_public, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			clientID, string(hash), name, string(urisJSON), scope, owner, boolToInt(isPublic), nowStr,
		)
		if err != nil {
			return fmt.Errorf("insert oauth client %s: %w", clientID, err)
		}
	}
	return nil
}

// ---------------- OAuthClientStore 接口实现 ----------------

// GetByID 实现 auth.OAuthClientStore.GetByID
func (db *DB) GetByID(clientID string) (secretHash string, name string, redirectURIs []string, allowedScopes []string, ownerUsername string, isPublic bool, err error) {
	var urisJSON, scopeStr string
	var ownerStr sql.NullString
	var isPubInt int
	err = db.conn.QueryRow(
		`SELECT client_secret_hash, name, redirect_uris, allowed_scopes, owner_username, is_public
		 FROM oauth_clients WHERE client_id = ?`, clientID,
	).Scan(&secretHash, &name, &urisJSON, &scopeStr, &ownerStr, &isPubInt)
	if err != nil {
		if err == sql.ErrNoRows {
			err = auth.ErrInvalidClient
		}
		return
	}
	isPublic = isPubInt != 0
	ownerUsername = ownerStr.String
	if urisJSON != "" {
		_ = json.Unmarshal([]byte(urisJSON), &redirectURIs)
	}
	allowedScopes = splitAndTrim(scopeStr, " ")
	return
}

// VerifySecret 实现 auth.OAuthClientStore.VerifySecret
func (db *DB) VerifySecret(secretHash, plainSecret string) bool {
	if secretHash == "" || plainSecret == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(secretHash), []byte(plainSecret)) == nil
}

// ---------------- AuthCodeStore 接口实现 ----------------

// SaveAuthCode 保存授权码（避免与 SSOSessionStore.Save 同名冲突）
// 对应 auth.AuthCodeStore.Save（注意：auth.AuthCodeStore 接口中方法名仍是 Save，
// 但我们通过以下方式适配：把 SaveAuthCode 作为内部方法，另提供 Save 包装以匹配接口签名）
func (db *DB) SaveAuthCode(code, clientID, userID, redirectURI, scope, state, nonce, codeChallenge, codeChallengeMethod string, expiresAt time.Time) error {
	nowStr := formatDBTime(time.Now(), db.dialect)
	expStr := formatDBTime(expiresAt, db.dialect)
	_, err := db.conn.Exec(
		`INSERT INTO oauth_codes (code, client_id, user_id, redirect_uri, scope, state, nonce, code_challenge, code_challenge_method, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		code, clientID, userID, redirectURI, scope, state, nonce, codeChallenge, codeChallengeMethod, expStr, nowStr,
	)
	return err
}

// GetAndMarkUsed 实现 auth.AuthCodeStore.GetAndMarkUsed
func (db *DB) GetAndMarkUsed(code, clientID, redirectURI string) (userID, scope, state, challenge, method, storedRedirect string, err error) {
	var usedInt int
	var expiresAt any
	var codeChallenge sql.NullString
	var codeMethod sql.NullString
	var storedRedirectURI sql.NullString
	var stateStr sql.NullString
	err = db.conn.QueryRow(
		`SELECT user_id, scope, state, redirect_uri, code_challenge, code_challenge_method, expires_at, used
		 FROM oauth_codes WHERE code = ? AND client_id = ?`,
		code, clientID,
	).Scan(&userID, &scope, &stateStr, &storedRedirectURI, &codeChallenge, &codeMethod, &expiresAt, &usedInt)
	if err != nil {
		if err == sql.ErrNoRows {
			err = auth.ErrInvalidClient
		}
		return
	}
	if usedInt != 0 {
		err = auth.ErrAuthCodeUsed
		return
	}
	tm := toTimeAny(expiresAt)
	if time.Now().After(tm) {
		err = auth.ErrAuthCodeExpired
		return
	}
	challenge = codeChallenge.String
	method = codeMethod.String
	storedRedirect = storedRedirectURI.String
	state = stateStr.String
	// 原子标记已使用
	res, uErr := db.conn.Exec(`UPDATE oauth_codes SET used = 1 WHERE code = ? AND used = 0`, code)
	if uErr != nil {
		err = uErr
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		err = auth.ErrAuthCodeUsed
		return
	}
	return
}

// ---------------- RefreshSessionStore 接口实现 ----------------

// CreateRefreshSession 实现 auth.RefreshSessionStore.CreateRefreshSession
func (db *DB) CreateRefreshSession(sessionID, userID, clientID, jti, scope, ip, ua string, expiresAt time.Time) error {
	nowStr := formatDBTime(time.Now(), db.dialect)
	expStr := formatDBTime(expiresAt, db.dialect)
	_, err := db.conn.Exec(
		`INSERT INTO refresh_sessions (session_id, user_id, client_id, jti, scope, ip, user_agent, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, userID, clientID, jti, scope, ip, ua, expStr, nowStr,
	)
	return err
}

// GetSessionByJTI 实现 auth.RefreshSessionStore.GetSessionByJTI
func (db *DB) GetSessionByJTI(jti string) (sessionID, userID, scope string, revoked bool, err error) {
	var revokedAt any
	var expiresAt any
	err = db.conn.QueryRow(
		`SELECT session_id, user_id, scope, revoked_at, expires_at FROM refresh_sessions WHERE jti = ?`, jti,
	).Scan(&sessionID, &userID, &scope, &revokedAt, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			err = errors.New("refresh session not found")
		}
		return
	}
	revoked = !isAnyNil(revokedAt)
	// 过期也视为 revoked
	expTm := toTimeAny(expiresAt)
	if time.Now().After(expTm) {
		revoked = true
	}
	return
}

// RotateSession 实现 auth.RefreshSessionStore.RotateSession
func (db *DB) RotateSession(oldJTI, newSessionID, newJTI string, newExpiresAt time.Time) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	nowStr := formatDBTime(time.Now(), db.dialect)
	newExpStr := formatDBTime(newExpiresAt, db.dialect)
	if _, err := tx.Exec(`UPDATE refresh_sessions SET revoked_at = ? WHERE jti = ?`, nowStr, oldJTI); err != nil {
		return err
	}
	var userID, clientID, scope, ip, ua string
	if err := tx.QueryRow(
		`SELECT user_id, client_id, scope, COALESCE(ip,''), COALESCE(user_agent,'') FROM refresh_sessions WHERE jti = ?`, oldJTI,
	).Scan(&userID, &clientID, &scope, &ip, &ua); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO refresh_sessions (session_id, user_id, client_id, jti, scope, ip, user_agent, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newSessionID, userID, clientID, newJTI, scope, ip, ua, newExpStr, nowStr,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeAllUserSessions 实现 auth.RefreshSessionStore.RevokeAllUserSessions
func (db *DB) RevokeAllUserSessions(userID string) error {
	nowStr := formatDBTime(time.Now(), db.dialect)
	switch db.dialect {
	case "mysql":
		_, err := db.conn.Exec(`UPDATE refresh_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, nowStr, userID)
		return err
	default:
		_, err := db.conn.Exec(`UPDATE refresh_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, nowStr, userID)
		return err
	}
}

// RevokeSessionByJTI 实现 auth.RefreshSessionStore.RevokeSessionByJTI
func (db *DB) RevokeSessionByJTI(jti string) error {
	nowStr := formatDBTime(time.Now(), db.dialect)
	_, err := db.conn.Exec(`UPDATE refresh_sessions SET revoked_at = ? WHERE jti = ? AND revoked_at IS NULL`, nowStr, jti)
	return err
}

// ---------------- SSOSessionStore 接口实现 ----------------

// SaveSSOSession 内部保存方法（避免与 AuthCodeStore.Save 冲突），再用 Save 包装
func (db *DB) SaveSSOSession(sessionID, userID, ip, ua string, expiresAt time.Time) error {
	nowStr := formatDBTime(time.Now(), db.dialect)
	expStr := formatDBTime(expiresAt, db.dialect)
	_, err := db.conn.Exec(
		`INSERT INTO sso_sessions (session_id, user_id, ip, user_agent, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, userID, ip, ua, expStr, nowStr,
	)
	return err
}

// SaveSession 实现 auth.SSOSessionStore.SaveSession
func (db *DB) SaveSession(sessionID, userID, ip, ua string, expiresAt time.Time) error {
	return db.SaveSSOSession(sessionID, userID, ip, ua, expiresAt)
}

// GetBySessionID 实现 auth.SSOSessionStore.GetBySessionID
func (db *DB) GetBySessionID(sessionID string) (string, error) {
	var userID string
	var expiresAt any
	err := db.conn.QueryRow(`SELECT user_id, expires_at FROM sso_sessions WHERE session_id = ?`, sessionID).Scan(&userID, &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", errors.New("sso session not found")
		}
		return "", err
	}
	expTm := toTimeAny(expiresAt)
	if time.Now().After(expTm) {
		return "", errors.New("sso session expired")
	}
	return userID, nil
}

// Delete 实现 auth.SSOSessionStore.Delete
func (db *DB) Delete(sessionID string) error {
	_, err := db.conn.Exec(`DELETE FROM sso_sessions WHERE session_id = ?`, sessionID)
	return err
}

// DeleteByUser 实现 auth.SSOSessionStore.DeleteByUser
func (db *DB) DeleteByUser(userID string) error {
	_, err := db.conn.Exec(`DELETE FROM sso_sessions WHERE user_id = ?`, userID)
	return err
}

// ---------------- BlacklistStore 接口实现 ----------------

// EnsureBlacklistTable 确保 token_blacklist 表存在
func (db *DB) EnsureBlacklistTable() error {
	switch db.dialect {
	case "mysql":
		_, err := db.conn.Exec(`CREATE TABLE IF NOT EXISTS token_blacklist (
			jti VARCHAR(128) PRIMARY KEY,
			expires_at DATETIME NOT NULL,
			INDEX idx_bl_expires (expires_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
		return err
	default:
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS token_blacklist (jti TEXT PRIMARY KEY, expires_at TEXT NOT NULL)`,
			`CREATE INDEX IF NOT EXISTS idx_bl_expires ON token_blacklist(expires_at)`,
		}
		return db.execOneByOne(stmts)
	}
}

// AddToBlacklist 实现 auth.BlacklistStore.AddToBlacklist
func (db *DB) AddToBlacklist(jti string, ttl time.Duration) error {
	if jti == "" {
		return nil
	}
	expStr := formatDBTime(time.Now().Add(ttl), db.dialect)
	_, err := db.conn.Exec(db.replaceIntoPrefix()+` INTO token_blacklist (jti, expires_at) VALUES (?, ?)`, jti, expStr)
	return err
}

// IsBlacklisted 实现 auth.BlacklistStore.IsBlacklisted
func (db *DB) IsBlacklisted(jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}
	var expiresAt any
	err := db.conn.QueryRow(`SELECT expires_at FROM token_blacklist WHERE jti = ?`, jti).Scan(&expiresAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if time.Now().After(toTimeAny(expiresAt)) {
		_, _ = db.conn.Exec(`DELETE FROM token_blacklist WHERE jti = ?`, jti)
		return false, nil
	}
	return true, nil
}

// ---------------- 工具函数 ----------------

// boolToInt 转换 bool → int（SQL 兼容）
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// splitAndTrim 按分隔符切分并去空格、去空串
func splitAndTrim(s, sep string) []string {
	if s == "" {
		return nil
	}
	var res []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || string(s[i]) == sep {
			part := strings.TrimSpace(s[start:i])
			if part != "" {
				res = append(res, part)
			}
			start = i + 1
		}
	}
	return res
}

// formatDBTime 把 time.Time 格式化为对应方言的存储形式
// MySQL: DATETIME → "2006-01-02 15:04:05"
// SQLite: TEXT ISO8601 → RFC3339
func formatDBTime(t time.Time, dialect string) string {
	if dialect == "mysql" {
		return t.Format("2006-01-02 15:04:05")
	}
	return t.Format(time.RFC3339)
}

// toTimeAny 把任意类型（string / time.Time / []byte 等）转为 time.Time
func toTimeAny(v any) time.Time {
	if v == nil {
		return time.Time{}
	}
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
			if parsed, err := time.ParseInLocation(layout, t, time.Local); err == nil {
				return parsed
			}
		}
		return time.Time{}
	case []byte:
		return toTimeAny(string(t))
	default:
		return time.Time{}
	}
}

// isAnyNil 检查 interface{} 是否为 nil（NULL 值）
func isAnyNil(v any) bool {
	if v == nil {
		return true
	}
	// sql NULL 扫描到 string 时是空串（当使用 sql.NullString 时 Valid=false 但我们这里用 any 接收）
	// 这里简单处理：如果是空字符串也当 nil（针对 SQLite TEXT NULL 扫描到空 string 的情况）
	if s, ok := v.(string); ok && s == "" {
		// 注意：如果 revoked_at 存了合法 DATETIME，就一定非空字符串，所以安全
		return true
	}
	return false
}
