package service

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/big"

	"filesync/internal/model"
)

// ============================================================
// JWKSService 生成 JWKS（JSON Web Key Set）端点响应
// 供 API 网关、FileSvc 等下游服务本地校验 JWT 签名
// ============================================================

// JWKSService JWKS 服务
type JWKSService struct {
	pubKey *rsa.PublicKey
	kid    string // 密钥版本标识（如 "rsa-key-1"）
}

// NewJWKSService 创建 JWKS 服务
func NewJWKSService(pubKey *rsa.PublicKey, kid string) *JWKSService {
	return &JWKSService{pubKey: pubKey, kid: kid}
}

// GetJWKS 返回 JWKS 响应（含一个公钥）
func (s *JWKSService) GetJWKS() *model.JWKSResponse {
	if s.pubKey == nil {
		return &model.JWKSResponse{Keys: []model.JWK{}}
	}
	jwk := model.JWK{
		Kty: "RSA",
		Use: "sig",
		Alg: "RS256",
		Kid: s.kid,
		N:   encodeBase64URL(s.pubKey.N.Bytes()),
		E:   encodeIntToBase64URL(s.pubKey.E),
	}
	return &model.JWKSResponse{Keys: []model.JWK{jwk}}
}

// encodeBase64URL 字节 → Base64URL（无填充）
func encodeBase64URL(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// encodeIntToBase64URL RSA 公钥指数 E → Base64URL（大端序无符号整数）
func encodeIntToBase64URL(e int) string {
	// RSA E 通常是 65537（0x10001 = 3 字节）
	if e == 0 {
		return "AQAB" // 65537 的 Base64URL
	}
	var buf []byte
	switch {
	case e <= 0xFF:
		buf = []byte{byte(e)}
	case e <= 0xFFFF:
		buf = make([]byte, 2)
		binary.BigEndian.PutUint16(buf, uint16(e))
	case e <= 0xFFFFFFFF:
		buf = make([]byte, 4)
		binary.BigEndian.PutUint32(buf, uint32(e))
	default:
		bigE := big.NewInt(int64(e))
		buf = bigE.Bytes()
	}
	_ = fmt.Sprintf
	return encodeBase64URL(buf)
}
