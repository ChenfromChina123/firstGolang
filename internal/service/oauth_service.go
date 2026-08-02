package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"filesync/internal/auth"
	"filesync/internal/store"
)

// ============================================================
// OAuthService 编排 OAuth2 授权码流程 + SSO 免登
// 供 AuthSvc 的 /oauth/authorize 和 /oauth/token 端点使用
// ============================================================


// authCodeStoreDB 包装 *store.DB，实现 auth.AuthCodeStore 接口
// 解决 Save 方法签名与 SSOSessionStore.Save 冲突问题
type authCodeStoreDB struct{ *store.DB }

// Save 实现 auth.AuthCodeStore.Save，内部委托 DB.SaveAuthCode
func (a *authCodeStoreDB) Save(code, clientID, userID, redirectURI, scope, state, nonce, cc, ccm string, expiresAt time.Time) error {
	return a.DB.SaveAuthCode(code, clientID, userID, redirectURI, scope, state, nonce, cc, ccm, expiresAt)
}

// ssoStoreDB 包装 *store.DB，实现 auth.SSOSessionStore 接口
// 解决 Save 方法签名与 AuthCodeStore.Save 冲突问题
type ssoStoreDB struct{ *store.DB }

// Save 实现 auth.SSOSessionStore.Save，内部委托 DB.SaveSSOSession
func (s *ssoStoreDB) Save(sessionID, userID, ip, ua string, expiresAt time.Time) error {
	return s.DB.SaveSSOSession(sessionID, userID, ip, ua, expiresAt)
}

// OAuthService OAuth2 + SSO 服务
type OAuthService struct {
	db       *store.DB
	clients  auth.OAuthClientStore // 实际是 *store.DB（实现了该接口）
	codes    auth.AuthCodeStore    // 实际是 *store.DB
	tokens   *auth.RefreshTokenManager
	sso      *auth.SSOSessionManager
}

// NewOAuthService 创建 OAuth 服务
func NewOAuthService(
	db *store.DB,
	tokens *auth.RefreshTokenManager,
	sso *auth.SSOSessionManager,
) *OAuthService {
	return &OAuthService{
		db:      db,
		clients: db, // DB 实现了 OAuthClientStore
		codes:   &authCodeStoreDB{db}, // wrapper 实现 AuthCodeStore.Save
		tokens:  tokens,
		sso:     sso,
	}
}

// ---- /oauth/authorize ----

// AuthorizeRequest 授权请求参数（GET /oauth/authorize）
type AuthorizeRequest struct {
	ResponseType string `json:"response_type"` // 仅支持 "code"
	ClientID     string `json:"client_id"`
	RedirectURI  string `json:"redirect_uri"`
	Scope        string `json:"scope"`
	State        string `json:"state"`
	// PKCE
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"` // S256 | plain
	// OIDC
	Nonce string `json:"nonce"`
}

// AuthorizeResult 授权结果
// - RedirectURI: 302 重定向地址（已拼接 code + state）
// - NeedLogin:   true = 未登录，前端应跳统一登录页
type AuthorizeResult struct {
	RedirectURI string
	NeedLogin   bool
}

// Authorize 处理授权请求
// 已登录（SSO Cookie 或 Bearer Token）→ 生成授权码并重定向
// 未登录 → 返回 NeedLogin=true，前端跳登录页
func (svc *OAuthService) Authorize(w http.ResponseWriter, r *http.Request, req AuthorizeRequest) (*AuthorizeResult, error) {
	// 1. 基础校验
	if req.ResponseType != "code" {
		return nil, errors.New("unsupported response_type, only 'code' supported")
	}
	if req.ClientID == "" || req.RedirectURI == "" {
		return nil, errors.New("client_id and redirect_uri required")
	}
	// 2. 校验客户端 + 回调 URI
	if err := auth.ValidateClientRedirectURI(svc.clients, req.ClientID, req.RedirectURI); err != nil {
		return nil, err
	}
	// 3. 校验 scope（获取允许的 scope 列表用于过滤）
	_, _, _, allowedScopes, _, err := svc.clients.GetByID(req.ClientID)
	if err != nil {
		return nil, err
	}
	grantedScope, err := auth.ValidateScope(allowedScopes, req.Scope)
	if err != nil {
		return nil, err
	}
	// 4. 尝试获取已登录用户（Bearer Token 优先，然后 SSO Cookie）
	var userID, username, role string
	accessTok, _ := svc.tokens.ReadTokensFromRequest(r)
	if accessTok != "" {
		if claims, err := svc.tokens.ParseAccessToken(accessTok); err == nil {
			userID = claims.UserID
			username = claims.Username
			role = claims.Role
		}
	}
	if userID == "" && svc.sso != nil {
		userID = svc.sso.GetSessionUser(r)
		// 从 SSO 获取用户信息
		if userID != "" {
			if u, e := svc.db.GetUserByID(userID); e == nil {
				username = u.Username
				role = u.Role
				// SSO 登录成功 → 补签双 Token（刷新会话）
				ip := ClientIP(r)
				ua := UserAgent(r)
				_, _, _, _ = svc.tokens.IssueTokenPair(
					w, userID, username, role, req.ClientID, grantedScope, ip, ua,
				)
			} else {
				userID = ""
			}
		}
	}
	// 5. 未登录 → 返回 NeedLogin
	if userID == "" {
		return &AuthorizeResult{NeedLogin: true}, nil
	}
	// 6. 已登录 → 生成授权码 + 重定向
	code, expiresAt := auth.GenerateAuthCode(req.ClientID, userID, req.RedirectURI, grantedScope, req.State)
	err = svc.codes.Save(
		code, req.ClientID, userID, req.RedirectURI, grantedScope,
		req.State, req.Nonce, req.CodeChallenge, req.CodeChallengeMethod, expiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("save auth code: %w", err)
	}
	// 拼接回调 URI
	cb, err := url.Parse(req.RedirectURI)
	if err != nil {
		return nil, errors.New("invalid redirect_uri")
	}
	q := cb.Query()
	q.Set("code", code)
	if req.State != "" {
		q.Set("state", req.State)
	}
	cb.RawQuery = q.Encode()
	return &AuthorizeResult{RedirectURI: cb.String()}, nil
}

