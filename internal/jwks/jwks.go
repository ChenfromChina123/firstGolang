package jwks

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"filesync/internal/config"
)

// Claims 与 AuthSvc AccessClaims（uid/un/rl/scp）保持一致
type Claims struct {
	UserID   string `json:"uid"`
	Username string `json:"un"`
	Role     string `json:"rl"`
	Scope    string `json:"scp,omitempty"`
	Azp      string `json:"azp,omitempty"`
	jwt.RegisteredClaims
}

type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDoc struct {
	Keys []jwksKey `json:"keys"`
}

// Validator 拉取并缓存 AuthSvc JWKS 公钥
type Validator struct {
	authSvcURL string
	client     *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	order     []string
	fetchedAt time.Time
}

// New 创建 JWKS 校验器
func New(authSvcURL string) *Validator {
	if authSvcURL == "" {
		authSvcURL = config.AuthSvcURL()
	}
	return &Validator{
		authSvcURL: strings.TrimSuffix(authSvcURL, "/"),
		client:     &http.Client{Timeout: 5 * time.Second},
		keys:       map[string]*rsa.PublicKey{},
	}
}

func (v *Validator) refresh() error {
	v.mu.Lock()
	cached := time.Since(v.fetchedAt) < time.Hour && len(v.keys) > 0
	v.mu.Unlock()
	if cached {
		return nil
	}
	url := v.authSvcURL + "/.well-known/jwks.json"
	resp, err := v.client.Get(url)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var doc jwksDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("parse jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	var order []string
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.N == "" || k.E == "" {
			continue
		}
		pub, err := parseRSAKey(k.N, k.E)
		if err != nil {
			continue
		}
		kid := k.Kid
		if kid == "" {
			kid = "default"
		}
		if _, ok := keys[kid]; !ok {
			order = append(order, kid)
		}
		keys[kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("jwks contains no usable RSA keys")
	}
	v.mu.Lock()
	v.keys = keys
	v.order = order
	v.fetchedAt = time.Now()
	v.mu.Unlock()
	return nil
}

func parseRSAKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, err
	}
	if len(eBytes) == 0 {
		return nil, errors.New("empty exponent")
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

// Verify 校验 AuthSvc Access Token（RS256 + JWKS）
func (v *Validator) Verify(tokenString string) (*Claims, error) {
	if tokenString == "" {
		return nil, errors.New("missing access token")
	}
	if err := v.refresh(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	keys := make(map[string]*rsa.PublicKey, len(v.keys))
	for k, pub := range v.keys {
		keys[k] = pub
	}
	order := append([]string{}, v.order...)
	v.mu.Unlock()

	parse := func(key *rsa.PublicKey) (*Claims, error) {
		tok, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return key, nil
		})
		if err != nil {
			return nil, err
		}
		claims, ok := tok.Claims.(*Claims)
		if !ok || !tok.Valid || claims.UserID == "" {
			return nil, errors.New("invalid access token claims")
		}
		return claims, nil
	}

	parts := strings.SplitN(tokenString, ".", 3)
	if len(parts) == 3 {
		if hdr, err := base64.RawURLEncoding.DecodeString(parts[0]); err == nil {
			var h struct {
				Kid string `json:"kid"`
			}
			if json.Unmarshal(hdr, &h) == nil && h.Kid != "" {
				if key, ok := keys[h.Kid]; ok {
					if claims, err := parse(key); err == nil {
						return claims, nil
					}
				}
			}
		}
	}
	var lastErr error
	for _, kid := range order {
		if claims, err := parse(keys[kid]); err == nil {
			return claims, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no jwks key available")
	}
	return nil, lastErr
}
