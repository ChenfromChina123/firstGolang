package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// RSAKeys 持有 RSA 私钥与公钥 PEM，用于敏感字段（password）的传输加密
// 前端用公钥加密 password，后端用私钥解密，防止明文密码出现在请求体中
type RSAKeys struct {
	privateKey  *rsa.PrivateKey
	publicKeyPEM string
}

// LoadOrGenerateKeys 从 privateKeyPath 加载 RSA 私钥；文件不存在则生成 2048 位新密钥对并落盘
// 持久化密钥可保证重启后公钥不变，前端缓存的公钥不会失效
func LoadOrGenerateKeys(privateKeyPath string) (*RSAKeys, error) {
	data, err := os.ReadFile(privateKeyPath)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, errors.New("RSA private key PEM decode failed")
		}
		if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return &RSAKeys{privateKey: key, publicKeyPEM: marshalPublicKeyPEM(&key.PublicKey)}, nil
		}
		// 尝试 PKCS8
		keyAny, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, fmt.Errorf("parse private key: %v / %v", err, err2)
		}
		key, ok := keyAny.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not RSA")
		}
		return &RSAKeys{privateKey: key, publicKeyPEM: marshalPublicKeyPEM(&key.PublicKey)}, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read private key: %w", err)
	}

	// 生成新密钥对
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate RSA key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(privateKeyPath), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir for private key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(privateKeyPath, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write private key: %w", err)
	}
	return &RSAKeys{privateKey: key, publicKeyPEM: marshalPublicKeyPEM(&key.PublicKey)}, nil
}

// marshalPublicKeyPEM 将公钥序列化为 PKIX PEM（-----BEGIN PUBLIC KEY-----）
func marshalPublicKeyPEM(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// PublicKeyPEM 返回公钥 PEM（供 /api/pubkey 返回给前端）
func (k *RSAKeys) PublicKeyPEM() string {
	return k.publicKeyPEM
}

// PrivateKey 返回 RSA 私钥（用于 JWT RS256 签名等，绝不通过网络传输）
func (k *RSAKeys) PrivateKey() *rsa.PrivateKey {
	return k.privateKey
}

// DecryptBase64 解密前端用公钥加密、base64 编码的密文，返回明文字节
func (k *RSAKeys) DecryptBase64(ciphertextBase64 string) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextBase64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	plaintext, err := rsa.DecryptPKCS1v15(rand.Reader, k.privateKey, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("RSA decrypt: %w", err)
	}
	return plaintext, nil
}

// DecryptPassword 解密密码字段；空字段直接返回空。
// 解密失败返回 error，调用方必须拒绝该请求（不接受明文密码回退）。
func (k *RSAKeys) DecryptPassword(field string) (string, error) {
	if field == "" {
		return "", nil
	}
	plaintext, err := k.DecryptBase64(field)
	if err != nil {
		return "", fmt.Errorf("decrypt password: %w", err)
	}
	return string(plaintext), nil
}