// ---- /oauth/token ----

// TokenRequest 令牌请求参数（POST /oauth/token）
type TokenRequest struct {
	GrantType    string `json:"grant_type"` // authorization_code | refresh_token
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`     // 公开客户端需传
	ClientSecret string `json:"client_secret"` // 非公开客户端需传
	// Refresh Token 模式
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	// PKCE
	CodeVerifier string `json:"code_verifier"`
}

// TokenResponse 令牌响应（RFC 6749 格式）
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"` // OIDC（预留）
}

// Token 处理令牌请求
func (svc *OAuthService) Token(w http.ResponseWriter, r *http.Request, req TokenRequest) (*TokenResponse, error) {
	switch req.GrantType {
	case "authorization_code":
		return svc.tokenFromCode(w, r, req)
	case "refresh_token":
		return svc.tokenFromRefresh(w, r, req)
	default:
		return nil, errors.New("unsupported grant_type, use 'authorization_code' or 'refresh_token'")
	}
}

// tokenFromCode 授权码换 Token
func (svc *OAuthService) tokenFromCode(w http.ResponseWriter, r *http.Request, req TokenRequest) (*TokenResponse, error) {
	if req.Code == "" || req.RedirectURI == "" {
		return nil, errors.New("code and redirect_uri required")
	}
	// 1. 校验客户端密钥（仅非公开客户端）
	if req.ClientID == "" {
		return nil, errors.New("client_id required")
	}
	if err := auth.ValidateClientSecret(svc.clients, req.ClientID, req.ClientSecret); err != nil {
		return nil, err
	}
	// 2. 校验授权码（一次性消费）
	userID, grantedScope, _, err := auth.ValidateAuthCode(svc.codes, req.Code, req.ClientID, req.RedirectURI)
	if err != nil {
		return nil, err
	}
	// 3. 查用户信息
	u, err := svc.db.GetUserByID(userID)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}
	// 4. 签发双 Token
	ip := ClientIP(r)
	ua := UserAgent(r)
	accessTok, _, expiresIn, err := svc.tokens.IssueTokenPair(
		w, u.ID, u.Username, u.Role, req.ClientID, grantedScope, ip, ua,
	)
	if err != nil {
		return nil, err
	}
	return &TokenResponse{
		AccessToken: accessTok,
		TokenType:   "Bearer",
		ExpiresIn:   expiresIn,
		Scope:       grantedScope,
	}, nil
}

// tokenFromRefresh Refresh Token 换 Token
func (svc *OAuthService) tokenFromRefresh(w http.ResponseWriter, r *http.Request, req TokenRequest) (*TokenResponse, error) {
	// 校验客户端
	if req.ClientID == "" {
		req.ClientID = "filesync-web"
	}
	if err := auth.ValidateClientSecret(svc.clients, req.ClientID, req.ClientSecret); err != nil {
		return nil, err
	}
	ip := ClientIP(r)
	ua := UserAgent(r)
	refreshTok := req.RefreshToken
	if refreshTok == "" {
		_, refreshTok = svc.tokens.ReadTokensFromRequest(r)
	}
	accessTok, _, expiresIn, username, role, err := svc.tokens.RotateTokens(
		w, refreshTok, req.ClientID, ip, ua,
		func(uid string) (string, string, error) {
			u, e := svc.db.GetUserByID(uid)
			if e != nil {
				return "", "", e
			}
			return u.Username, u.Role, nil
		},
	)
	if err != nil {
		return nil, err
	}
	_ = username
	_ = role
	return &TokenResponse{
		AccessToken: accessTok,
		TokenType:   "Bearer",
		ExpiresIn:   expiresIn,
	}, nil
}

// ---- SSO 登录页辅助 ----

// BuildLoginRedirect 构造统一登录页跳转地址（未登录时使用）
// 登录成功后回跳到 originalURL（完整的 /oauth/authorize 地址）
func BuildLoginRedirect(loginBaseURL, originalURL string) string {
	base, err := url.Parse(loginBaseURL)
	if err != nil {
		return loginBaseURL
	}
	q := base.Query()
	q.Set("redirect", originalURL)
	base.RawQuery = q.Encode()
	return base.String()
}

// ---- 额外：获取当前用户（从 Access Token / SSO）----

// GetCurrentUser 从请求解析当前登录用户（ID + Username + Role）
// 未登录返回 nil
func (svc *OAuthService) GetCurrentUser(r *http.Request) (userID, username, role string) {
	accessTok, _ := svc.tokens.ReadTokensFromRequest(r)
	if accessTok != "" {
		if claims, err := svc.tokens.ParseAccessToken(accessTok); err == nil {
			return claims.UserID, claims.Username, claims.Role
		}
	}
	if svc.sso != nil {
		if uid := svc.sso.GetSessionUser(r); uid != "" {
			if u, e := svc.db.GetUserByID(uid); e == nil {
				return u.ID, u.Username, u.Role
			}
		}
	}
	return "", "", ""
}
