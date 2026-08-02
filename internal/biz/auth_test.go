package biz

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"filesync/internal/jwks"
)

// newTestValidator 生成 RSA 密钥并起一个假 JWKS 服务，返回校验器、kid 与私钥
func newTestValidator(t *testing.T) (*jwks.Validator, string, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kid := "test-kid"
	doc := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return NewValidator(srv.URL), kid, key
}

func signTestToken(t *testing.T, key *rsa.PrivateKey, kid, userID string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"uid": userID, "un": "tester", "rl": "customer", "scp": "read",
		"exp": time.Now().Add(time.Hour).Unix(), "iss": "filesync-auth",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJWKSVerifyValidToken(t *testing.T) {
	validator, kid, key := newTestValidator(t)
	tok := signTestToken(t, key, kid, "user-1")
	ac, err := VerifyBearer(validator, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ac.UserID != "user-1" || ac.Role != "customer" {
		t.Fatalf("claims mismatch: %+v", ac)
	}
}

func TestJWKSRejectsBadToken(t *testing.T) {
	validator, kid, _ := newTestValidator(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	tok := signTestToken(t, other, kid, "user-1")
	if _, err := VerifyBearer(validator, tok); err == nil {
		t.Fatal("expected verify failure for wrong key")
	}
	if _, err := VerifyBearer(validator, ""); err == nil {
		t.Fatal("expected failure for empty token")
	}
}

func TestAuthMiddlewareRejectsSpoofedHeader(t *testing.T) {
	validator, kid, key := newTestValidator(t)
	oldMode := os.Getenv("AUTH_MODE")
	_ = os.Setenv("AUTH_MODE", "jwt")
	defer os.Setenv("AUTH_MODE", oldMode)

	// 无 Token 但伪造 X-Auth-User-ID → 401
	req := httptest.NewRequest(http.MethodGet, "/api/rbac/menus", nil)
	req.Header.Set("X-Auth-User-ID", "admin")
	req.Header.Set("X-Auth-Role", "admin")
	rec := httptest.NewRecorder()
	AuthMiddleware(validator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("spoofed header without token: want 401 got %d", rec.Code)
	}

	// 有效 Bearer → 放行并注入身份
	req2 := httptest.NewRequest(http.MethodGet, "/api/rbac/menus", nil)
	req2.Header.Set("Authorization", "Bearer "+signTestToken(t, key, kid, "user-1"))
	rec2 := httptest.NewRecorder()
	var injected authCtx
	AuthMiddleware(validator)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac, _ := AuthFromContext(r.Context())
		injected = ac
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || injected.UserID != "user-1" {
		t.Fatalf("valid bearer: code=%d injected=%+v", rec2.Code, injected)
	}
}
